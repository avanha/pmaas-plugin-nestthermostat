package nestthermostat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"time"

	"crypto/rand"

	"github.com/avanha/pmaas-common/mailbox"
	"github.com/avanha/pmaas-plugin-nestthermostat/config"
	"github.com/avanha/pmaas-plugin-nestthermostat/data"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/common"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/http"
	poller2 "github.com/avanha/pmaas-plugin-nestthermostat/internal/poller"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/pubsub"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
	spi "github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/events"
	"github.com/avanha/pmaas-spi/tracking"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	smartdevicemanagement "google.golang.org/api/smartdevicemanagement/v1"
)

func NewPluginConfig() config.PluginConfig {
	return config.PluginConfig{}
}

// saveQueueStopTimeout bounds how long Stop waits for a pending persistent-config save to finish
// writing to disk before giving up on waiting for it.
const saveQueueStopTimeout = 5 * time.Second

type plugin struct {
	container         spi.IPMAASContainer
	config            config.PluginConfig
	httpHandler       *http.Handler
	thermostats       map[string]*entities.NestThermostat
	cancelWorkers     context.CancelFunc
	workersWg         sync.WaitGroup
	googleUser        string
	oauthClientConfig *oauth2.Config
	oauthAttempt      *oauthAttempt
	oauthClientToken  *oauth2.Token
	oauthClientScope  string
	oauthRefreshToken string

	// saveQueue persists the latest persistent config off the plugin's own mailbox goroutine, so a
	// slow disk write never blocks it. Only the most recent save matters, so a queued-but-not-yet-
	// started save is discarded and replaced whenever a newer one is sent, rather than run in order.
	saveQueue *mailbox.ConflatingMailbox
}

type oauthAttempt struct {
	state string
	// oauthConfig is a per-attempt copy of the plugin's oauth2.Config with RedirectURL resolved from
	// the request that started this attempt. It's reused verbatim for both AuthCodeURL and Exchange,
	// since OAuth requires the redirect_uri to match exactly between the two steps.
	oauthConfig *oauth2.Config
	ctx         context.Context
	cancelFn    context.CancelFunc
}

func (a *oauthAttempt) cancel() bool {
	cancelFn := a.cancelFn

	if cancelFn == nil {
		return false
	}

	cancelFn()
	return true
}

func NewPlugin(cfg config.PluginConfig) spi.IPMAASPlugin {
	return &plugin{
		config:      cfg,
		httpHandler: http.NewHandler(),
		thermostats: make(map[string]*entities.NestThermostat),
	}
}

func (p *plugin) Init(container spi.IPMAASContainer) {
	p.container = container
	p.saveQueue = mailbox.NewConflatingMailbox()

	oauthClientConfig, err := google.ConfigFromJSON(p.config.OAuthClientConfig, smartdevicemanagement.SdmServiceScope)
	if err != nil {
		panic(fmt.Errorf("%T Failed to create OAuth client config: %v", p, err))
	}
	// RedirectURL is intentionally left unset here: it's resolved per-attempt in prepareOAuthAttempt
	// from the server's configured base URLs (via container.GetBaseUrl), since it depends on which of
	// possibly several externally-reachable hostnames the initiating request arrived on.
	currentEndpoint := oauthClientConfig.Endpoint
	oauthClientConfig.Endpoint = oauth2.Endpoint{
		AuthURL:  fmt.Sprintf("https://nestservices.google.com/partnerconnections/%s/auth", p.config.SdmProjectId),
		TokenURL: currentEndpoint.TokenURL,
	}
	p.oauthClientConfig = oauthClientConfig
	p.httpHandler.Init(container, &entityStoreAdapter{parent: p})

	persistentConfig, err := p.container.LoadConfig(func(typeName string) any {
		v1Type := reflect.TypeFor[config.PersistentConfigV1]()
		v1TypeName := v1Type.PkgPath() + "/" + v1Type.Name()

		if typeName == v1TypeName {
			return &config.PersistentConfigV1{}
		}

		return nil
	})

	if persistentConfig != nil {
		persistentConfigV1 := persistentConfig.(*config.PersistentConfigV1)
		p.oauthRefreshToken = persistentConfigV1.RefreshToken
	}
}

func (p *plugin) Start() {
	p.registerPreconfiguredThermostats()

	ctx, cancelFn := context.WithCancel(context.Background())
	p.cancelWorkers = cancelFn

	poller := poller2.NewPoller(
		sdm.ClientOptions{
			// Reuse the ClientID/ClientSecret already parsed from OAuthClientConfig in Init, rather
			// than a separate config field, so there's only one place these credentials can come from.
			ClientId:     p.oauthClientConfig.ClientID,
			ClientSecret: p.oauthClientConfig.ClientSecret,
			SdmProjectID: p.config.SdmProjectId,
		},
		p.currentRefreshToken,
		func(fetchTime time.Time, devices []sdm.DeviceTraits) {
			err := p.container.EnqueueOnPluginGoRoutine(func() { p.handleDeviceList(fetchTime, devices) })

			if err != nil {
				fmt.Printf("%T Failed to enqueue device list update: %v\n", p, err)
			}
		})
	p.workersWg.Go(func() { poller.Run(ctx) })

	subscriber := pubsub.NewSubscriber(
		pubsub.SubscriberOptions{
			ProjectId:           p.config.GcpProjectId,
			SubscriptionId:      p.config.PubSubSubscriptionId,
			ServiceAccountCreds: p.config.ServiceAccountCreds,
		},
		func(deviceId string, timestamp time.Time, traits googleapi.RawMessage) {
			err := p.container.EnqueueOnPluginGoRoutine(func() { p.handleDeviceUpdate(deviceId, timestamp, traits) })

			if err != nil {
				fmt.Printf("%T Failed to enqueue device update: %v\n", p, err)
			}
		})
	p.workersWg.Go(func() { subscriber.Run(ctx) })
}

// registerPreconfiguredThermostats registers an entity for each device id in p.config.ThermostatIds
// that isn't already known, so the UI has something to show for "well known" devices before the first
// successful poll ever completes. Its data is empty until handleDeviceList or handleDeviceUpdate first
// fills it in — a future history-tracking lookup could instead seed it from the last known sample.
func (p *plugin) registerPreconfiguredThermostats() {
	for _, thermostat := range p.config.Thermostats {
		if _, known := p.thermostats[thermostat.ID]; known {
			continue
		}

		p.registerNewThermostat(entities.NewNestThermostat(thermostat.ID, thermostat.Name))
	}
}

// currentRefreshToken returns the plugin's current OAuth refresh token. It's safe to call from any
// goroutine (the poller calls it from its own background goroutine): the read happens on the plugin's
// own mailbox goroutine, same as every other access to plugin state.
func (p *plugin) currentRefreshToken() string {
	token, err := spi.ExecValueFunctionOnPluginGoRoutine(
		p.container,
		func() string { return p.oauthRefreshToken },
		func() string { return "" },
		"unable to read current refresh token")

	if err != nil {
		fmt.Printf("%T Failed to read current refresh token: %v\n", p, err)
	}

	return token
}

// handleDeviceList processes the poller's periodic full snapshot of every device the SDM API returns
// (there's no local allowlist — which devices that is was already decided by the user during the
// SDM/PCM consent flow). A device seen here for the first time is registered as a new entity; one
// already known (including one only pre-registered via registerPreconfiguredThermostats, with no real
// data yet) is updated in place, gated per-trait by sdm.ApplyTraits — see its doc for why a single
// whole-record staleness check isn't enough. Runs on the plugin's own mailbox goroutine (see Start).
func (p *plugin) handleDeviceList(fetchTime time.Time, devices []sdm.DeviceTraits) {
	for _, device := range devices {
		existing, known := p.thermostats[device.Id]

		if !known {
			t := entities.NewNestThermostat(device.Id, "Nest Thermostat")
			sdm.ApplyTraits(t, fetchTime, device.Traits)
			p.registerNewThermostat(t)
			continue
		}

		if sdm.ApplyTraits(existing, fetchTime, device.Traits) {
			p.broadcastThermostatStateChanged(existing)
		}
	}
}

// registerNewThermostat registers t as a new entity and, once that succeeds, starts tracking it in
// p.thermostats. t is stored by pointer and mutated in place by later updates, since the entity's stub
// (and anything that captured a reference to it via the stub) keeps pointing at this exact instance.
func (p *plugin) registerNewThermostat(t *entities.NestThermostat) {
	stub := entities.NewStub(p.container, t)

	pmaasEntityId, err := p.container.RegisterEntity(
		t.Id,
		tracking.TrackableType,
		t.Name.Value,
		func() (any, error) { return stub, nil })

	if err != nil {
		fmt.Printf("%T Failed to register entity for thermostat %s: %v\n", p, t.Id, err)
		return
	}

	t.PmaasEntityId = pmaasEntityId
	p.thermostats[t.Id] = t

	fmt.Printf("%T Registered thermostat %s (%s) as entity %s\n", p, t.Id, t.Name.Value, pmaasEntityId)
}

func (p *plugin) broadcastThermostatStateChanged(t *entities.NestThermostat) {
	event := events.EntityStateChangedEvent{
		EntityEvent: events.EntityEvent{
			Id:         t.PmaasEntityId,
			EntityType: tracking.TrackableType,
			Name:       t.Name.Value,
		},
		NewState: t.GetThermostatData(),
	}

	if err := p.container.BroadcastEvent(t.PmaasEntityId, event); err != nil {
		fmt.Printf("%T Failed to broadcast state change for %s: %v\n", p, t.Id, err)
	}
}

// handleDeviceUpdate processes a single pubsub trait-change notification. Unlike handleDeviceList, it
// never registers a new device: a bare partial trait update doesn't carry enough information (e.g. no
// Info.customName) to stand in for a full device record, so an update for a device not already known
// (from a prior handleDeviceList) is simply discarded. Runs on the plugin's own mailbox goroutine.
func (p *plugin) handleDeviceUpdate(deviceId string, timestamp time.Time, traits googleapi.RawMessage) {
	existing, known := p.thermostats[deviceId]

	if !known {
		fmt.Printf("%T Discarding update for unknown thermostat %s\n", p, deviceId)
		return
	}

	parsedTraits, err := sdm.ParseTraits(traits)

	if err != nil {
		fmt.Printf("%T Failed to parse traits for thermostat %s: %v\n", p, deviceId, err)
		return
	}

	if sdm.ApplyTraits(existing, timestamp, parsedTraits) {
		p.broadcastThermostatStateChanged(existing)
	}
}

func (p *plugin) Stop() chan func() {
	fmt.Printf("%T Stopping...\n", p)

	if p.oauthAttempt != nil && p.oauthAttempt.cancel() {
		p.oauthAttempt = nil
	}

	p.cancelWorkers()

	callbackCh := make(chan func())
	go func() {
		fmt.Printf("%T Waiting for workers to finish...\n", p)
		p.workersWg.Wait()

		fmt.Printf("%T Waiting for pending config save to complete...\n", p)
		saveCtx, cancelSaveCtx := context.WithTimeout(context.Background(), saveQueueStopTimeout)
		defer cancelSaveCtx()

		if err := p.saveQueue.Stop(saveCtx); err != nil {
			fmt.Printf("%T Timed out waiting for pending config save: %v\n", p, err)
		}

		callbackCh <- func() { p.onWorkersStopped(callbackCh) }
	}()

	return callbackCh
}

func (p *plugin) onWorkersStopped(callbackCh chan func()) {
	fmt.Printf("%T Workers stopped, deregistering entities...\n", p)
	p.deregisterEntities()
	close(callbackCh)
}

func (p *plugin) deregisterEntities() {
	for id, t := range p.thermostats {
		if t.PmaasEntityId == "" {
			continue
		}

		if err := p.container.DeregisterEntity(t.PmaasEntityId); err != nil {
			fmt.Printf("%T Failed to deregister thermostat %s: %v\n", p, id, err)
			continue
		}

		t.PmaasEntityId = ""
	}
}

func (p *plugin) getStatusAndEntities() common.StatusAndEntities {
	return common.StatusAndEntities{
		Status: data.PluginStatus{
			GoogleUser: p.googleUser,
		},
	}
}

func (p *plugin) prepareOAuthAttempt(baseUrl string) common.OAuthAttemptOrError {
	if p.oauthAttempt != nil {
		fmt.Printf("Oauth attempt already in progress, cancelling and recreating")
		p.oauthAttempt.cancel()
		p.oauthAttempt = nil
	}

	// Note: The token flow for Nest/SDM doesn't support PKCE, so it's omitted here.

	// Copy the template config so this attempt's RedirectURL doesn't leak into other attempts or
	// concurrent access to p.oauthClientConfig.
	oauthConfig := *p.oauthClientConfig
	oauthConfig.RedirectURL = strings.TrimSuffix(baseUrl, "/") + common.OAuthCallbackPath

	state := generateRandomState()

	// Set ApprovalForce to ensure the user is prompted
	authURL := oauthConfig.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.ApprovalForce)

	ctx, cancelFn := context.WithCancel(context.Background())

	p.oauthAttempt = &oauthAttempt{
		state:       state,
		oauthConfig: &oauthConfig,
		ctx:         ctx,
		cancelFn:    cancelFn,
	}

	return common.OAuthAttemptOrError{
		AuthUri: authURL,
	}
}

func (p *plugin) exchangeCodeForToken(url *url.URL) <-chan error {
	resultCh := make(chan error, 1)
	attempt := p.oauthAttempt

	if attempt == nil {
		resultCh <- fmt.Errorf("oauth attempt not initialized")
		close(resultCh)
		return resultCh
	}

	contextError := attempt.ctx.Err()

	if contextError != nil {
		resultCh <- fmt.Errorf("oauth attempt context already canceled: %v", contextError)
		close(resultCh)
		return resultCh
	}

	state := url.Query().Get("state")

	if state != attempt.state {
		// CSRF mismatch.
		// It also catches the case where a new attempt replaced the old one
		// Don't cancel the attempt, since we don't know if this is the matching one.
		// attempt.cancel()
		// p.oauthAttempt = nil
		resultCh <- errors.New("invalid state")
		close(resultCh)
		return resultCh
	}

	scope := url.Query().Get("scope")
	code := url.Query().Get("code")

	// Use the same per-attempt config (and thus the same RedirectURL) that built the auth URL, since
	// OAuth requires redirect_uri to match exactly between the authorization and token requests.
	oauthClientConfig := attempt.oauthConfig

	p.workersWg.Go(func() {
		defer close(resultCh)

		if attempt.ctx.Err() != nil {
			fmt.Printf("%T Aborting auth code exchange, attempt already canceled\n", p)
			resultCh <- fmt.Errorf("oauth attempt context already canceled")
			return
		}

		// We want to cancel the context before this completes, regardless of the outcome.
		// Generally, onExchangeCodeForTokenComplete will cancel and clear the attempt, but in case
		// we can't actually enqueue the callback, or it never runs, we want to at least cancel the attempt.
		defer attempt.cancel()

		fmt.Printf("Redirect for exchange is: %s\n", oauthClientConfig.RedirectURL)

		token, err := oauthClientConfig.Exchange(
			attempt.ctx,
			code)

		completionError, enqueueError := spi.ExecValueFunctionOnPluginGoRoutine(
			p.container,
			func() error {
				return p.onExchangeCodeForTokenComplete(attempt, token, scope, err)
			},
			func() error { return nil },
			"Failed to enqueue onExchangeCodeForTokenComplete callback")

		if enqueueError != nil {
			fmt.Printf("%T Failed to enqueue onExchangeCodeForTokenComplete callback: %v\n", p, enqueueError)
			resultCh <- fmt.Errorf("failed to enqueue onExchangeCodeForTokenComplete callback: %v", enqueueError)
		}

		if completionError != nil {
			resultCh <- err
		}
	})

	return resultCh
}

func (p *plugin) onExchangeCodeForTokenComplete(attempt *oauthAttempt, token *oauth2.Token,
	scope string, err error) error {
	fmt.Printf("%T onExchangeCodeForTokenComplete, err=%v\n", p, err)

	// Cancel the specific attempt no matter what since it's completing
	attempt.cancel()

	if attempt != p.oauthAttempt {
		fmt.Printf("%T Ignoring stale oauth completion, attempt no longer current\n", p)
		return fmt.Errorf("onExchangeCodeForTokenComplete failed, attempt no longer current")
	}

	// Since it's the current attempt, clear it from the plugin's state.
	p.oauthAttempt = nil

	if err != nil {
		return fmt.Errorf("onExchangeCodeForTokenComplete failed, code exchange failed, err=%v", err)
	}

	p.oauthClientToken = token
	p.oauthRefreshToken = token.RefreshToken
	p.oauthClientScope = scope

	p.saveConfig()

	return nil
}

// saveConfig builds a snapshot of the current persistent config on the plugin's own mailbox goroutine
// (so it's always internally consistent), then hands the actual disk write off to saveQueue. That
// keeps the write itself off the mailbox, and since only the most recent snapshot is ever worth
// persisting, an earlier save still queued when a newer one arrives is simply discarded in favor of it.
func (p *plugin) saveConfig() {
	persistentConfig := config.PersistentConfigV1{
		AccessToken:               p.oauthClientToken.AccessToken,
		AccessTokenType:           p.oauthClientToken.TokenType,
		AccessTokenExpirationTime: p.oauthClientToken.Expiry,
		AccessTokenScopes:         []string{p.oauthClientScope},
		RefreshToken:              p.oauthClientToken.RefreshToken,
	}

	err := p.saveQueue.Send(func() {
		if err := p.container.SaveConfig(persistentConfig); err != nil {
			fmt.Printf("%T Failed to save persistent config: %v\n", p, err)
		}
	})

	if err != nil {
		fmt.Printf("%T Failed to enqueue persistent config save: %v\n", p, err)
	}
}

// generateRandomState creates a cryptographically secure random string
// suitable for use as an OAuth2 CSRF state token.
func generateRandomState() string {
	b := make([]byte, 32)

	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("failed to generate random state: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
