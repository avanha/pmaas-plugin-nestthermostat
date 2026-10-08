package refreshtoken

import (
	"fmt"
	"time"

	"github.com/avanha/pmaas-spi/alert"
)

const (
	// CriticalWindow is how close to its expiration the token must be for the alert to be critical rather
	// than a warning. (The warning starts at ExpiringSoonWindow.)
	CriticalWindow = 2 * time.Hour

	// RecheckInterval is how often the plugin re-evaluates the token, so that the alert follows it as
	// the expiration approaches even when nothing else happens: no poll fails until the token has already
	// stopped working.
	RecheckInterval = time.Hour
)

const (
	alertTitleRejected = "Nest refresh token rejected"
	alertTitleExpired  = "Nest refresh token expired"
	alertTitleExpiring = "Nest refresh token expires soon"
)

// Alert is what the token's status should be reported to the user as.
type Alert struct {
	// Active is false when there's nothing to report, which means any alert already raised for the token
	// should be cleared.
	Active   bool
	Severity alert.Severity
	Title    string
	Message  string
}

// Same reports whether a and b are the same alert as far as the user can tell, ignoring the wording of the
// message, which counts down as the expiration approaches. It's how a caller decides whether something
// changed enough to be worth raising again right now.
func (a Alert) Same(b Alert) bool {
	return a.Active == b.Active && a.Severity == b.Severity && a.Title == b.Title
}

// AlertFor decides how status, as of now, should be reported:
//
//   - A token Google has rejected is critical. It's certain, and the plugin has stopped working.
//   - A token that has expired is critical too, for the same reason: it's the same state, just noticed
//     from the clock before Google had the chance to say so.
//   - A token that expires within CriticalWindow is critical, since the next check may be after it has.
//   - A token that expires within ExpiringSoonWindow is a warning.
//   - Anything else, including no token at all, isn't an alert.
//
// A message about an estimated expiration says so: it's a guess, and may be wrong.
func AlertFor(status Status, now time.Time) Alert {
	switch status.State {
	case StateRejected:
		return Alert{
			Active:   true,
			Severity: alert.SeverityCritical,
			Title:    alertTitleRejected,
			Message: "Google no longer accepts the plugin's refresh token, because it expired or was revoked, " +
				"so the plugin can't poll Google until it's authorized again. " +
				"Open the Nest Thermostats page and use Get Token.",
		}
	case StateExpired:
		return Alert{
			Active:   true,
			Severity: alert.SeverityCritical,
			Title:    alertTitleExpired,
			Message: fmt.Sprintf("The plugin's refresh token expired %s, %s. "+
				"Open the Nest Thermostats page and use Get Token.",
				describeAgo(now.Sub(status.Expiration)), describeBasis(status)),
		}
	case StateExpiringSoon:
		severity := alert.SeverityWarning

		if status.Expiration.Sub(now) <= CriticalWindow {
			severity = alert.SeverityCritical
		}

		return Alert{
			Active:   true,
			Severity: severity,
			Title:    alertTitleExpiring,
			Message: fmt.Sprintf("The plugin's refresh token expires in %s, %s. "+
				"Open the Nest Thermostats page and use Get Token before then.",
				describeSpan(status.Expiration.Sub(now)), describeBasis(status)),
		}
	default:
		return Alert{}
	}
}

// describeBasis says where the expiration came from.
func describeBasis(status Status) string {
	if status.ExpirationKnown {
		return "as reported by Google"
	}

	return "which is an estimate"
}

func describeAgo(d time.Duration) string {
	return describeSpan(d) + " ago"
}

// describeSpan describes a duration in its two most significant units: "6d 23h", "5h 12m", "42m".
func describeSpan(d time.Duration) string {
	d = d.Truncate(time.Minute)

	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
