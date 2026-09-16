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
	state            string
	verifier         string
	exchangeCancelFn context.CancelFunc
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
		fmt.Printf("%T Failed to create OAuth client config: %v", p, err)
		return
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

	if p.oauthAttempt != nil && p.oauthAttempt.exchangeCancelFn != nil {
		p.oauthAttempt.exchangeCancelFn()
		p.oauthAttempt.exchangeCancelFn = nil
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
			AuthUri:    p.oauthClientConfig.AuthCodeURL("", oauth2.AccessTypeOffline),
		},
	}
}

func (p *plugin) prepareOAuthAttempt() (string, error) {
	if p.oauthAttempt != nil {
		return "", fmt.Errorf("oauth attempt already in progress")
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

	p.oauthAttempt = &oauthAttempt{
		state:    state,
		verifier: verifier,
	}

	return authURL, nil
}

func (p *plugin) exchangeCodeForToken(url url.URL) error {
	if p.oauthAttempt == nil {
		return fmt.Errorf("oauth attempt not initialized")
	}

	state := url.Query().Get("state")

	if state != p.oauthAttempt.state {
		// CSRF mismatch
		return errors.New("invalid state")
	}

	code := url.Query().Get("code")

	ctx, cancelFn := context.WithCancel(context.Background())
	verifier := p.oauthAttempt.verifier
	oauthClientConfig := p.oauthClientConfig
	p.oauthAttempt.exchangeCancelFn = cancelFn

	go func() {
		defer cancelFn()
		token, err := oauthClientConfig.Exchange(
			ctx,
			code,
			oauth2.VerifierOption(verifier))

		if ctx.Err() != nil {
			fmt.Printf("%T Token exchange completed, but context already canceled\n", p)
			return
		}

		_, enqueueError := spi.ExecValueFunctionOnPluginGoRoutine(
			p.container,
			func() bool {
				return p.onExchangeCodeForTokenComplete(token, err)
			},
			func() bool { return false },
			"Failed to enqueue onExchangeCodeForTokenComplete callback")

		if enqueueError != nil {
			fmt.Printf("%T Failed to enqueue onExchangeCodeForTokenComplete callback: %v\n", p, enqueueError)
		}
	}()

	return nil
}

func (p *plugin) onExchangeCodeForTokenComplete(token *oauth2.Token, err error) bool {
	fmt.Printf("%T onExchangeCodeForTokenComplete, err=%v\n", p, err)
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
