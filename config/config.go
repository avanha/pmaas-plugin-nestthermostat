package config

type PluginConfig struct {
	GcpProjectId         string   `json:"gcpProjectId"`
	PubSubSubscriptionId string   `json:"pubSubSubscriptionId"`
	SdmProjectId         string   `json:"sdmProjectId"`
	ClientId             string   `json:"clientId"`
	ClientSecret         string   `json:"clientSecret"`
	RefreshToken         string   `json:"refreshToken"`
	ThermostatIds        []string `json:"thermostatIds"`
	ServiceAccountCreds  []byte
}
