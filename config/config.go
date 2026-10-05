package config

import "time"

type Thermostat struct {
	ID   string
	Name string
}

type PluginConfig struct {
	GcpProjectId         string       `json:"gcpProjectId"`
	PubSubSubscriptionId string       `json:"pubSubSubscriptionId"`
	SdmProjectId         string       `json:"sdmProjectId"`
	Thermostats          []Thermostat `json:"thermostats"`
	ServiceAccountCreds  []byte
	// OAuthClientConfig is the raw Google OAuth client credentials JSON (client ID, secret, endpoint).
	// It's the single source of truth for those values — parsed once in Init into oauthClientConfig —
	// rather than duplicating ClientID/ClientSecret as separate plain-string fields here, which would
	// invite the two to drift out of sync.
	OAuthClientConfig []byte

	// RefreshTokenLifetimeEstimate is how long the status page assumes a refresh token lasts when Google
	// doesn't say how long this one will (it does for apps whose OAuth consent screen is in "Testing"
	// status, where refresh tokens last 7 days, and doesn't otherwise). Zero means 7 days. Set it to a
	// negative value to not estimate at all, which is right once the consent screen is in production and
	// refresh tokens no longer expire on a schedule; the status page then shows no expiration unless
	// Google reports one.
	RefreshTokenLifetimeEstimate time.Duration
}

func (c *PluginConfig) AddThermostat(id string, name string) {
	c.Thermostats = append(c.Thermostats, Thermostat{ID: id, Name: name})
}
