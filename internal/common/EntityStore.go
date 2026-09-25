package common

import (
	"net/url"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
)

// OAuthCallbackPath is the path Google redirects back to after the user completes consent. It's
// shared between the route registration (internal/http.Handler.Init) and the redirect_uri built in
// plugin.prepareOAuthAttempt, which must match exactly.
const OAuthCallbackPath = "/plugins/nestthermostat/oauthCallback"

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
