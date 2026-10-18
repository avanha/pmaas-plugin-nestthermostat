package entities

import (
	"fmt"
	"log/slog"
	"sync/atomic"

	spicommon "github.com/avanha/pmaas-spi/common"
	"github.com/avanha/pmaas-spi/environment"
)

// Force implementation of environment.IThermostat
var _ environment.IThermostat = (*thermostatStub)(nil)

// thermostatStub is the thread-safe handle other plugins receive for a registered NestThermostat. It
// implements environment.IThermostat by hopping onto this plugin's goroutine, where all NestThermostat
// state lives, so a consumer (e.g. the environment plugin, seeding a newly discovered thermostat with its
// current state) can read it from any goroutine.
type thermostatStub struct {
	log                    *slog.Logger
	id                     string
	closeFn                func() error
	entityWrapperReference atomic.Pointer[spicommon.ThreadSafeEntityWrapper[environment.IThermostat]]
}

func newThermostatStub(
	log *slog.Logger, id string, entityWrapper *spicommon.ThreadSafeEntityWrapper[environment.IThermostat]) *thermostatStub {
	instance := &thermostatStub{log: log, id: id}
	instance.entityWrapperReference.Store(entityWrapper)

	instance.closeFn = func() error {
		if instance.entityWrapperReference.CompareAndSwap(entityWrapper, nil) {
			instance.closeFn = nil
			return nil
		}

		return fmt.Errorf("failed to clear entity wrapper, current value does not match expected value")
	}

	return instance
}

func (s *thermostatStub) close() {
	closeFn := s.closeFn

	if closeFn == nil {
		return
	}

	if err := closeFn(); err != nil {
		s.log.Error("failed to close thermostat stub", "id", s.id, "error", err)
	}
}

func (s *thermostatStub) GetThermostatData() environment.Thermostat {
	return spicommon.ThreadSafeEntityWrapperExecValueFunc(
		s.entityWrapperReference.Load(),
		func(target environment.IThermostat) environment.Thermostat { return target.GetThermostatData() })
}
