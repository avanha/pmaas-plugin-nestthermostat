package nestthermostat

import (
	"net/url"

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

func (e *entityStoreAdapter) GetOAuthAttempt(baseUrl string) (common.OAuthAttemptOrError, error) {
	// HTTP requests come in on arbitrary goroutines, so execute getStatusAndEntities on the
	// main plugin goroutine
	return spi.ExecValueFunctionOnPluginGoRoutine(
		e.parent.container,
		func() common.OAuthAttemptOrError { return e.parent.prepareOAuthAttempt(baseUrl) },
		func() common.OAuthAttemptOrError { return common.OAuthAttemptOrError{} },
		"unable to prepare OAuth attempt")
}

func (e *entityStoreAdapter) ProcessOAuthCallback(url *url.URL) (<-chan error, error) {
	// HTTP requests come in on arbitrary goroutines, so execute getStatusAndEntities on the
	// main plugin goroutine
	return spi.ExecValueFunctionOnPluginGoRoutine(
		e.parent.container,
		func() <-chan error { return e.parent.exchangeCodeForToken(url) },
		func() <-chan error { return nil },
		"unable to process OAuth callback")
}
