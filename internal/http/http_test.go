package http

import (
	"bytes"
	"html/template"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
)

func TestFormatSpanAndRemaining(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{0, "< 1m"},
		{59 * time.Second, "< 1m"},
		{5 * time.Minute, "5m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h 0m"},
		{5*time.Hour + 12*time.Minute, "5h 12m"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{24 * time.Hour, "1d 0h"},
		{6*24*time.Hour + 23*time.Hour + 30*time.Minute, "6d 23h"},
	} {
		if got := formatSpan(c.d); got != c.want {
			t.Errorf("formatSpan(%v) = %q, want %q", c.d, got, c.want)
		}
	}

	// The margins keep these from being flaky: time passes between building the value and formatting it.
	if got := formatRemaining(time.Now().Add(3*time.Hour + 30*time.Second)); got != "in 3h 0m" {
		t.Errorf("future: got %q", got)
	}

	if got := formatRemaining(time.Now().Add(-(2*time.Hour + 5*time.Minute + 30*time.Second))); got != "2h 5m ago" {
		t.Errorf("past: got %q", got)
	}
}

func renderStatus(t *testing.T, status data.PluginStatus) string {
	t.Helper()

	name := path.Base(statusTemplate.Paths[0])
	tmpl, err := template.New(name).Funcs(statusTemplate.FuncMap).ParseFS(contentFS, "content/"+statusTemplate.Paths[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var out bytes.Buffer
	if err := tmpl.ExecuteTemplate(&out, name, status); err != nil {
		t.Fatalf("execute: %v", err)
	}

	return out.String()
}

func TestStatusTemplate_RefreshTokenStates(t *testing.T) {
	obtained := time.Now().Add(-2 * 24 * time.Hour)

	base := data.PluginStatus{HasRefreshToken: true, RefreshTokenObtainedTime: obtained}

	for _, c := range []struct {
		name        string
		status      func(s data.PluginStatus) data.PluginStatus
		contains    []string
		notContains []string
	}{
		{
			name: "valid, expiration reported by Google",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "valid"
				s.RefreshTokenExpiration = time.Now().Add(5 * 24 * time.Hour)
				s.RefreshTokenExpirationKnown = true
				return s
			},
			contains:    []string{"Configured", "Expires", "(in 4d 23h)"},
			notContains: []string{"(Estimated)", "attention", "Use Get Token"},
		},
		{
			name: "valid, expiration estimated",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "valid"
				s.RefreshTokenExpiration = time.Now().Add(5 * 24 * time.Hour)
				return s
			},
			contains:    []string{"Configured", "(in 4d 23h)", "(Estimated)"},
			notContains: []string{"attention", "Use Get Token"},
		},
		{
			name: "expiring soon",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "expiring"
				s.RefreshTokenExpiration = time.Now().Add(5*time.Hour + 30*time.Second)
				s.RefreshTokenExpirationKnown = true
				return s
			},
			contains:    []string{"Expiring Soon", "token-warning", "(in 5h 0m)"},
			notContains: []string{"attention", "Use Get Token", "(Estimated)"},
		},
		{
			name: "expired, reported by Google",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "expired"
				s.RefreshTokenExpiration = time.Now().Add(-(3*time.Hour + 30*time.Second))
				s.RefreshTokenExpirationKnown = true
				return s
			},
			contains: []string{"Expired", "(3h 0m ago)", "get-token-button attention",
				"can't reach Google until it's authorized again"},
			notContains: []string{"(Estimated)", "estimated expiration has passed"},
		},
		{
			name: "expired, estimated only",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "expired"
				s.RefreshTokenExpiration = time.Now().Add(-time.Hour)
				return s
			},
			contains:    []string{"Expired", "(Estimated)", "get-token-button attention", "estimated expiration has passed"},
			notContains: []string{"can't reach Google"},
		},
		{
			name: "rejected by Google",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "rejected"
				s.RefreshTokenExpiration = time.Now().Add(24 * time.Hour)
				return s
			},
			contains: []string{"Rejected by Google", "get-token-button attention",
				"can't reach Google until it's authorized again"},
			notContains: []string{"estimated expiration has passed"},
		},
		{
			name: "no expiration reported",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "valid"
				s.RefreshTokenNoExpiry = true
				return s
			},
			contains:    []string{"Configured", "None reported"},
			notContains: []string{"(Estimated)", "attention", "the estimate passed"},
		},
		{
			name: "estimate disproven",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.RefreshTokenState = "valid"
				s.RefreshTokenNoExpiry = true
				s.RefreshTokenEstimateDisproven = true
				return s
			},
			contains:    []string{"None reported", "the estimate passed and the token still works"},
			notContains: []string{"(Estimated)", "attention"},
		},
		{
			name: "no token",
			status: func(s data.PluginStatus) data.PluginStatus {
				s.HasRefreshToken = false
				s.RefreshTokenState = "none"
				s.RefreshTokenNoExpiry = true
				return s
			},
			contains:    []string{"Not Configured", "get-token-button attention"},
			notContains: []string{"Obtained", "Expires"},
		},
	} {
		out := renderStatus(t, c.status(base))

		for _, want := range c.contains {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output is missing %q", c.name, want)
			}
		}

		for _, unwanted := range c.notContains {
			if strings.Contains(out, unwanted) {
				t.Errorf("%s: output unexpectedly contains %q", c.name, unwanted)
			}
		}
	}
}
