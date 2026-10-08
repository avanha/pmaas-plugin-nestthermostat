package refreshtoken

import (
	"strings"
	"testing"
	"time"

	"github.com/avanha/pmaas-spi/alert"
)

// statusExpiringIn is the status of a token expiring remaining from now (a negative remaining means it already
// has), either as Google reported it or as the default estimate worked it out.
func statusExpiringIn(remaining time.Duration, known bool) Status {
	if known {
		return Evaluate(Info{
			HasToken: true, Obtained: now.Add(-24 * time.Hour), Expiration: now.Add(remaining), ExpirationKnown: true,
		}, now)
	}

	// Obtained exactly early enough that obtained plus the default estimate is the time wanted.
	return Evaluate(Info{HasToken: true, Obtained: now.Add(remaining - DefaultEstimatedLifetime)}, now)
}

func TestAlertFor_RejectedIsCritical(t *testing.T) {
	got := AlertFor(Evaluate(Info{HasToken: true, Obtained: now, Rejected: true}, now), now)

	if !got.Active || got.Severity != alert.SeverityCritical || got.Title != alertTitleRejected ||
		!strings.Contains(got.Message, "Get Token") {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestAlertFor_ThresholdsOnTheRemainingTime(t *testing.T) {
	for _, c := range []struct {
		name      string
		remaining time.Duration
		active    bool
		severity  alert.Severity
		title     string
	}{
		{"a week left", 7 * 24 * time.Hour, false, 0, ""},
		{"just outside the warning window", 24*time.Hour + time.Minute, false, 0, ""},
		{"exactly the warning window", 24 * time.Hour, true, alert.SeverityWarning, alertTitleExpiring},
		{"a few hours past the critical window", 5 * time.Hour, true, alert.SeverityWarning, alertTitleExpiring},
		{"just outside the critical window", 2*time.Hour + time.Minute, true, alert.SeverityWarning, alertTitleExpiring},
		{"exactly the critical window", 2 * time.Hour, true, alert.SeverityCritical, alertTitleExpiring},
		{"inside the critical window", 45 * time.Minute, true, alert.SeverityCritical, alertTitleExpiring},
		{"a moment left", time.Minute, true, alert.SeverityCritical, alertTitleExpiring},
		{"expiring this instant", 0, true, alert.SeverityCritical, alertTitleExpired},
		{"expired an hour ago", -time.Hour, true, alert.SeverityCritical, alertTitleExpired},
		{"expired a week ago", -7 * 24 * time.Hour, true, alert.SeverityCritical, alertTitleExpired},
	} {
		for _, known := range []bool{true, false} {
			got := AlertFor(statusExpiringIn(c.remaining, known), now)

			if got.Active != c.active || got.Severity != c.severity || got.Title != c.title {
				t.Errorf("%s (known=%v): got %+v", c.name, known, got)
			}
		}
	}
}

func TestAlertFor_NothingToReportWithoutAProblem(t *testing.T) {
	for name, status := range map[string]Status{
		"no token":           Evaluate(Info{}, now),
		"no expiration":      Evaluate(Info{HasToken: true, EstimatedLifetime: -1}, now),
		"estimate disproven": Evaluate(Info{HasToken: true, Obtained: now.Add(-10 * 24 * time.Hour), LastUsedSuccessfully: now}, now),
	} {
		if got := AlertFor(status, now); got.Active || got.Title != "" || got.Message != "" {
			t.Errorf("%s: got %+v", name, got)
		}
	}
}

func TestAlertFor_MessagesSayHowLongAndWhereTheExpirationCameFrom(t *testing.T) {
	known := AlertFor(statusExpiringIn(5*time.Hour+30*time.Minute, true), now)
	if !strings.Contains(known.Message, "expires in 5h 30m") || !strings.Contains(known.Message, "as reported by Google") ||
		strings.Contains(known.Message, "estimate") {
		t.Errorf("unexpected message %q", known.Message)
	}

	estimated := AlertFor(statusExpiringIn(5*time.Hour+30*time.Minute, false), now)
	if !strings.Contains(estimated.Message, "which is an estimate") || strings.Contains(estimated.Message, "reported by Google") {
		t.Errorf("unexpected message %q", estimated.Message)
	}

	expired := AlertFor(statusExpiringIn(-26*time.Hour, true), now)
	if !strings.Contains(expired.Message, "expired 1d 2h ago") {
		t.Errorf("unexpected message %q", expired.Message)
	}

	if !strings.Contains(known.Message, "Get Token") || !strings.Contains(expired.Message, "Get Token") {
		t.Error("an alert should say what to do about it")
	}
}

func TestAlertSame_IgnoresTheCountdownInTheMessage(t *testing.T) {
	a := AlertFor(statusExpiringIn(5*time.Hour, true), now)
	b := AlertFor(statusExpiringIn(4*time.Hour+50*time.Minute, true), now)

	if a.Message == b.Message {
		t.Fatal("test premise: the messages should differ")
	}

	if !a.Same(b) {
		t.Error("the same alert a few minutes later should be the same alert")
	}

	critical := AlertFor(statusExpiringIn(time.Hour, true), now)
	if a.Same(critical) {
		t.Error("an escalation must be a different alert")
	}

	expired := AlertFor(statusExpiringIn(-time.Hour, true), now)
	if critical.Same(expired) {
		t.Error("expiring and expired are different alerts, even at the same severity")
	}

	if a.Same(Alert{}) || !(Alert{}).Same(Alert{}) {
		t.Error("an inactive alert is only the same as another inactive one")
	}
}

func TestDescribeSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "less than a minute", 59 * time.Second: "less than a minute", time.Minute: "1m", 59 * time.Minute: "59m",
		time.Hour: "1h 0m", 5*time.Hour + 12*time.Minute: "5h 12m", 24 * time.Hour: "1d 0h", 6*24*time.Hour + 23*time.Hour: "6d 23h",
	} {
		if got := describeSpan(d); got != want {
			t.Errorf("describeSpan(%v) = %q, want %q", d, got, want)
		}
	}
}
