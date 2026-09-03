package poller

import (
	"context"
	"fmt"
	"iter"
	"sync/atomic"
	"time"

	"github.com/avanha/pmaas-common/slices"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
)

func NewPoller(
	sdmClientOptions sdm.ClientOptions,
	registeredIds iter.Seq[string],
	deviceListHandlerFn func([]entities.NestThermostat)) *Poller {
	deviceIds := make(map[string]bool)

	for id := range registeredIds {
		deviceIds[id] = true
	}

	return &Poller{
		initialDelaySeconds: 30,
		intervalMinutes:     60,
		sdmClientOptions:    sdmClientOptions,
		registeredDevices:   deviceIds,
		deviceListHandlerFn: deviceListHandlerFn,
	}
}

type Poller struct {
	registeredDevices   map[string]bool
	sdmClientOptions    sdm.ClientOptions
	initialDelaySeconds int
	intervalMinutes     time.Duration
	deviceListHandlerFn func([]entities.NestThermostat)
	sdmClient           *sdm.Client
	err                 atomic.Value
}

func (p *Poller) Run(ctx context.Context) {
	run := p.waitForTimer(ctx, time.NewTimer(time.Duration(p.initialDelaySeconds)*time.Second))

	if run {
		sdmClient, err := sdm.NewClient(ctx, p.sdmClientOptions)

		if err != nil {
			clientCreateError := fmt.Errorf("unable to create sdm client: %w", err)
			p.err.Store(clientCreateError)
			fmt.Printf("Poller failed: %v", clientCreateError)
			return
		}

		p.sdmClient = sdmClient
		userInfo, err := sdmClient.FetchUserInfo(ctx)

		if err == nil {
			fmt.Printf("Current user: %s\n", userInfo.Email)
		} else {
			fmt.Printf("Error retrieving user: %v\n", err)
		}

		ticker := time.NewTicker(time.Duration(p.intervalMinutes) * time.Minute)
		defer ticker.Stop()

		for run {
			p.poll(ctx)
			run = p.waitForTick(ctx, ticker)
		}
	}

	fmt.Print("Nest poller terminated\n")
}

func (p *Poller) waitForTimer(ctx context.Context, timer *time.Timer) bool {
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		}
	}
}

func (p *Poller) waitForTick(ctx context.Context, ticker *time.Ticker) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			return true
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	devices, err := p.sdmClient.FetchDevices(ctx)

	if err != nil {
		p.err.Store(err)
		return
	}

	registeredDevices := slices.Filter(devices, func(thermostat *entities.NestThermostat) bool {
		_, ok := p.registeredDevices[thermostat.Id]
		return ok
	})

	p.deviceListHandlerFn(registeredDevices)
}
