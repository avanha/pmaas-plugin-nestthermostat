package nestthermostat

import (
	"github.com/avanha/pmaas-plugin-nestthermostat/internal/common"
	spi "github.com/avanha/pmaas-spi"
)

type entityStoreAdapter struct {
	parent *plugin
}

func (e *entityStoreAdapter) GetStatusAndEntities() (common.StatusAndEntities, error) {
	// HTTP requests come in on arbitrary goroutines, so execute getStatusAndEntities on the
	// main plugin goroutine to get all states atomically.
	return spi.ExecValueFunctionOnPluginGoRoutine(
		e.parent.container,
		e.parent.getStatusAndEntities,
		func() common.StatusAndEntities { return common.StatusAndEntities{} },
		"unable to get status and entities")
}
