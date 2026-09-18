package nestthermostat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"sync"
	"time"

	"crypto/rand"

	"github.com/avanha/pmaas-plugin-nestthermostat/config"
	"github.com/avanha/pmaas-plugin-nestthermostat/data"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/common"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/http"
	poller2 "github.com/avanha/pmaas-plugin-nestthermostat/internal/poller"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/pubsub"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
	"github.com/avanha/pmaas-spi"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	smartdevicemanagement "google.golang.org/api/smartdevicemanagement/v1"
)

func NewPluginConfig() config.PluginConfig {
	return config.PluginConfig{}
}

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
}

type oauthAttempt struct {
	state    string
	verifier string
	ctx      context.Context
	cancelFn context.CancelFunc
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
	oauthClientConfig, err := google.ConfigFromJSON(p.config.OAuthClientConfig, smartdevicemanagement.SdmServiceScope)
	if err != nil {
		panic(fmt.Errorf("%T Failed to create OAuth client config: %v", p, err))
	}
	p.oauthClientConfig = oauthClientConfig
	p.httpHandler.Init(container, &entityStoreAdapter{parent: p})
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

func (p *plugin) prepareOAuthAttempt() (string, error) {
	if p.oauthAttempt != nil {
		fmt.Printf("Oauth attempt already in progress, cancelling and recreating")
		p.oauthAttempt.cancel()
		p.oauthAttempt = nil
	}

	// 1. Generate a high-entropy cryptographically random verifier string (43-128 chars)
	verifier := oauth2.GenerateVerifier()

	// 2. Derive the S256 challenge from the verifier
	challenge := oauth2.S256ChallengeFromVerifier(verifier)

	// 3. (Optional but recommended) Generate a random state token for CSRF protection
	state := generateRandomState()

	authURL := p.oauthClientConfig.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.S256ChallengeOption(challenge))

	ctx, cancelFn := context.WithCancel(context.Background())

	p.oauthAttempt = &oauthAttempt{
		state:    state,
		verifier: verifier,
		ctx:      ctx,
		cancelFn: cancelFn,
	}

	return authURL, nil
}

func (p *plugin) exchangeCodeForToken(url url.URL) error {
	attempt := p.oauthAttempt

	if attempt == nil {
		return fmt.Errorf("oauth attempt not initialized")
	}

	contextError := attempt.ctx.Err()

	if contextError != nil {
		return fmt.Errorf("oauth attempt context already canceled: %v", contextError)
	}

	state := url.Query().Get("state")

	if state != attempt.state {
		// CSRF mismatch.
		// It also catches the case where a new attempt replaced the old one
		// Don't cancel the attempt, since we don't know if this is the matching one.
		// attempt.cancel()
		// p.oauthAttempt = nil
		return errors.New("invalid state")
	}

	code := url.Query().Get("code")

	verifier := attempt.verifier
	oauthClientConfig := p.oauthClientConfig

	p.workersWg.Go(func() {
		if attempt.ctx.Err() != nil {
			fmt.Printf("%T Aborting auth code exchange, attempt already canceled\n", p)
			return
		}

		// We want to cancel the context before this completes, regardless of the outcome.
		// Generally, onExchangeCodeForTokenComplete will cancel and clear the attempt, but in case
		// we can't actually enqueue the callback or it never runs, we want to at least cancel the attempt.
		defer attempt.cancel()

		token, err := oauthClientConfig.Exchange(
			attempt.ctx,
			code,
			oauth2.VerifierOption(verifier))

		_, enqueueError := spi.ExecValueFunctionOnPluginGoRoutine(
			p.container,
			func() bool {
				return p.onExchangeCodeForTokenComplete(attempt, token, err)
			},
			func() bool { return false },
			"Failed to enqueue onExchangeCodeForTokenComplete callback")

		if enqueueError != nil {
			fmt.Printf("%T Failed to enqueue onExchangeCodeForTokenComplete callback: %v\n", p, enqueueError)
		}
	})

	return nil
}

func (p *plugin) onExchangeCodeForTokenComplete(attempt *oauthAttempt, token *oauth2.Token, err error) bool {
	fmt.Printf("%T onExchangeCodeForTokenComplete, err=%v\n", p, err)

	// Cancel the specific attempt no matter what since it's completing
	attempt.cancel()

	if attempt != p.oauthAttempt {
		fmt.Printf("%T Ignoring stale oauth completion, attempt no longer current\n", p)
		return false
	}

	// Since it's the current attempt, clear it from the plugin's state.
	p.oauthAttempt = nil

	if err != nil {
		return false
	}

	p.oauthClientToken = token

	return true
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
