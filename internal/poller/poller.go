package poller

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
)

// NewPoller creates a Poller. sdmClientOptions supplies the static SDM client identity (ClientId,
// ClientSecret, SdmProjectID); its RefreshToken field is ignored — refreshTokenFn is consulted instead,
// every time the poller needs to (re)create its SDM client, since the refresh token may not exist yet
// when the poller starts (no OAuth attempt has completed) and only becomes available later.
//
// There's no device allowlist here: whatever devices FetchDevices returns are passed straight to
// deviceListHandlerFn. Which devices that is is already decided by the user during the SDM/PCM consent
// flow (they explicitly pick which devices to share with this OAuth client), so filtering again locally
// would just be a second, easily-stale copy of that same decision.
func NewPoller(
	sdmClientOptions sdm.ClientOptions,
	refreshTokenFn func() string,
	deviceListHandlerFn func(fetchTime time.Time, devices []sdm.DeviceTraits)) *Poller {
	return &Poller{
		initialDelaySeconds: 30,
		intervalMinutes:     60,
		sdmClientOptions:    sdmClientOptions,
		refreshTokenFn:      refreshTokenFn,
		deviceListHandlerFn: deviceListHandlerFn,
	}
}

type Poller struct {
	sdmClientOptions    sdm.ClientOptions
	refreshTokenFn      func() string
	initialDelaySeconds int
	intervalMinutes     time.Duration
	deviceListHandlerFn func(fetchTime time.Time, devices []sdm.DeviceTraits)
	sdmClient           *sdm.Client
	err                 atomic.Value
}

func (p *Poller) Run(ctx context.Context) {
	run := p.waitForTimer(ctx, time.NewTimer(time.Duration(p.initialDelaySeconds)*time.Second))

	if !run {
		fmt.Print("Nest poller terminated\n")
		return
	}

	ticker := time.NewTicker(time.Duration(p.intervalMinutes) * time.Minute)
	defer ticker.Stop()

	for run {
		p.poll(ctx)
		run = p.waitForTick(ctx, ticker)
	}

	fmt.Print("Nest poller terminated\n")
}

// ensureClient lazily (re)creates the SDM client once a refresh token is available. It's re-checked on
// every poll, not just the first, so the poller recovers on its own once a refresh token that didn't
// exist yet at startup (e.g. no OAuth attempt has completed) becomes available.
func (p *Poller) ensureClient(ctx context.Context) bool {
	if p.sdmClient != nil {
		return true
	}

	refreshToken := p.refreshTokenFn()

	if refreshToken == "" {
		fmt.Print("Nest poller: no refresh token available yet, skipping poll\n")
		return false
	}

	options := p.sdmClientOptions
	options.RefreshToken = refreshToken

	sdmClient, err := sdm.NewClient(ctx, options)

	if err != nil {
		clientCreateError := fmt.Errorf("unable to create sdm client: %w", err)
		p.err.Store(clientCreateError)
		fmt.Printf("Poller failed: %v\n", clientCreateError)
		return false
	}

	//userInfo, err := sdmClient.FetchUserInfo(ctx)
	//
	//if err == nil {
	//	fmt.Printf("Current user: %s\n", userInfo.Email)
	//} else {
	//	fmt.Printf("Error retrieving user: %v\n", err)
	//}

	p.sdmClient = sdmClient
	return true
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
	if !p.ensureClient(ctx) {
		return
	}

	devices, err := p.sdmClient.FetchDevices(ctx)

	if err != nil {
		fmt.Printf("Error fetching devices: %v\n", err)
		p.err.Store(err)
		return
	}

	// The device resource itself carries no per-device or per-trait update timestamp (it's just Name,
	// Type, Traits), so the best available "when was this data current as of" is the time of this poll
	// response — captured once so every device from this same response gets the identical timestamp.
	p.deviceListHandlerFn(time.Now(), devices)
}
