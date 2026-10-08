package nestthermostat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/avanha/pmaas-common/mailbox"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/refreshtoken"
	"github.com/avanha/pmaas-spi/alert"
	"golang.org/x/oauth2"
)

func containerOf(p *plugin) *testContainer {
	return p.container.(*testContainer)
}

func raisedEvent(t *testing.T, event any) alert.RaisedEvent {
	t.Helper()

	raised, ok := event.(alert.RaisedEvent)
	if !ok {
		t.Fatalf("expected a raised event, got %#v", event)
	}

	return raised
}

func TestAlert_ARejectedTokenRaisesACriticalAlertOnce(t *testing.T) {
	p := newTokenTestPlugin()

	p.recordError(rejection())
	p.recordError(rejection()) // every failed poll says the same thing

	events := containerOf(p).alertEvents()
	if len(events) != 1 {
		t.Fatalf("expected one alert, got %+v", events)
	}

	raised := raisedEvent(t, events[0])
	if raised.Source != "nestthermostat" || raised.Key != "refresh-token" || raised.Severity != alert.SeverityCritical ||
		raised.Title != "Nest refresh token rejected" || raised.Message == "" {
		t.Fatalf("unexpected alert %+v", raised)
	}

	// Alerts come from the plugin, not from one of its entities, so the source stands in for the entity id.
	if ids := containerOf(p).broadcastIds; len(ids) != 1 || ids[0] != "nestthermostat" {
		t.Fatalf("unexpected broadcast ids %v", ids)
	}
}

func TestAlert_AnErrorThatIsNotARejectionRaisesNothing(t *testing.T) {
	p := newTokenTestPlugin()

	p.recordError(errors.New("poll: connection reset"))

	if events := containerOf(p).alertEvents(); len(events) != 0 {
		t.Fatalf("unexpected alerts %+v", events)
	}
}

func TestAlert_ASuccessfulPollClearsItOnceAndThenSaysNothing(t *testing.T) {
	p := newTokenTestPlugin()
	p.recordError(rejection())

	p.handleDeviceList(time.Now(), nil)
	p.handleDeviceList(time.Now(), nil)
	p.handleDeviceList(time.Now(), nil)

	events := containerOf(p).alertEvents()
	if len(events) != 2 {
		t.Fatalf("expected a raise and a single clear, got %+v", events)
	}

	cleared, ok := events[1].(alert.ClearedEvent)
	if !ok || cleared.Source != "nestthermostat" || cleared.Key != "refresh-token" {
		t.Fatalf("unexpected second event %#v", events[1])
	}
}

func TestAlert_AHealthyTokenSaysNothingAtAll(t *testing.T) {
	p := newTokenTestPlugin()

	for i := 0; i < 5; i++ {
		p.handleDeviceList(time.Now(), nil)
	}

	p.reportRefreshTokenAlert(true)

	if events := containerOf(p).alertEvents(); len(events) != 0 {
		t.Fatalf("unexpected alerts %+v", events)
	}
}

func TestAlert_AnExpiringTokenIsAWarningThenCriticalAsItGetsCloser(t *testing.T) {
	p := newTokenTestPlugin()

	expiringIn := func(d time.Duration) {
		p.oauthRefreshTokenExpiration = time.Now().Add(d)
		p.oauthRefreshTokenExpirationKnown = true
	}

	// More than a day away: nothing to say.
	expiringIn(30 * time.Hour)
	p.reportRefreshTokenAlert(true)

	if events := containerOf(p).alertEvents(); len(events) != 0 {
		t.Fatalf("unexpected alerts %+v", events)
	}

	expiringIn(10 * time.Hour)
	p.reportRefreshTokenAlert(true)

	expiringIn(90 * time.Minute)
	p.reportRefreshTokenAlert(true)

	expiringIn(-time.Hour)
	p.reportRefreshTokenAlert(true)

	events := containerOf(p).alertEvents()
	if len(events) != 3 {
		t.Fatalf("got %+v", events)
	}

	for i, want := range []struct {
		severity alert.Severity
		title    string
	}{
		{alert.SeverityWarning, "Nest refresh token expires soon"},
		{alert.SeverityCritical, "Nest refresh token expires soon"},
		{alert.SeverityCritical, "Nest refresh token expired"},
	} {
		raised := raisedEvent(t, events[i])
		if raised.Severity != want.severity || raised.Title != want.title {
			t.Errorf("alert %d: got %+v, want %+v", i, raised, want)
		}
	}
}

func TestAlert_TheHourlyCheckRefreshesAnAlertButOtherCallsDoNotRepeatIt(t *testing.T) {
	p := newTokenTestPlugin()
	p.oauthRefreshTokenExpiration = time.Now().Add(10 * time.Hour)
	p.oauthRefreshTokenExpirationKnown = true

	p.reportRefreshTokenAlert(false)
	p.reportRefreshTokenAlert(false) // after, say, a successful poll
	p.handleDeviceList(time.Now(), nil)

	if got := len(containerOf(p).alertEvents()); got != 1 {
		t.Fatalf("expected the unchanged alert to be raised once, got %d events", got)
	}

	// The periodic check forces it, to bring the countdown in the message up to date.
	p.reportRefreshTokenAlert(true)

	if got := len(containerOf(p).alertEvents()); got != 2 {
		t.Fatalf("expected the forced check to raise it again, got %d events", got)
	}
}

func TestAlert_ANewTokenClearsTheAlert(t *testing.T) {
	container := &testContainer{}
	p := newTokenTestPlugin()
	p.container = container
	p.saveQueue = mailbox.NewConflatingMailbox()
	p.recordError(rejection())

	ctx, cancel := context.WithCancel(context.Background())
	attempt := &oauthAttempt{ctx: ctx, cancelFn: cancel}
	p.oauthAttempt = attempt

	token := (&oauth2.Token{AccessToken: "a", RefreshToken: "new"}).
		WithExtra(map[string]any{refreshtoken.ExpiresInKey: float64(604799)})

	if err := p.onExchangeCodeForTokenComplete(attempt, token, "scope", nil); err != nil {
		t.Fatal(err)
	}

	events := container.alertEvents()
	if len(events) != 2 {
		t.Fatalf("expected a raise and a clear, got %+v", events)
	}

	if _, ok := events[1].(alert.ClearedEvent); !ok {
		t.Fatalf("expected the alert to be cleared, got %#v", events[1])
	}
}

func TestAlert_ABroadcastThatFailedIsTriedAgain(t *testing.T) {
	p := newTokenTestPlugin()
	container := containerOf(p)
	container.broadcastErr = errors.New("event manager stopped")

	p.recordError(rejection())

	if p.tokenAlertRaised {
		t.Fatal("an alert that wasn't raised was remembered as raised")
	}

	container.broadcastErr = nil
	p.handleDeviceList(time.Now(), nil) // no longer rejected, so there's nothing to raise now

	p.recordError(rejection())

	if events := container.alertEvents(); len(events) != 1 {
		t.Fatalf("expected the alert to be raised once the broadcast worked, got %+v", events)
	}
}

func TestAlert_AClearThatFailedIsTriedAgain(t *testing.T) {
	p := newTokenTestPlugin()
	container := containerOf(p)
	p.recordError(rejection())

	container.broadcastErr = errors.New("event manager stopped")
	p.handleDeviceList(time.Now(), nil)

	container.broadcastErr = nil
	p.handleDeviceList(time.Now(), nil)

	events := container.alertEvents()
	if len(events) != 2 {
		t.Fatalf("got %+v", events)
	}

	if _, ok := events[1].(alert.ClearedEvent); !ok {
		t.Fatalf("expected the clear to be sent once the broadcast worked, got %#v", events[1])
	}
}

func TestAlert_TheRecheckLoopEvaluatesRegularlyAndStopsWhenAsked(t *testing.T) {
	container := &testContainer{mailbox: mailbox.NewMailbox()}
	defer container.mailbox.Stop()

	p := newTokenTestPlugin()
	p.container = container
	p.tokenAlertInterval = 10 * time.Millisecond
	p.oauthRefreshTokenExpiration = time.Now().Add(10 * time.Hour)
	p.oauthRefreshTokenExpirationKnown = true

	ctx, cancel := context.WithCancel(context.Background())

	var finished sync.WaitGroup
	finished.Go(func() { p.runRefreshTokenAlertCheck(ctx) })

	deadline := time.Now().Add(5 * time.Second)
	for len(container.alertEvents()) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the check didn't run repeatedly: %+v", container.alertEvents())
		}

		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	finished.Wait()

	// Anything already enqueued still runs; after that, nothing more arrives.
	if err := container.mailbox.ExecVoidFn(func() {}); err != nil {
		t.Fatal(err)
	}

	count := len(container.alertEvents())
	time.Sleep(100 * time.Millisecond)

	if got := len(container.alertEvents()); got != count {
		t.Fatalf("the check kept running after it was stopped: %d then %d events", count, got)
	}
}
