package poller

import (
	"context"
	"testing"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
)

func TestPoller_TriggerPoll_WakesWaitForTimer(t *testing.T) {
	p := &Poller{
		triggerCh:             make(chan struct{}, 1),
		sdmClient:             &sdm.Client{},
		lastUserInfoFetchTime: time.Now(),
	}

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	resultCh := make(chan bool, 1)
	go func() { resultCh <- p.waitForTimer(context.Background(), timer) }()

	p.TriggerPoll()

	select {
	case result := <-resultCh:
		if !result {
			t.Fatal("expected waitForTimer to return true after TriggerPoll")
		}
	case <-time.After(time.Second):
		t.Fatal("waitForTimer did not return after TriggerPoll")
	}

	if p.sdmClient != nil {
		t.Error("expected sdmClient to be reset to nil after a trigger")
	}

	if !p.lastUserInfoFetchTime.IsZero() {
		t.Error("expected lastUserInfoFetchTime to be reset to zero after a trigger")
	}
}

func TestPoller_TriggerPoll_WakesWaitForTick(t *testing.T) {
	p := &Poller{
		intervalMinutes:       60,
		triggerCh:             make(chan struct{}, 1),
		sdmClient:             &sdm.Client{},
		lastUserInfoFetchTime: time.Now(),
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	resultCh := make(chan bool, 1)
	go func() { resultCh <- p.waitForTick(context.Background(), ticker) }()

	p.TriggerPoll()

	select {
	case result := <-resultCh:
		if !result {
			t.Fatal("expected waitForTick to return true after TriggerPoll")
		}
	case <-time.After(time.Second):
		t.Fatal("waitForTick did not return after TriggerPoll")
	}

	if p.sdmClient != nil {
		t.Error("expected sdmClient to be reset to nil after a trigger")
	}

	if !p.lastUserInfoFetchTime.IsZero() {
		t.Error("expected lastUserInfoFetchTime to be reset to zero after a trigger")
	}
}

// TestPoller_TriggerPoll_DoesNotBlockWhenAlreadyPending confirms TriggerPoll's documented "no-op if a
// trigger is already pending" contract, rather than blocking or panicking when called twice before the
// first is consumed.
func TestPoller_TriggerPoll_DoesNotBlockWhenAlreadyPending(t *testing.T) {
	p := &Poller{triggerCh: make(chan struct{}, 1)}

	done := make(chan struct{})
	go func() {
		p.TriggerPoll()
		p.TriggerPoll()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("TriggerPoll blocked when a trigger was already pending")
	}
}

func TestPoller_Run_TerminatesOnContextCancellation(t *testing.T) {
	p := &Poller{
		initialDelaySeconds: 0,
		intervalMinutes:     60,
		triggerCh:           make(chan struct{}, 1),
		refreshTokenFn:      func() string { return "" },
		deviceListHandlerFn: func(time.Time, []sdm.DeviceTraits) {},
		userInfoHandlerFn:   func(sdm.UserInfo) {},
		errorHandlerFn:      func(error) {},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not terminate after context cancellation")
	}
}
