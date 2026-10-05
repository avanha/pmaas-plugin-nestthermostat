// Package refreshtoken works out whether the plugin's OAuth refresh token can be relied on.
//
// A refresh token can stop working in two ways the plugin can notice. Google can say when it will
// expire (the refresh_token_expires_in field of the token response, which it sends for apps whose
// consent screen is in "Testing" status, where tokens last 7 days), or it can simply refuse the
// token (an invalid_grant error when exchanging it for an access token), because it expired, was
// revoked, or the account's password changed.
//
// When Google doesn't say when the token expires, the plugin falls back to an estimate. The estimate
// is only a guess, so it's always reported as one, and it corrects itself: if the token is still being
// used successfully after the estimated expiry, the estimate was wrong.
package refreshtoken

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

const (
	// DefaultEstimatedLifetime is how long a refresh token is assumed to last when Google doesn't
	// report it. It's the lifetime Google gives refresh tokens for apps in "Testing" status.
	DefaultEstimatedLifetime = 7 * 24 * time.Hour

	// ExpiringSoonWindow is how long before the expiration the state becomes ExpiringSoon.
	ExpiringSoonWindow = 24 * time.Hour

	// ExpiresInKey is the name of the token response field Google reports the lifetime in, in seconds.
	ExpiresInKey = "refresh_token_expires_in"
)

// State is how far the refresh token can be relied on.
type State int

const (
	// StateNone: there is no refresh token.
	StateNone State = iota
	// StateValid: as far as is known, the token works.
	StateValid
	// StateExpiringSoon: the token will expire within ExpiringSoonWindow.
	StateExpiringSoon
	// StateExpired: the token's expiration time has passed.
	StateExpired
	// StateRejected: Google refused the token. This is a fact rather than a prediction, so it wins
	// over every other state, including one that thinks the token has time left.
	StateRejected
)

// String returns the state's name as the status page's template refers to it.
func (s State) String() string {
	switch s {
	case StateValid:
		return "valid"
	case StateExpiringSoon:
		return "expiring"
	case StateExpired:
		return "expired"
	case StateRejected:
		return "rejected"
	default:
		return "none"
	}
}

// LifetimeFromResponse reads the lifetime Google reports for a refresh token, given the value of
// ExpiresInKey from oauth2.Token.Extra. It reports false if the value is missing or isn't a usable
// number of seconds. Depending on how the token endpoint answered, the number arrives as a float64
// (JSON), a string (form encoded), or something else numeric.
func LifetimeFromResponse(value any) (time.Duration, bool) {
	var seconds float64

	switch v := value.(type) {
	case float64:
		seconds = v
	case float32:
		seconds = float64(v)
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}

		seconds = parsed
	case string:
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}

		seconds = parsed
	default:
		return 0, false
	}

	if seconds <= 0 || seconds > float64(100*365*24*60*60) {
		return 0, false
	}

	return time.Duration(seconds * float64(time.Second)), true
}

// IsRejection reports whether err says Google refused the refresh token: an invalid_grant answer
// from the token endpoint, which is what an expired, revoked or otherwise dead refresh token gets.
// It looks through any wrapping, so it works on errors as they come out of the API clients.
func IsRejection(err error) bool {
	var retrieveError *oauth2.RetrieveError

	return errors.As(err, &retrieveError) && retrieveError.ErrorCode == "invalid_grant"
}

// Info is what's known about the refresh token.
type Info struct {
	HasToken bool

	// Obtained is when the token was obtained. Zero if that wasn't recorded.
	Obtained time.Time

	// Expiration is when Google said the token expires. Only meaningful if ExpirationKnown.
	Expiration      time.Time
	ExpirationKnown bool

	// EstimatedLifetime is how long to assume the token lasts when Google didn't say: zero means
	// DefaultEstimatedLifetime, and a negative value means not to estimate at all.
	EstimatedLifetime time.Duration

	// Rejected is whether Google has refused the token, and not been satisfied since.
	Rejected bool

	// LastUsedSuccessfully is when the token was last used to successfully fetch something. Zero if
	// never, or not since the plugin started.
	LastUsedSuccessfully time.Time
}

// Status is Info interpreted at a point in time.
type Status struct {
	State State

	// Expiration is when the token expires, or zero if there's no expiration to report (see NoExpiry).
	Expiration time.Time

	// ExpirationKnown is true if Google reported Expiration and false if it's an estimate. Only
	// meaningful when Expiration is non-zero.
	ExpirationKnown bool

	// NoExpiry is true if there's no expiration to report: Google didn't give one and there's nothing to
	// estimate one from, or the estimate was proven wrong (EstimateDisproven).
	NoExpiry bool

	// EstimateDisproven is true if the estimated expiration passed and the token has been used
	// successfully since, so the estimate was wrong and the token isn't expiring on that schedule.
	EstimateDisproven bool
}

// Evaluate interprets info as of now.
func Evaluate(info Info, now time.Time) Status {
	if !info.HasToken {
		return Status{State: StateNone, NoExpiry: true}
	}

	status := Status{State: StateValid}

	switch {
	case info.ExpirationKnown:
		status.Expiration = info.Expiration
		status.ExpirationKnown = true
	case info.EstimatedLifetime >= 0 && !info.Obtained.IsZero():
		lifetime := info.EstimatedLifetime
		if lifetime == 0 {
			lifetime = DefaultEstimatedLifetime
		}

		status.Expiration = info.Obtained.Add(lifetime)
	default:
		status.NoExpiry = true
	}

	// An estimate that the token has since outlived was wrong, and shouldn't be shown as if it meant
	// anything. (A reported expiration is never second-guessed: Google said so.)
	if !status.NoExpiry && !status.ExpirationKnown &&
		now.After(status.Expiration) && info.LastUsedSuccessfully.After(status.Expiration) {
		status.Expiration = time.Time{}
		status.NoExpiry = true
		status.EstimateDisproven = true
	}

	switch {
	case info.Rejected:
		status.State = StateRejected
	case status.NoExpiry:
		status.State = StateValid
	case !now.Before(status.Expiration):
		status.State = StateExpired
	case status.Expiration.Sub(now) <= ExpiringSoonWindow:
		status.State = StateExpiringSoon
	}

	return status
}
