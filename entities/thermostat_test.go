package entities_test

import (
	"reflect"
	"testing"

	"github.com/avanha/pmaas-common/lww"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
)

// TestNestThermostat_GetUpdateTimeTrackedFields_IncludesAllTimestampedFields guards
// GetUpdateTimeTrackedFields' hardcoded list against silently falling out of sync with
// NestThermostat's actual lww.Register fields. It does this the "slow but obviously correct" way —
// via reflection — specifically so the production code (GetUpdateTimeTrackedFields, and therefore
// LastUpdateTime) doesn't have to pay reflection's cost or fragility itself.
func TestNestThermostat_GetUpdateTimeTrackedFields_IncludesAllTimestampedFields(t *testing.T) {
	thermostat := entities.NewNestThermostat("id", "name", false)

	gotAddrs := make(map[uintptr]bool)

	for _, field := range thermostat.GetUpdateTimeTrackedFields() {
		gotAddrs[reflect.ValueOf(field).Pointer()] = true
	}

	structValue := reflect.ValueOf(thermostat).Elem()
	structType := structValue.Type()
	timestampedType := reflect.TypeFor[lww.Timestamped]()

	expectedCount := 0

	for i := 0; i < structType.NumField(); i++ {
		field := structValue.Field(i)

		if !field.CanAddr() {
			continue
		}

		fieldPtr := field.Addr()

		// A field satisfies lww.Timestamped via a pointer receiver (see lww.Register.Timestamp), so
		// this checks the pointer-to-field's type, not the field's own value type.
		if !fieldPtr.Type().Implements(timestampedType) {
			continue
		}

		expectedCount++

		if !gotAddrs[fieldPtr.Pointer()] {
			t.Errorf(
				"field %q implements lww.Timestamped but is missing from GetUpdateTimeTrackedFields",
				structType.Field(i).Name)
		}
	}

	if got := len(thermostat.GetUpdateTimeTrackedFields()); got != expectedCount {
		t.Errorf(
			"GetUpdateTimeTrackedFields returned %d fields, but reflection found %d fields implementing lww.Timestamped",
			got, expectedCount)
	}
}
