package nestthermostat

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/avanha/pmaas-common/mailbox"
	"github.com/avanha/pmaas-plugin-nestthermostat/config"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/refreshtoken"
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/sdm"
	spi "github.com/avanha/pmaas-spi"
	"golang.org/x/oauth2"
)

func newTokenTestPlugin() *plugin {
	return &plugin{
		thermostats:                   make(map[string]*entities.NestThermostat),
		oauthRefreshToken:             "refresh-token",
		oauthRefreshTokenObtainedTime: time.Now().Add(-time.Hour),
	}
}

func rejection() error {
	// Shaped like what the API clients return: wrapped more than once around what the token endpoint said.
	return fmt.Errorf("poll: error fetching devices: %w",
		fmt.Errorf("failed to list devices: %w",
			&url.Error{Op: "Get", URL: "https://example", Err: &oauth2.RetrieveError{ErrorCode: "invalid_grant"}}))
}

func TestRefreshTokenStatus_RejectionIsRecordedAndClearedByASuccessfulPoll(t *testing.T) {
	p := newTokenTestPlugin()

	if got := p.getStatusAndEntities().Status.RefreshTokenState; got != "valid" {
		t.Fatalf("expected a fresh token to be valid, got %q", got)
	}

	p.recordError(rejection())

	status := p.getStatusAndEntities().Status
	if status.RefreshTokenState != "rejected" || !status.HasRefreshToken {
		t.Fatalf("expected the token to be rejected, got %+v", status)
	}

	// The error is still the last error shown, as before.
	if status.LastErrorMessage == "" || status.LastErrorTime.IsZero() {
		t.Fatalf("expected the error to be recorded too, got %+v", status)
	}

	p.handleDeviceList(time.Now(), nil)

	if got := p.getStatusAndEntities().Status.RefreshTokenState; got != "valid" {
		t.Fatalf("expected a successful poll to clear the rejection, got %q", got)
	}
}

func TestRefreshTokenStatus_RejectionIsClearedByASuccessfulUserInfoFetch(t *testing.T) {
	p := newTokenTestPlugin()
	p.recordError(rejection())

	p.handleUserInfoUpdate(sdm.UserInfo{})

	if got := p.getStatusAndEntities().Status.RefreshTokenState; got != "valid" {
		t.Fatalf("got %q", got)
	}
}

func TestRefreshTokenStatus_OtherErrorsDoNotMarkTheTokenRejected(t *testing.T) {
	p := newTokenTestPlugin()

	p.recordError(errors.New("poll: error fetching devices: connection reset"))
	p.recordError(fmt.Errorf("wrapped: %w", &oauth2.RetrieveError{ErrorCode: "invalid_client"}))

	if got := p.getStatusAndEntities().Status.RefreshTokenState; got != "valid" {
		t.Fatalf("got %q", got)
	}
}

func TestRefreshTokenStatus_ReportedLifetimeIsKnown(t *testing.T) {
	p := newTokenTestPlugin()
	now := time.Now()

	// As oauth2 presents a JSON token response.
	token := (&oauth2.Token{AccessToken: "a"}).WithExtra(map[string]any{refreshtoken.ExpiresInKey: float64(604799)})
	p.recordRefreshTokenLifetime(token, now)

	if !p.oauthRefreshTokenExpirationKnown || !p.oauthRefreshTokenExpiration.Equal(now.Add(604799*time.Second)) {
		t.Fatalf("unexpected %v, known=%v", p.oauthRefreshTokenExpiration, p.oauthRefreshTokenExpirationKnown)
	}

	p.oauthRefreshTokenObtainedTime = now

	status := p.getStatusAndEntities().Status
	if !status.RefreshTokenExpirationKnown || !status.RefreshTokenExpiration.Equal(p.oauthRefreshTokenExpiration) ||
		status.RefreshTokenNoExpiry {
		t.Fatalf("unexpected status %+v", status)
	}
}

func TestRefreshTokenStatus_EstimatedWhenGoogleDoesNotSay(t *testing.T) {
	p := newTokenTestPlugin()
	p.oauthRefreshTokenExpiration = time.Now().Add(time.Hour) // left over from an earlier token
	p.oauthRefreshTokenExpirationKnown = true

	p.recordRefreshTokenLifetime(&oauth2.Token{AccessToken: "a"}, time.Now())

	if p.oauthRefreshTokenExpirationKnown || !p.oauthRefreshTokenExpiration.IsZero() {
		t.Fatalf("a token without a reported lifetime kept the previous token's expiration")
	}

	status := p.getStatusAndEntities().Status
	want := p.oauthRefreshTokenObtainedTime.Add(refreshtoken.DefaultEstimatedLifetime)

	if status.RefreshTokenExpirationKnown || status.RefreshTokenNoExpiry || !status.RefreshTokenExpiration.Equal(want) {
		t.Fatalf("expected an estimate of %v, got %+v", want, status)
	}
}

func TestRefreshTokenStatus_TheEstimateFollowsTheConfiguration(t *testing.T) {
	obtained := time.Now().Add(-10 * 24 * time.Hour)

	for name, c := range map[string]struct {
		estimate     time.Duration
		wantState    string
		wantNoExpiry bool
	}{
		"default of a week passed":   {0, "expired", false},
		"longer estimate not passed": {30 * 24 * time.Hour, "valid", false},
		"estimate disabled":          {-1, "valid", true},
	} {
		p := newTokenTestPlugin()
		p.oauthRefreshTokenObtainedTime = obtained
		p.config = config.PluginConfig{RefreshTokenLifetimeEstimate: c.estimate}

		status := p.getStatusAndEntities().Status
		if status.RefreshTokenState != c.wantState || status.RefreshTokenNoExpiry != c.wantNoExpiry {
			t.Errorf("%s: got %+v", name, status)
		}
	}
}

func TestRefreshTokenStatus_AnEstimateTheTokenOutlivedIsDisproven(t *testing.T) {
	p := newTokenTestPlugin()
	p.oauthRefreshTokenObtainedTime = time.Now().Add(-10 * 24 * time.Hour)

	if got := p.getStatusAndEntities().Status.RefreshTokenState; got != "expired" {
		t.Fatalf("expected the estimate to have passed, got %q", got)
	}

	p.handleDeviceList(time.Now(), nil)

	status := p.getStatusAndEntities().Status
	if status.RefreshTokenState != "valid" || !status.RefreshTokenEstimateDisproven || !status.RefreshTokenNoExpiry {
		t.Fatalf("expected the working token to disprove the estimate, got %+v", status)
	}
}

type savingContainer struct {
	spi.IPMAASContainer
	mu    sync.Mutex
	saved []any
}

func (c *savingContainer) SaveConfig(cfg any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saved = append(c.saved, cfg)

	return nil
}

func TestOnExchangeCodeForTokenComplete_RecordsAndPersistsTheLifetimeAndClearsRejection(t *testing.T) {
	container := &savingContainer{}
	p := newTokenTestPlugin()
	p.container = container
	p.saveQueue = mailbox.NewConflatingMailbox()
	p.oauthTokenRejected = true
	p.lastTokenUse = time.Now().Add(-time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	attempt := &oauthAttempt{ctx: ctx, cancelFn: cancel}
	p.oauthAttempt = attempt

	token := (&oauth2.Token{AccessToken: "a", RefreshToken: "new-refresh-token"}).
		WithExtra(map[string]any{refreshtoken.ExpiresInKey: float64(604799)})

	before := time.Now()
	if err := p.onExchangeCodeForTokenComplete(attempt, token, "scope", nil); err != nil {
		t.Fatal(err)
	}

	if p.oauthTokenRejected || !p.lastTokenUse.IsZero() {
		t.Fatal("a new token should start with a clean record")
	}

	status := p.getStatusAndEntities().Status
	if status.RefreshTokenState != "valid" || !status.RefreshTokenExpirationKnown ||
		status.RefreshTokenExpiration.Before(before.Add(604799*time.Second)) {
		t.Fatalf("unexpected status %+v", status)
	}

	// Flush the save, then check what reached disk, since the next start reads the expiration from it.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	if err := p.saveQueue.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}

	container.mu.Lock()
	defer container.mu.Unlock()

	if len(container.saved) != 1 {
		t.Fatalf("expected one save, got %d", len(container.saved))
	}

	saved, ok := container.saved[0].(config.PersistentConfigV1)
	if !ok {
		t.Fatalf("unexpected saved type %T", container.saved[0])
	}

	if !saved.RefreshTokenExpirationKnown || !saved.RefreshTokenExpirationTime.Equal(p.oauthRefreshTokenExpiration) ||
		saved.RefreshToken != "new-refresh-token" {
		t.Fatalf("unexpected saved config %+v", saved)
	}
}
