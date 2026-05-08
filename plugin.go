package nestthermostat

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/config"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	poller2 "github.com/avanha/pmaas-plugin-nestthermostat/internal/poller"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/pubsub"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
	"github.com/avanha/pmaas-spi"
	"google.golang.org/api/googleapi"
)

func NewPluginConfig() config.PluginConfig {
	return config.PluginConfig{}
}

type plugin struct {
	container     spi.IPMAASContainer
	config        config.PluginConfig
	thermostats   map[string]*entities.NestThermostat
	cancelWorkers context.CancelFunc
	workersWg     sync.WaitGroup
}

func NewPlugin(cfg config.PluginConfig) spi.IPMAASPlugin {
	return &plugin{
		config:      cfg,
		thermostats: make(map[string]*entities.NestThermostat),
	}
}

func (p *plugin) Init(container spi.IPMAASContainer) {
	p.container = container
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
