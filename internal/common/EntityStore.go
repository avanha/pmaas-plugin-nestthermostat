package common

import (
	"net/url"

	"github.com/avanha/pmaas-plugin-nestthermostat/data"
)

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
	GetOAuthAttempt() (OAuthAttemptOrError, error)
	ProcessOAuthCallback(url *url.URL) (<-chan error, error)
}
