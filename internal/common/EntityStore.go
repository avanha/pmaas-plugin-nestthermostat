package common

import (
	"net/url"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
)

// OAuthCallbackPath is the path, relative to this plugin's namespace, that Google redirects back to
// after the user completes consent. It's shared between the route registration
// (internal/http.Handler.Init, via container.AddRoute) and the redirect_uri built in
// plugin.prepareOAuthAttempt (via spi.PluginFullPath), which must match exactly.
const OAuthCallbackPath = "oauthCallback"

type StatusAndEntities struct {
	Status      data.PluginStatus
	Thermostats []data.ThermostatData
}

type OAuthAttemptOrError struct {
	AuthUri string
	Error   error
}

type ErrorResult struct {
	Error error
}

type EntityStore interface {
	GetStatusAndEntities() (StatusAndEntities, error)
	GetOAuthAttempt(baseUrl string) (OAuthAttemptOrError, error)
	ProcessOAuthCallback(url *url.URL) (<-chan error, error)
}
