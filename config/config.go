package config

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
}

func (c *PluginConfig) AddThermostat(id string, name string) {
	c.Thermostats = append(c.Thermostats, Thermostat{ID: id, Name: name})
}
