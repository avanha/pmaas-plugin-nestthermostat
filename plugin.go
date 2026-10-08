package nestthermostat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
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
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/refreshtoken"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
	spi "github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/alert"
	"github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/events"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	oauth2v2 "google.golang.org/api/oauth2/v2"
	smartdevicemanagement "google.golang.org/api/smartdevicemanagement/v1"
)

func NewPluginConfig() config.PluginConfig {
	return config.PluginConfig{}
}

const (
	// alertSource and alertKeyRefreshToken identify the alert raised about the refresh token: the plugin
	// has at most one at a time, whose severity and wording follow the token's state.
	alertSource          = "nestthermostat"
	alertKeyRefreshToken = "refresh-token"
)

// saveQueueStopTimeout bounds how long Stop waits for a pending persistent-config save to finish
// writing to disk before giving up on waiting for it.
const saveQueueStopTimeout = 5 * time.Second

// IThermostatType is used as the entityType when registering thermostats, so the environment plugin
// (which checks a registered entity's type for assignability to environment.IThermostat) can pick them
// up. Computed locally rather than exported from pmaas-spi/environment, matching how the bluetooth
// plugin computes its own IWirelessThermometerType for the same purpose.
var IThermostatType = reflect.TypeOf((*environment.IThermostat)(nil)).Elem()

type plugin struct {
	container   spi.IPMAASContainer
	config      config.PluginConfig
	httpHandler *http.Handler
	thermostats map[string]*entities.NestThermostat
	// poller is retained so onExchangeCodeForTokenComplete can call TriggerPoll on it after a successful
	// token exchange, rather than leaving a freshly (re)authorized token to sit unused until the next
	// scheduled tick.
	poller *poller2.Poller
	// anonymousThermostatCounter generates unique suffixes for placeholder thermostat names (see
	// nextPlaceholderThermostatName). Only ever touched on the plugin's own mailbox goroutine.
	anonymousThermostatCounter    int
	cancelWorkers                 context.CancelFunc
	workersWg                     sync.WaitGroup
	googleUser                    string
	googleUserPicture             string
	oauthClientConfig             *oauth2.Config
	oauthAttempt                  *oauthAttempt
	oauthClientToken              *oauth2.Token
	oauthClientScope              string
	oauthRefreshToken             string
	oauthRefreshTokenObtainedTime time.Time

	// oauthRefreshTokenExpiration is when Google said the refresh token expires, if it said (see
	// refreshtoken.ExpiresInKey); oauthRefreshTokenExpirationKnown is whether it did. When it didn't, the
	// status page estimates one (see refreshtoken.Evaluate).
	oauthRefreshTokenExpiration      time.Time
	oauthRefreshTokenExpirationKnown bool

	// oauthTokenRejected is whether Google has refused the refresh token (see refreshtoken.IsRejection)
	// and not been satisfied by it since: it's cleared by the next successful use of the token, or by a
	// new token. lastTokenUse is when the token last succeeded in fetching something.
	oauthTokenRejected bool
	lastTokenUse       time.Time

	// tokenAlert is the alert last raised about the refresh token, if tokenAlertRaised, so that one that
	// hasn't changed isn't raised again every time the token is looked at, and so that a clear is only sent
	// for an alert that was raised. tokenAlertInterval is how often the token is re-evaluated; zero means
	// refreshtoken.RecheckInterval.
	tokenAlert         refreshtoken.Alert
	tokenAlertRaised   bool
	tokenAlertInterval time.Duration

	// lastPollTime and lastPubSubMessageTime are the last time each data-ingestion path actually
	// delivered device data — see data.PluginStatus for the same distinction from "was attempted".
	lastPollTime          time.Time
	lastPubSubMessageTime time.Time

	// lastErrorMessage/lastErrorTime record the plugin's most recent error, from any source (OAuth
	// token exchange, polling, pubsub, or SDM trait processing) — see recordError.
	lastErrorMessage string
	lastErrorTime    time.Time

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

func (p *plugin) ShortName() string {
	return "nestthermostat"
}

func (p *plugin) Init(container spi.IPMAASContainer) {
	p.container = container
	p.saveQueue = mailbox.NewConflatingMailbox()

	// The userinfo.email/userinfo.profile scopes are what let FetchUserInfo (see internal/sdm.Client)
	// resolve the logged-in Google account's email and profile picture for the status page — a refresh
	// token obtained before these scopes were added won't have them, and FetchUserInfo will fail for it
	// until the user re-runs the OAuth/PCM consent flow (the "Get Token" button forces reconsent).
	oauthClientConfig, err := google.ConfigFromJSON(
		p.config.OAuthClientConfig,
		smartdevicemanagement.SdmServiceScope,
		oauth2v2.UserinfoEmailScope,
		oauth2v2.UserinfoProfileScope)
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
		p.oauthRefreshTokenObtainedTime = persistentConfigV1.RefreshTokenObtainedTime
		p.oauthRefreshTokenExpiration = persistentConfigV1.RefreshTokenExpirationTime
		p.oauthRefreshTokenExpirationKnown = persistentConfigV1.RefreshTokenExpirationKnown
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
		},
		func(userInfo sdm.UserInfo) {
			err := p.container.EnqueueOnPluginGoRoutine(func() { p.handleUserInfoUpdate(userInfo) })

			if err != nil {
				fmt.Printf("%T Failed to enqueue user info update: %v\n", p, err)
			}
		},
		func(pollErr error) {
			err := p.container.EnqueueOnPluginGoRoutine(func() { p.recordError(pollErr) })

			if err != nil {
				fmt.Printf("%T Failed to enqueue poll error (%v): %v\n", p, pollErr, err)
			}
		})
	p.poller = poller
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
		},
		func(pubsubErr error) {
			err := p.container.EnqueueOnPluginGoRoutine(func() { p.recordError(pubsubErr) })

			if err != nil {
				fmt.Printf("%T Failed to enqueue pubsub error (%v): %v\n", p, pubsubErr, err)
			}
		})
	p.workersWg.Go(func() { subscriber.Run(ctx) })

	// The token may have expired while the plugin wasn't running, and nothing will say so until a poll
	// fails, so check it now, and then regularly: the expiration drawing nearer is silent too.
	p.reportRefreshTokenAlert(true)
	p.workersWg.Go(func() { p.runRefreshTokenAlertCheck(ctx) })
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

		name := thermostat.Name
		nameLocked := name != ""

		if !nameLocked {
			name = p.nextPlaceholderThermostatName()
		}

		p.registerNewThermostat(entities.NewNestThermostat(thermostat.ID, name, nameLocked))
	}
}

// nextPlaceholderThermostatName returns a unique display name for a thermostat that has neither a
// configured name nor, yet, a name reported by the device itself. The placeholder is never locked (see
// entities.NestThermostat.NameLocked), so a real name from telemetry replaces it as soon as one arrives.
func (p *plugin) nextPlaceholderThermostatName() string {
	p.anonymousThermostatCounter++
	return fmt.Sprintf("Nest Thermostat %d", p.anonymousThermostatCounter)
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
	p.lastPollTime = fetchTime
	p.noteTokenUsedSuccessfully(fetchTime)

	for _, device := range devices {
		existing, known := p.thermostats[device.Id]

		if !known {
			t := entities.NewNestThermostat(device.Id, p.nextPlaceholderThermostatName(), false)
			applied := sdm.ApplyTraits(t, fetchTime, device.Traits)

			p.registerNewThermostat(t)

			// Registration only announces id/name/type — it carries no state payload, so without this,
			// whatever traits this same poll response just applied to t would sit unreported until some
			// later poll or pubsub message happened to trigger a broadcast (up to an hour away, given the
			// poller's interval). t.PmaasEntityId is only set once registration actually succeeds.
			if applied && t.PmaasEntityId != "" {
				p.broadcastThermostatStateChanged(t)
			}

			continue
		}

		if sdm.ApplyTraits(existing, fetchTime, device.Traits) {
			p.broadcastThermostatStateChanged(existing)
		}
	}
}

// handleUserInfoUpdate records the logged-in Google account's email/profile picture for status-page
// display. Called separately from handleDeviceList (rather than as an extra parameter on it) since the
// two are independent: the poller only refreshes user info occasionally (see
// poller.userInfoRefreshInterval), not on every device poll. Runs on the plugin's own mailbox goroutine.
func (p *plugin) handleUserInfoUpdate(userInfo sdm.UserInfo) {
	// The poller only calls this after a successful userinfo fetch, which is as good a use of the
	// refresh token as any.
	p.noteTokenUsedSuccessfully(time.Now())

	// Guards against ever clobbering a previously-known-good value with an empty one — shouldn't
	// actually happen given the poller only calls this on a successful fetch, but costs nothing to be
	// defensive about here too.
	if userInfo.Email == "" {
		return
	}

	p.googleUser = userInfo.Email
	p.googleUserPicture = userInfo.Picture
}

// registerNewThermostat registers t as a new entity and, once that succeeds, starts tracking it in
// p.thermostats. It's registered as an environment.IThermostat — advertising the capability, not a
// directly-trackable entity in its own right — so the environment plugin can pick it up and re-host it
// as a tracked, renderable entity, the same way it does for wireless thermometers advertised by the
// bluetooth plugin. A stub is supplied (see entities.NestThermostat.GetStub) so a consumer that starts
// after this thermostat was registered can read its current state, instead of waiting for the next
// state-change event.
func (p *plugin) registerNewThermostat(t *entities.NestThermostat) {
	pmaasEntityId, err := p.container.RegisterEntity(
		t.Id,
		IThermostatType,
		t.Name.Value,
		func() (any, error) { return t.GetStub(p.container), nil })

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
			EntityType: IThermostatType,
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
	p.lastPubSubMessageTime = timestamp

	existing, known := p.thermostats[deviceId]

	if !known {
		fmt.Printf("%T Discarding update for unknown thermostat %s\n", p, deviceId)
		return
	}

	parsedTraits, err := sdm.ParseTraits(traits)

	if err != nil {
		p.recordError(fmt.Errorf("sdm processing: failed to parse traits for thermostat %s: %w", deviceId, err))
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
		t.CloseStubIfPresent()
	}
}

func (p *plugin) getStatusAndEntities() common.StatusAndEntities {
	thermostats := make([]data.ThermostatData, 0, len(p.thermostats))

	for _, thermostat := range p.thermostats {
		thermostats = append(thermostats, thermostat.GetDisplayData())
	}

	sort.Slice(thermostats, func(i, j int) bool { return thermostats[i].Name < thermostats[j].Name })

	token := p.refreshTokenStatus(time.Now())

	return common.StatusAndEntities{
		Status: data.PluginStatus{
			GoogleUser:                    p.googleUser,
			GoogleUserPicture:             p.googleUserPicture,
			HasRefreshToken:               p.oauthRefreshToken != "",
			RefreshTokenObtainedTime:      p.oauthRefreshTokenObtainedTime,
			RefreshTokenState:             token.State.String(),
			RefreshTokenExpiration:        token.Expiration,
			RefreshTokenExpirationKnown:   token.ExpirationKnown,
			RefreshTokenNoExpiry:          token.NoExpiry,
			RefreshTokenEstimateDisproven: token.EstimateDisproven,
			LastPollTime:                  p.lastPollTime,
			LastPubSubMessageTime:         p.lastPubSubMessageTime,
			LastErrorMessage:              p.lastErrorMessage,
			LastErrorTime:                 p.lastErrorTime,
		},
		Thermostats: thermostats,
	}
}

// refreshTokenStatus interprets what's known about the refresh token as of now. Runs on the plugin's own
// goroutine.
func (p *plugin) refreshTokenStatus(now time.Time) refreshtoken.Status {
	return refreshtoken.Evaluate(refreshtoken.Info{
		HasToken:             p.oauthRefreshToken != "",
		Obtained:             p.oauthRefreshTokenObtainedTime,
		Expiration:           p.oauthRefreshTokenExpiration,
		ExpirationKnown:      p.oauthRefreshTokenExpirationKnown,
		EstimatedLifetime:    p.config.RefreshTokenLifetimeEstimate,
		Rejected:             p.oauthTokenRejected,
		LastUsedSuccessfully: p.lastTokenUse,
	}, now)
}

// reportRefreshTokenAlert tells the alert console how the refresh token is doing (see refreshtoken.AlertFor),
// raising or clearing its alert to match. It's called whenever something that affects the token happens, and
// on a timer for the one thing that does so silently, the expiration drawing nearer.
//
// Raising an alert again just updates it, so the periodic check passes force to refresh the message's
// countdown. Without force, an alert is only raised when it's new or has changed in a way the user would
// notice (see refreshtoken.Alert.Same), since this is also called after every successful poll. Runs on
// the plugin's own goroutine.
func (p *plugin) reportRefreshTokenAlert(force bool) {
	now := time.Now()
	desired := refreshtoken.AlertFor(p.refreshTokenStatus(now), now)

	if !desired.Active {
		if p.tokenAlertRaised {
			if err := alert.Clear(p.container, alertSource, alertKeyRefreshToken); err != nil {
				// Still raised as far as the console is concerned, so the next look tries again.
				fmt.Printf("%T Unable to clear the refresh token alert: %v\n", p, err)
				return
			}

			p.tokenAlertRaised = false
			p.tokenAlert = refreshtoken.Alert{}
		}

		return
	}

	if !force && p.tokenAlertRaised && p.tokenAlert.Same(desired) {
		return
	}

	err := alert.Raise(p.container, alertSource, alertKeyRefreshToken, desired.Severity, desired.Title, desired.Message)
	if err != nil {
		// Don't remember an alert that wasn't raised, so that the next look tries again.
		fmt.Printf("%T Unable to raise the refresh token alert: %v\n", p, err)
		return
	}

	p.tokenAlert = desired
	p.tokenAlertRaised = true
}

// runRefreshTokenAlertCheck re-evaluates the refresh token's alert every interval until ctx is done. The
// evaluation itself happens on the plugin's goroutine, where the token's state lives.
func (p *plugin) runRefreshTokenAlertCheck(ctx context.Context) {
	interval := p.tokenAlertInterval

	if interval <= 0 {
		interval = refreshtoken.RecheckInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.container.EnqueueOnPluginGoRoutine(func() { p.reportRefreshTokenAlert(true) }); err != nil {
				fmt.Printf("%T Failed to enqueue the refresh token alert check: %v\n", p, err)
			}
		}
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
	oauthConfig.RedirectURL = strings.TrimSuffix(baseUrl, "/") + p.container.RouteFullPath(common.OAuthCallbackPath)

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
		p.recordError(fmt.Errorf("token exchange failed: %w", err))
		return fmt.Errorf("onExchangeCodeForTokenComplete failed, code exchange failed, err=%v", err)
	}

	now := time.Now()

	p.oauthClientToken = token
	p.oauthRefreshToken = token.RefreshToken
	p.oauthRefreshTokenObtainedTime = now
	p.oauthClientScope = scope
	p.oauthTokenRejected = false
	p.lastTokenUse = time.Time{}
	p.recordRefreshTokenLifetime(token, now)
	p.reportRefreshTokenAlert(false)

	p.saveConfig()

	// p.poller is nil only if Start hasn't run yet, which shouldn't be reachable here in practice (PMAAS
	// only starts accepting HTTP connections, including this OAuth callback, once every plugin's Start
	// has already run), but costs nothing to guard against.
	if p.poller != nil {
		p.poller.TriggerPoll()
	}

	return nil
}

// recordError records err as the plugin's most recent error, for status-page display (see
// data.PluginStatus). Always called on the plugin's own mailbox goroutine — either directly, for errors
// arising from code already running there, or via EnqueueOnPluginGoRoutine, for errors arising on a
// worker's own background goroutine (the poller and pubsub subscriber) — so no locking is needed.
func (p *plugin) recordError(err error) {
	p.lastErrorMessage = err.Error()
	p.lastErrorTime = time.Now()
	fmt.Printf("%T Error: %v\n", p, err)

	// A refresh token Google won't honor any more needs the user to authorize again, which is worth
	// far more than a line in the last-error box: the status page says so (see data.PluginStatus).
	if refreshtoken.IsRejection(err) && !p.oauthTokenRejected {
		p.oauthTokenRejected = true
		fmt.Printf("%T WARNING: Google rejected the refresh token, it has expired or been revoked. "+
			"Authorize the plugin again using Get Token on its status page.\n", p)
		p.reportRefreshTokenAlert(false)
	}
}

// recordRefreshTokenLifetime notes when Google says the refresh token just obtained (at now) expires,
// if it says. Google reports it for apps whose consent screen is in "Testing" status and not otherwise,
// so when it's absent that's normal, and the status page falls back to an estimate. Either way what was
// found is logged, since it's the evidence for which kind of app this is.
func (p *plugin) recordRefreshTokenLifetime(token *oauth2.Token, now time.Time) {
	lifetime, reported := refreshtoken.LifetimeFromResponse(token.Extra(refreshtoken.ExpiresInKey))

	if !reported {
		p.oauthRefreshTokenExpiration = time.Time{}
		p.oauthRefreshTokenExpirationKnown = false
		fmt.Printf("%T Google did not report a refresh token lifetime (%s absent or unusable), "+
			"the expiration shown will be an estimate\n", p, refreshtoken.ExpiresInKey)

		return
	}

	p.oauthRefreshTokenExpiration = now.Add(lifetime)
	p.oauthRefreshTokenExpirationKnown = true
	fmt.Printf("%T Google reports the refresh token expires in %v, at %v\n",
		p, lifetime, p.oauthRefreshTokenExpiration)
}

// noteTokenUsedSuccessfully records that the refresh token just worked: the poller used it to fetch
// something. That clears any earlier rejection, and is what lets an estimated expiration be proven
// wrong (see refreshtoken.Evaluate).
func (p *plugin) noteTokenUsedSuccessfully(at time.Time) {
	p.oauthTokenRejected = false
	p.lastTokenUse = at

	// A token that works clears a rejection, and disproves an estimate that said it had expired.
	p.reportRefreshTokenAlert(false)
}

// saveConfig builds a snapshot of the current persistent config on the plugin's own mailbox goroutine
// (so it's always internally consistent), then hands the actual disk write off to saveQueue. That
// keeps the write itself off the mailbox, and since only the most recent snapshot is ever worth
// persisting, an earlier save still queued when a newer one arrives is simply discarded in favor of it.
func (p *plugin) saveConfig() {
	persistentConfig := config.PersistentConfigV1{
		AccessToken:                 p.oauthClientToken.AccessToken,
		AccessTokenType:             p.oauthClientToken.TokenType,
		AccessTokenExpirationTime:   p.oauthClientToken.Expiry,
		AccessTokenScopes:           []string{p.oauthClientScope},
		RefreshToken:                p.oauthClientToken.RefreshToken,
		RefreshTokenObtainedTime:    p.oauthRefreshTokenObtainedTime,
		RefreshTokenExpirationTime:  p.oauthRefreshTokenExpiration,
		RefreshTokenExpirationKnown: p.oauthRefreshTokenExpirationKnown,
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
