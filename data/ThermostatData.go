package data

import "time"

// ThermostatData is the plugin's own list-page display shape for a thermostat — distinct from
// entities.NestThermostatData (the tracking/history sample shape), since this one also carries static
// identity fields (Id, Name) that a history sample has no reason to repeat on every row.
type ThermostatData struct {
	Id          string
	Name        string
	Temperature float32
	HasHumidity bool
	Humidity    float32
	// HvacStatus is what the system is actually doing right now: "OFF", "HEATING", "COOLING".
	HvacStatus string
	// Mode is the configured mode, independent of HvacStatus: "HEAT", "COOL", "HEATCOOL", "OFF".
	Mode           string
	EcoMode        string
	HeatSetpoint   float32
	CoolSetpoint   float32
	LastUpdateTime time.Time
}
