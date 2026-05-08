package entities

import (
	"reflect"
	"time"

	"github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/tracking"
)

var NestThermostatDataType = reflect.TypeOf((*NestThermostatData)(nil)).Elem()

type NestThermostatData struct {
	Temperature    float32
	Humidity       float32
	HvacStatus     string
	EcoMode        string
	LastUpdateTime time.Time
}

type NestThermostat struct {
	Id             string
	Name           string
	Temperature    float32
	Humidity       float32
	HvacStatus     string
	EcoMode        string
	LastUpdateTime time.Time
}

func (t *NestThermostat) TrackingConfig() tracking.Config {
	return tracking.Config{
		Name:         t.Name,
		TrackingMode: tracking.ModePush,
		Schema: tracking.Schema{
			DataStructType:     NestThermostatDataType,
			InsertArgFactoryFn: NestThermostatDataToInsertArgs,
		},
	}
}

func (t *NestThermostat) Data() tracking.DataSample {
	return tracking.DataSample{
		LastUpdateTime: t.LastUpdateTime,
		Data: NestThermostatData{
			Temperature:    t.Temperature,
			Humidity:       t.Humidity,
			HvacStatus:     t.HvacStatus,
			EcoMode:        t.EcoMode,
			LastUpdateTime: t.LastUpdateTime,
		},
	}
}

func (t *NestThermostat) GetWirelessThermometerData() environment.WirelessThermometer {
	return environment.WirelessThermometer{
		Name: t.Name,
		SensorData: environment.SensorData{
			Temperature:    t.Temperature,
			HasHumidity:    true,
			Humidity:       t.Humidity,
			LastUpdateTime: t.LastUpdateTime,
		},
	}
}

func NestThermostatDataToInsertArgs(anyData *any) ([]any, error) {
	d := (*anyData).(NestThermostatData)
	return []any{d.Temperature, d.Humidity, d.HvacStatus, d.EcoMode, d.LastUpdateTime}, nil
}
