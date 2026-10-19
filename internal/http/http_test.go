package http

import (
	"bytes"
	"path"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
)

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

// The core renders with text/template, which does no escaping, so anything the template interpolates has to
// be escaped by the template itself.
func TestStatusTemplate_EscapesHostileText(t *testing.T) {
	const hostile = `<script>alert(1)</script>`

	status := data.PluginStatus{
		LastErrorTime:    time.Now(),
		LastErrorMessage: "oauth failed: " + hostile,
		GoogleUser:       hostile,
	}

	output := renderStatus(t, status)

	if strings.Contains(output, hostile) {
		t.Fatalf("unescaped text reached the page:\n%s", output)
	}

	if !strings.Contains(output, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("expected the escaped text on the page:\n%s", output)
	}
}

func TestThermostatTemplate_ShowsTwoDecimalsOfTemperatureAndOneOfHumidityWhateverTheReadingHas(t *testing.T) {
	name := path.Base(thermostatTemplate.Paths[0])
	tmpl, err := template.New(name).Funcs(thermostatTemplate.FuncMap).ParseFS(contentFS, "content/"+thermostatTemplate.Paths[0])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var out bytes.Buffer
	thermostat := data.ThermostatData{
		Id: "t1", Name: "Hall", Temperature: 21.123456, HasHumidity: true, Humidity: 36.987654, Mode: "HEATCOOL",
		HeatSetpoint: 19.444444, CoolSetpoint: 24.555555, LastUpdateTime: time.Now(),
	}

	if err := tmpl.ExecuteTemplate(&out, name, thermostat); err != nil {
		t.Fatalf("execute: %v", err)
	}

	page := out.String()
	for _, want := range []string{"21.12 C", "70.02 F", "37.0%", ">19.44<", ">24.56<"} {
		if !strings.Contains(page, want) {
			t.Errorf("missing %q in %s", want, page)
		}
	}

	for _, bad := range []string{"21.123", "36.98", "19.444", "24.555"} {
		if strings.Contains(page, bad) {
			t.Errorf("too many decimal places (%q) in %s", bad, page)
		}
	}
}
