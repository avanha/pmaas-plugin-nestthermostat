package entities

import (
	"reflect"
	"time"

	"github.com/avanha/pmaas-common/lww"
	"github.com/avanha/pmaas-plugin-nestthermostat/data"
	"github.com/avanha/pmaas-spi/environment"
	"github.com/avanha/pmaas-spi/tracking"
)

var NestThermostatDataType = reflect.TypeOf((*NestThermostatData)(nil)).Elem()

type NestThermostatData struct {
	Temperature    float32
	Humidity       float32
	HvacStatus     string
	Mode           string
	EcoMode        string
	HeatSetpoint   float32
	CoolSetpoint   float32
	Connectivity   string
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
	Name        lww.Register[string]
	Temperature lww.Register[float32]
	Humidity    lww.Register[float32]
	HvacStatus  lww.Register[string]
	// Mode is the thermostat's configured mode (HEAT/COOL/HEATCOOL/OFF, from
	// sdm.devices.traits.ThermostatMode) — independent of HvacStatus, which reflects whether it's
	// actually running right now. A HEATCOOL-mode thermostat has both setpoints meaningful at once,
	// regardless of HvacStatus.
	Mode         lww.Register[string]
	EcoMode      lww.Register[string]
	HeatSetpoint lww.Register[float32]
	CoolSetpoint lww.Register[float32]
	// Connectivity is "ONLINE" or "OFFLINE" (sdm.devices.traits.Connectivity).
	Connectivity lww.Register[string]

	// OfflineSince is when Connectivity last transitioned to "OFFLINE" — not a last-write-wins
	// register like the fields above, since it's a derived transition marker rather than a directly
	// received value: a poll reports the full trait set every time, so if this were tracked the same
	// way as Connectivity itself, "still offline" would keep re-stamping it with the current poll time
	// instead of preserving the actual moment it went offline. See sdm.ApplyTraits. Zero value means
	// it's never been observed offline. Deliberately excluded from GetUpdateTimeTrackedFields: it's not
	// a data-freshness signal, and including it would make a stale, disconnected device's LastUpdateTime
	// look artificially recent just because we noticed it went offline recently.
	OfflineSince time.Time

	// PmaasEntityId is the id returned by IPMAASContainer.RegisterEntity once this thermostat has been
	// registered. Empty until then.
	PmaasEntityId string

	// NameLocked is true when the name came from local configuration rather than device telemetry. A
	// locked name is never overwritten by sdm.ApplyTraits.
	NameLocked bool
}

// NewNestThermostat creates a thermostat with the given id and an initial name.
// nameLocked prevents the given name from being overwritten by later updates
func NewNestThermostat(id string, name string, nameLocked bool) *NestThermostat {
	return &NestThermostat{
		Id:         id,
		Name:       lww.Register[string]{Value: name},
		NameLocked: nameLocked,
	}
}

// GetUpdateTimeTrackedFields returns every lww.Register field on this thermostat, as the shared
// lww.Timestamped interface (see pmaas-common/lww).
func (t *NestThermostat) GetUpdateTimeTrackedFields() []lww.Timestamped {
	return []lww.Timestamped{
		&t.Name,
		&t.Temperature,
		&t.Humidity,
		&t.HvacStatus,
		&t.Mode,
		&t.EcoMode,
		&t.HeatSetpoint,
		&t.CoolSetpoint,
		&t.Connectivity,
	}
}

// LastUpdateTime is the most recent of this thermostat's field-level update times. It's computed on
// demand rather than stored, so it can never drift out of sync with the fields it summarizes.
func (t *NestThermostat) LastUpdateTime() time.Time {
	var latest time.Time

	for _, field := range t.GetUpdateTimeTrackedFields() {
		if updateTime := field.Timestamp(); updateTime.After(latest) {
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
			Mode:           t.Mode.Value,
			EcoMode:        t.EcoMode.Value,
			HeatSetpoint:   t.HeatSetpoint.Value,
			CoolSetpoint:   t.CoolSetpoint.Value,
			Connectivity:   t.Connectivity.Value,
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
		Mode:           t.Mode.Value,
		EcoMode:        t.EcoMode.Value,
		HeatSetpoint:   t.HeatSetpoint.Value,
		CoolSetpoint:   t.CoolSetpoint.Value,
		Connectivity:   t.Connectivity.Value,
		OfflineSince:   t.OfflineSince,
		LastUpdateTime: t.LastUpdateTime(),
	}
}

// GetDisplayData returns this thermostat's own list-page display shape (see data.ThermostatData) —
// distinct from GetThermostatData, which is the generic cross-plugin representation broadcast to other
// plugins. This one includes Id, since the plugin's own status page is exactly where a user would want
// to look up a device's id for their local config.
func (t *NestThermostat) GetDisplayData() data.ThermostatData {
	return data.ThermostatData{
		Id:             t.Id,
		Name:           t.Name.Value,
		Temperature:    t.Temperature.Value,
		HasHumidity:    true,
		Humidity:       t.Humidity.Value,
		HvacStatus:     t.HvacStatus.Value,
		Mode:           t.Mode.Value,
		EcoMode:        t.EcoMode.Value,
		HeatSetpoint:   t.HeatSetpoint.Value,
		CoolSetpoint:   t.CoolSetpoint.Value,
		Connectivity:   t.Connectivity.Value,
		OfflineSince:   t.OfflineSince,
		LastUpdateTime: t.LastUpdateTime(),
	}
}

func NestThermostatDataToInsertArgs(anyData *any) ([]any, error) {
	d := (*anyData).(NestThermostatData)
	return []any{
		d.Temperature, d.Humidity, d.HvacStatus, d.Mode, d.EcoMode, d.HeatSetpoint, d.CoolSetpoint,
		d.Connectivity, d.LastUpdateTime,
	}, nil
}
