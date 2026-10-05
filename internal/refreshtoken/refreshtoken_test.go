package refreshtoken

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

var now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestLifetimeFromResponse(t *testing.T) {
	week := 7 * 24 * time.Hour

	for _, c := range []struct {
		name   string
		value  any
		want   time.Duration
		wantOk bool
	}{
		{"json number as float64", float64(604799), 604799 * time.Second, true},
		{"form value as string", "604799", 604799 * time.Second, true},
		{"json.Number", json.Number("604799"), 604799 * time.Second, true},
		{"int", 3600, time.Hour, true},
		{"int64", int64(86400), 24 * time.Hour, true},
		{"a week", float64(week / time.Second), week, true},
		{"missing", nil, 0, false},
		{"empty string", "", 0, false},
		{"not a number", "soon", 0, false},
		{"zero", float64(0), 0, false},
		{"negative", float64(-5), 0, false},
		{"absurdly large", float64(1e18), 0, false},
		{"unsupported type", []int{1}, 0, false},
	} {
		got, ok := LifetimeFromResponse(c.value)
		if ok != c.wantOk || got != c.want {
			t.Errorf("%s: LifetimeFromResponse(%v) = %v, %v; want %v, %v", c.name, c.value, got, ok, c.want, c.wantOk)
		}
	}
}

// What oauth2.Token.Extra actually returns depends on how the endpoint answered, so check the real
// type rather than only the shapes assumed above.
func TestLifetimeFromResponse_FromARealToken(t *testing.T) {
	jsonToken := (&oauth2.Token{AccessToken: "a"}).WithExtra(map[string]any{ExpiresInKey: float64(604799)})
	if got, ok := LifetimeFromResponse(jsonToken.Extra(ExpiresInKey)); !ok || got != 604799*time.Second {
		t.Errorf("JSON-style extra: got %v, %v", got, ok)
	}

	formToken := (&oauth2.Token{AccessToken: "a"}).WithExtra(url.Values{ExpiresInKey: {"604799"}})
	if got, ok := LifetimeFromResponse(formToken.Extra(ExpiresInKey)); !ok || got != 604799*time.Second {
		t.Errorf("form-style extra: got %v, %v", got, ok)
	}

	if _, ok := LifetimeFromResponse((&oauth2.Token{AccessToken: "a"}).Extra(ExpiresInKey)); ok {
		t.Error("a token with no extra fields reported a lifetime")
	}
}

func TestIsRejection(t *testing.T) {
	invalidGrant := &oauth2.RetrieveError{ErrorCode: "invalid_grant", ErrorDescription: "Token has been expired or revoked."}

	if !IsRejection(invalidGrant) {
		t.Error("invalid_grant not recognized")
	}

	// The API clients wrap what the token endpoint returned, more than once.
	wrapped := fmt.Errorf("poll: error fetching devices: %w",
		fmt.Errorf("failed to list devices: %w", &url.Error{Op: "Get", URL: "https://example", Err: invalidGrant}))
	if !IsRejection(wrapped) {
		t.Error("wrapped invalid_grant not recognized")
	}

	for name, err := range map[string]error{
		"nil":                nil,
		"unrelated":          errors.New("boom"),
		"other oauth error":  &oauth2.RetrieveError{ErrorCode: "invalid_client"},
		"no error code":      &oauth2.RetrieveError{},
		"mentions the words": errors.New("invalid_grant"),
	} {
		if IsRejection(err) {
			t.Errorf("%s: wrongly recognized as a rejection", name)
		}
	}
}

func TestEvaluate_NoToken(t *testing.T) {
	got := Evaluate(Info{}, now)

	if got.State != StateNone || !got.NoExpiry {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestEvaluate_ReportedExpiration(t *testing.T) {
	obtained := now.Add(-2 * 24 * time.Hour)
	expiration := obtained.Add(7 * 24 * time.Hour)

	for _, c := range []struct {
		name string
		now  time.Time
		want State
	}{
		{"plenty of time", now, StateValid},
		{"just outside the warning window", expiration.Add(-ExpiringSoonWindow - time.Minute), StateValid},
		{"inside the warning window", expiration.Add(-ExpiringSoonWindow + time.Minute), StateExpiringSoon},
		{"a minute before", expiration.Add(-time.Minute), StateExpiringSoon},
		{"at the moment", expiration, StateExpired},
		{"after", expiration.Add(time.Hour), StateExpired},
	} {
		got := Evaluate(Info{HasToken: true, Obtained: obtained, Expiration: expiration, ExpirationKnown: true}, c.now)

		if got.State != c.want || !got.ExpirationKnown || !got.Expiration.Equal(expiration) || got.NoExpiry {
			t.Errorf("%s: got %+v, want state %v with the reported expiration", c.name, got, c.want)
		}
	}
}

func TestEvaluate_ReportedExpirationBeatsTheEstimate(t *testing.T) {
	obtained := now.Add(-time.Hour)
	reported := obtained.Add(30 * 24 * time.Hour)

	got := Evaluate(Info{HasToken: true, Obtained: obtained, Expiration: reported, ExpirationKnown: true}, now)

	if !got.Expiration.Equal(reported) || !got.ExpirationKnown {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestEvaluate_EstimatesWhenGoogleDidNotSay(t *testing.T) {
	obtained := now.Add(-2 * 24 * time.Hour)

	got := Evaluate(Info{HasToken: true, Obtained: obtained}, now)
	if got.ExpirationKnown || got.NoExpiry || !got.Expiration.Equal(obtained.Add(DefaultEstimatedLifetime)) || got.State != StateValid {
		t.Fatalf("default estimate: unexpected %+v", got)
	}

	got = Evaluate(Info{HasToken: true, Obtained: obtained, EstimatedLifetime: 3 * 24 * time.Hour}, now)
	if got.ExpirationKnown || !got.Expiration.Equal(obtained.Add(3*24*time.Hour)) || got.State != StateExpiringSoon {
		t.Fatalf("configured estimate: unexpected %+v", got)
	}

	got = Evaluate(Info{HasToken: true, Obtained: now.Add(-8 * 24 * time.Hour)}, now)
	if got.State != StateExpired || got.ExpirationKnown {
		t.Fatalf("passed estimate: unexpected %+v", got)
	}
}

func TestEvaluate_NoEstimateWhenDisabledOrImpossible(t *testing.T) {
	for name, info := range map[string]Info{
		"disabled":              {HasToken: true, Obtained: now.Add(-30 * 24 * time.Hour), EstimatedLifetime: -1},
		"obtained time unknown": {HasToken: true},
	} {
		got := Evaluate(info, now)

		if !got.NoExpiry || !got.Expiration.IsZero() || got.State != StateValid || got.EstimateDisproven {
			t.Errorf("%s: unexpected %+v", name, got)
		}
	}
}

func TestEvaluate_AnEstimateTheTokenOutlivedIsDisproven(t *testing.T) {
	obtained := now.Add(-10 * 24 * time.Hour)
	estimatedExpiration := obtained.Add(DefaultEstimatedLifetime)

	// Used successfully after the estimated expiration: the estimate was wrong.
	got := Evaluate(Info{HasToken: true, Obtained: obtained, LastUsedSuccessfully: estimatedExpiration.Add(time.Hour)}, now)
	if got.State != StateValid || !got.NoExpiry || !got.EstimateDisproven || !got.Expiration.IsZero() {
		t.Fatalf("unexpected %+v", got)
	}

	// Last used before the estimated expiration proves nothing.
	got = Evaluate(Info{HasToken: true, Obtained: obtained, LastUsedSuccessfully: estimatedExpiration.Add(-time.Hour)}, now)
	if got.State != StateExpired || got.EstimateDisproven {
		t.Fatalf("unexpected %+v", got)
	}

	// Never used successfully: nothing proves it either.
	got = Evaluate(Info{HasToken: true, Obtained: obtained}, now)
	if got.State != StateExpired || got.EstimateDisproven {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestEvaluate_AReportedExpirationIsNeverSecondGuessed(t *testing.T) {
	expiration := now.Add(-time.Hour)

	got := Evaluate(Info{
		HasToken: true, Expiration: expiration, ExpirationKnown: true, LastUsedSuccessfully: now.Add(-time.Minute),
	}, now)

	if got.State != StateExpired || got.EstimateDisproven || !got.ExpirationKnown {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestEvaluate_RejectionWinsOverEverythingElse(t *testing.T) {
	for name, info := range map[string]Info{
		"with time left":     {HasToken: true, Obtained: now, Expiration: now.Add(30 * 24 * time.Hour), ExpirationKnown: true},
		"estimated":          {HasToken: true, Obtained: now},
		"no expiry":          {HasToken: true, EstimatedLifetime: -1},
		"already expired":    {HasToken: true, Expiration: now.Add(-time.Hour), ExpirationKnown: true},
		"estimate disproven": {HasToken: true, Obtained: now.Add(-10 * 24 * time.Hour), LastUsedSuccessfully: now.Add(-time.Hour)},
	} {
		info.Rejected = true

		if got := Evaluate(info, now); got.State != StateRejected {
			t.Errorf("%s: got state %v, want rejected", name, got.State)
		}
	}

	// Without a token there's nothing to reject.
	if got := Evaluate(Info{Rejected: true}, now); got.State != StateNone {
		t.Errorf("got %v, want none", got.State)
	}
}

func TestState_String(t *testing.T) {
	for state, want := range map[State]string{
		StateNone: "none", StateValid: "valid", StateExpiringSoon: "expiring", StateExpired: "expired", StateRejected: "rejected",
	} {
		if got := state.String(); got != want {
			t.Errorf("%d: got %q, want %q", state, got, want)
		}
	}
}
