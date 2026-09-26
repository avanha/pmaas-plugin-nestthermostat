package nestthermostat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
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
	ctx, cancelFn := context.WithCancel(context.Background())
	p.cancelWorkers = cancelFn
	deviceIds := maps.Keys(p.thermostats)
	poller := poller2.NewPoller(
		sdm.ClientOptions{
			ClientId:     p.config.ClientId,
			ClientSecret: p.config.ClientSecret,
			RefreshToken: p.config.RefreshToken,
		},
		deviceIds,
		p.handleDeviceList)
	p.workersWg.Go(func() { poller.Run(ctx) })

	subscriber := pubsub.NewSubscriber(
		pubsub.SubscriberOptions{
			ProjectId:           p.config.GcpProjectId,
			SubscriptionId:      p.config.PubSubSubscriptionId,
			ServiceAccountCreds: p.config.ServiceAccountCreds,
		},
		deviceIds,
		p.handleDeviceUpdate)
	p.workersWg.Go(func() { subscriber.Run(ctx) })
}

func (p *plugin) handleDeviceList(devices []entities.NestThermostat) {

}

func (p *plugin) handleDeviceUpdate(deviceId string, timestamp time.Time, traits googleapi.RawMessage) {
	//t, ok := p.thermostats[deviceId]
	//if !ok {
	//	return
	//}
	//
	//p.sdmClient.UpdateTraits(t, traits)
	//t.LastUpdateTime = time.Now()
	//
	//event := events.EntityStateChangedEvent{
	//	EntityEvent: events.EntityEvent{
	//		Id:         t.Id,
	//		EntityType: reflect.TypeOf(t),
	//		Name:       t.Name,
	//	},
	//	NewState: t,
	//}
	//
	//err := p.container.BroadcastEvent(t.Id, event)
	//if err != nil {
	//	log.Printf("NestThermostat: Failed to broadcast state change for %s: %v", t.Id, err)
	//}
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
	//p.deregisterEntities()
	close(callbackCh)

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
