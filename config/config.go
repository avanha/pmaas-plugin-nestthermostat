package config

import (
	"sync/atomic"
	"time"

	"github.com/avanha/pmaas-spi/entity"
)

type Thermostat struct {
	ID   string
	Name string
}

type PluginConfig struct {
	GcpProjectId         string       `json:"gcpProjectId"`
	PubSubSubscriptionId string       `json:"pubSubSubscriptionId"`
	SdmProjectId         string       `json:"sdmProjectId"`
	Thermostats          []Thermostat `json:"thermostats"`
	ServiceAccountCreds  []byte
	// OAuthClientConfig is the raw Google OAuth client credentials JSON (client ID, secret, endpoint).
	// It's the single source of truth for those values — parsed once in Init into oauthClientConfig —
	// rather than duplicating ClientID/ClientSecret as separate plain-string fields here, which would
	// invite the two to drift out of sync.
	OAuthClientConfig []byte

	// RefreshTokenLifetimeEstimate is how long the status page assumes a refresh token lasts when Google
	// doesn't say how long this one will (it does for apps whose OAuth consent screen is in "Testing"
	// status, where refresh tokens last 7 days, and doesn't otherwise). Zero means 7 days. Set it to a
	// negative value to not estimate at all, which is right once the consent screen is in production and
	// refresh tokens no longer expire on a schedule; the status page then shows no expiration unless
	// Google reports one.
	RefreshTokenLifetimeEstimate time.Duration

	// handles are the assembly's handles on the thermostats it added, by device id (see AddThermostat). A copy of the config shares them.
	handles map[string]*ThermostatHandle
}

// AddThermostat adds a thermostat to know by its device id and name. The handle it returns names the thermostat's entity to the
// assembly, to put it in a group, and can be ignored.
func (c *PluginConfig) AddThermostat(id string, name string) *ThermostatHandle {
	c.Thermostats = append(c.Thermostats, Thermostat{ID: id, Name: name})

	if c.handles == nil {
		c.handles = make(map[string]*ThermostatHandle)
	}

	handle, ok := c.handles[id]
	if !ok {
		handle = &ThermostatHandle{}
		c.handles[id] = handle
	}

	return handle
}

// HandleFor is the handle on the thermostat with the device id, if the assembly added it with AddThermostat. For the plugin.
func (c PluginConfig) HandleFor(id string) *ThermostatHandle {
	return c.handles[id]
}

// ThermostatHandle is the assembly's handle on a thermostat. It is an entity.Ref, so that the assembly can put the thermostat in a
// group.
type ThermostatHandle struct {
	entityId atomic.Pointer[string]
}

var _ entity.Ref = (*ThermostatHandle)(nil)

// RegisteredId is the id of the thermostat's entity, or false until the plugin has registered it and after it has stopped.
func (h *ThermostatHandle) RegisteredId() (string, bool) {
	if id := h.entityId.Load(); id != nil && *id != "" {
		return *id, true
	}

	return "", false
}

// SetEntityId is for the plugin: the id the entity was registered under, or empty when it is deregistered.
func (h *ThermostatHandle) SetEntityId(id string) {
	if id == "" {
		h.entityId.Store(nil)

		return
	}

	h.entityId.Store(&id)
}
