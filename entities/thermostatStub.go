package entities

import (
	"fmt"
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
	id                     string
	closeFn                func() error
	entityWrapperReference atomic.Pointer[spicommon.ThreadSafeEntityWrapper[environment.IThermostat]]
}

func newThermostatStub(
	id string, entityWrapper *spicommon.ThreadSafeEntityWrapper[environment.IThermostat]) *thermostatStub {
	instance := &thermostatStub{id: id}
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
		fmt.Printf("Failed to close thermostat stub %s: %v\n", s.id, err)
	}
}

func (s *thermostatStub) GetThermostatData() environment.Thermostat {
	return spicommon.ThreadSafeEntityWrapperExecValueFunc(
		s.entityWrapperReference.Load(),
		func(target environment.IThermostat) environment.Thermostat { return target.GetThermostatData() })
}
