package poller

import (
	"context"
	"fmt"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
)

// userInfoRefreshInterval bounds how often maybeRefreshUserInfo actually fetches user info, checked as
// part of each regular poll rather than on a separate timer of its own: the user's Google account
// email/profile picture changing is rare enough that exact timing doesn't matter, and a fetch is cheap
// enough that it doesn't need its own dedicated schedule — just a "don't bother more often than this"
// floor, so it doesn't add an extra API call to every single hourly poll.
const userInfoRefreshInterval = 4 * time.Hour

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
	deviceListHandlerFn func(fetchTime time.Time, devices []sdm.DeviceTraits),
	userInfoHandlerFn func(userInfo sdm.UserInfo),
	errorHandlerFn func(err error)) *Poller {
	return &Poller{
		initialDelaySeconds: 30,
		intervalMinutes:     60,
		sdmClientOptions:    sdmClientOptions,
		refreshTokenFn:      refreshTokenFn,
		deviceListHandlerFn: deviceListHandlerFn,
		userInfoHandlerFn:   userInfoHandlerFn,
		errorHandlerFn:      errorHandlerFn,
		triggerCh:           make(chan struct{}, 1),
	}
}

type Poller struct {
	sdmClientOptions    sdm.ClientOptions
	refreshTokenFn      func() string
	initialDelaySeconds int
	intervalMinutes     time.Duration
	deviceListHandlerFn func(fetchTime time.Time, devices []sdm.DeviceTraits)
	userInfoHandlerFn   func(userInfo sdm.UserInfo)
	errorHandlerFn      func(err error)
	sdmClient           *sdm.Client
	// lastUserInfoFetchTime is zero until the first successful user info fetch. See
	// maybeRefreshUserInfo/userInfoRefreshInterval.
	lastUserInfoFetchTime time.Time
	// triggerCh receives a signal from TriggerPoll. Buffered by 1 so a trigger arriving while a poll is
	// already in flight is queued rather than dropped, but a second trigger before the first is consumed
	// is a no-op rather than piling up.
	triggerCh chan struct{}
}

// TriggerPoll requests an immediate poll cycle rather than waiting for the next scheduled tick, and
// discards the current SDM client first so the new cycle re-authenticates from scratch with whatever
// refresh token is current — e.g. after a successful OAuth token exchange, so a freshly (re)authorized
// token (which may grant access to additional devices, in the "force the flow to pick up new devices"
// case — see plugin.go's "Get Token" button) takes effect immediately, rather than sitting unused for up
// to an hour, or indefinitely if a client built from an older token was already running. Safe to call
// from any goroutine; a no-op if a trigger is already pending.
func (p *Poller) TriggerPoll() {
	select {
	case p.triggerCh <- struct{}{}:
	default:
	}
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
		p.errorHandlerFn(fmt.Errorf("poll: unable to create sdm client: %w", err))
		return false
	}

	p.sdmClient = sdmClient
	return true
}

// maybeRefreshUserInfo fetches the current user info if it's never been fetched, or if
// userInfoRefreshInterval has elapsed since the last fetch. A failed fetch doesn't prevent device polling
// from proceeding — it's just surfaced as an error, and left to retry on the next poll (rather than
// immediately) — the same rationale as the refresh interval itself: nothing about this needs to be fast.
func (p *Poller) maybeRefreshUserInfo(ctx context.Context) {
	if !p.lastUserInfoFetchTime.IsZero() && time.Since(p.lastUserInfoFetchTime) < userInfoRefreshInterval {
		return
	}

	userInfo, err := p.sdmClient.FetchUserInfo(ctx)

	if err != nil {
		p.errorHandlerFn(fmt.Errorf("poll: unable to fetch user info: %w", err))
		return
	}

	p.lastUserInfoFetchTime = time.Now()
	p.userInfoHandlerFn(userInfo)
}

func (p *Poller) waitForTimer(ctx context.Context, timer *time.Timer) bool {
	for {
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
			return true
		case <-p.triggerCh:
			timer.Stop()
			p.resetForTrigger()
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
		case <-p.triggerCh:
			// Reset so the regular schedule restarts from this triggered poll, rather than the next
			// regularly-scheduled tick landing right on top of it.
			ticker.Reset(time.Duration(p.intervalMinutes) * time.Minute)
			p.resetForTrigger()
			return true
		}
	}
}

// resetForTrigger discards state tied to the old refresh token/client, so the poll cycle TriggerPoll is
// about to cause rebuilds everything from scratch — including user info, since a re-authorization could
// just as easily be for a different Google account entirely. Always called from the poller's own
// goroutine (via waitForTimer/waitForTick), same as every other access to this state, so this is safe
// despite TriggerPoll itself being callable from elsewhere.
func (p *Poller) resetForTrigger() {
	p.sdmClient = nil
	p.lastUserInfoFetchTime = time.Time{}
}

func (p *Poller) poll(ctx context.Context) {
	if !p.ensureClient(ctx) {
		return
	}

	p.maybeRefreshUserInfo(ctx)

	devices, err := p.sdmClient.FetchDevices(ctx)

	if err != nil {
		p.errorHandlerFn(fmt.Errorf("poll: error fetching devices: %w", err))
		return
	}

	// The device resource itself carries no per-device or per-trait update timestamp (it's just Name,
	// Type, Traits), so the best available "when was this data current as of" is the time of this poll
	// response — captured once so every device from this same response gets the identical timestamp.
	p.deviceListHandlerFn(time.Now(), devices)
}
