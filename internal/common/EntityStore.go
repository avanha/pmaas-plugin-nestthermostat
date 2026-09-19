package common

import "github.com/avanha/pmaas-plugin-nestthermostat/data"

type StatusAndEntities struct {
	Status      data.PluginStatus
	Thermostats []data.ThermostatData
}

type OAuthAttemptOrError struct {
	AuthUri string
	Error   error
}

type EntityStore interface {
	GetStatusAndEntities() (StatusAndEntities, error)
	GetOAuthAttempt() (OAuthAttemptOrError, error)
}
