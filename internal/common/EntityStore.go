package common

import "github.com/avanha/pmaas-plugin-nestthermostat/data"

type StatusAndEntities struct {
	Status      data.PluginStatus
	Thermostats []data.ThermostatData
}

type EntityStore interface {
	GetStatusAndEntities() (StatusAndEntities, error)
}
