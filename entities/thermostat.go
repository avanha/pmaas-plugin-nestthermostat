package entities

import (
	"reflect"
	"time"

	"github.com/avanha/pmaas-common/lww"
	"github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/tracking"
)

var NestThermostatDataType = reflect.TypeOf((*NestThermostatData)(nil)).Elem()

type NestThermostatData struct {
	Temperature    float32
	Humidity       float32
	HvacStatus     string
	EcoMode        string
	HeatSetpoint   float32
	CoolSetpoint   float32
	LastUpdateTime time.Time
}

type NestThermostat struct {
	Id string

	// Each of these is a last-write-wins register (see pmaas-common/lww): it tracks its own value
	// together with the time it was last set, and only accepts a new value if the new value's own
	// reported time is actually after that — independently of the others. That's needed because SDM
	// traits update independently of one another, and neither polling nor pubsub delivery order
	// reflects the order the underlying changes actually happened in: comparing an incoming update
	// against one whole-entity timestamp would let an update to one trait wrongly discard an equally
	// legitimate, individually-still-current update previously received for another. See
	// sdm.ApplyTraits, which is what actually calls Set on these.
	Name         lww.Register[string]
	Temperature  lww.Register[float32]
	Humidity     lww.Register[float32]
	HvacStatus   lww.Register[string]
	EcoMode      lww.Register[string]
	HeatSetpoint lww.Register[float32]
	CoolSetpoint lww.Register[float32]

	// PmaasEntityId is the id returned by IPMAASContainer.RegisterEntity once this thermostat has been
	// registered. Empty until then.
	PmaasEntityId string
}

// NewNestThermostat creates a thermostat with the given id and an initial name, before any trait data
// has been applied to it (e.g. a device only pre-registered from configuration, or one just discovered
// by a poll before sdm.ApplyTraits has run against it).
func NewNestThermostat(id string, name string) *NestThermostat {
	return &NestThermostat{
		Id:   id,
		Name: lww.Register[string]{Value: name},
	}
}

// LastUpdateTime is the most recent of this thermostat's field-level update times. It's computed on
// demand rather than stored, so it can never drift out of sync with the fields it summarizes.
func (t *NestThermostat) LastUpdateTime() time.Time {
	latest := t.Name.UpdateTime

	for _, updateTime := range [...]time.Time{
		t.Temperature.UpdateTime,
		t.Humidity.UpdateTime,
		t.HvacStatus.UpdateTime,
		t.EcoMode.UpdateTime,
		t.HeatSetpoint.UpdateTime,
		t.CoolSetpoint.UpdateTime,
	} {
		if updateTime.After(latest) {
			latest = updateTime
		}
	}

	return latest
}

func (t *NestThermostat) TrackingConfig() tracking.Config {
	return tracking.Config{
		Name:         t.Name.Value,
		TrackingMode: tracking.ModePush,
		Schema: tracking.Schema{
			DataStructType:     NestThermostatDataType,
			InsertArgFactoryFn: NestThermostatDataToInsertArgs,
		},
	}
}

func (t *NestThermostat) Data() tracking.DataSample {
	lastUpdateTime := t.LastUpdateTime()

	return tracking.DataSample{
		LastUpdateTime: lastUpdateTime,
		Data: NestThermostatData{
			Temperature:    t.Temperature.Value,
			Humidity:       t.Humidity.Value,
			HvacStatus:     t.HvacStatus.Value,
			EcoMode:        t.EcoMode.Value,
			HeatSetpoint:   t.HeatSetpoint.Value,
			CoolSetpoint:   t.CoolSetpoint.Value,
			LastUpdateTime: lastUpdateTime,
		},
	}
}

// GetThermostatData returns the generic, producer-facing view of this thermostat (see
// environment.Thermostat) — the external representation broadcast in state-change events, decoupled
// from NestThermostat's own internal field set so a future internal-only field never leaks out just by
// existing.
func (t *NestThermostat) GetThermostatData() environment.Thermostat {
	return environment.Thermostat{
		Name: t.Name.Value,
		SensorData: environment.SensorData{
			Temperature:    t.Temperature.Value,
			HasHumidity:    true,
			Humidity:       t.Humidity.Value,
			LastUpdateTime: t.LastUpdateTime(),
		},
		HvacStatus:     t.HvacStatus.Value,
		EcoMode:        t.EcoMode.Value,
		HeatSetpoint:   t.HeatSetpoint.Value,
		CoolSetpoint:   t.CoolSetpoint.Value,
		LastUpdateTime: t.LastUpdateTime(),
	}
}

func NestThermostatDataToInsertArgs(anyData *any) ([]any, error) {
	d := (*anyData).(NestThermostatData)
	return []any{d.Temperature, d.Humidity, d.HvacStatus, d.EcoMode, d.HeatSetpoint, d.CoolSetpoint, d.LastUpdateTime}, nil
}
