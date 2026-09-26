package entities

import (
	spi "github.com/avanha/pmaas-spi"
	"github.com/avanha/pmaas-spi/common"
	"github.com/avanha/pmaas-spi/tracking"
)

// Stub is a thread-safe handle to a NestThermostat: every call is marshaled onto the owning plugin's
// goroutine, so it's safe to call from any goroutine (rendering, tracking history, etc.) — unlike the
// NestThermostat it wraps, which is only ever safe to touch on the plugin's own goroutine.
type Stub struct {
	wrapper *common.ThreadSafeEntityWrapper[*NestThermostat]
}

func NewStub(container spi.IPMAASContainer, thermostat *NestThermostat) *Stub {
	return &Stub{
		wrapper: &common.ThreadSafeEntityWrapper[*NestThermostat]{
			Container: container,
			Entity:    thermostat,
		},
	}
}

func (s *Stub) TrackingConfig() tracking.Config {
	return common.ThreadSafeEntityWrapperExecValueFunc(
		s.wrapper,
		func(t *NestThermostat) tracking.Config { return t.TrackingConfig() })
}

func (s *Stub) Data() tracking.DataSample {
	return common.ThreadSafeEntityWrapperExecValueFunc(
		s.wrapper,
		func(t *NestThermostat) tracking.DataSample { return t.Data() })
}

var _ tracking.Trackable = (*Stub)(nil)
