package sdm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/avanha/pmaas-common/lww"
	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	smartdevicemanagement "google.golang.org/api/smartdevicemanagement/v1"
)

type ClientOptions struct {
	ClientId     string
	ClientSecret string
	RefreshToken string
	SdmProjectID string
}

type UserInfo struct {
	Email string `json:"email"`
}

// Traits mirror the subset of SDM device trait namespaces this plugin understands. Every field is a
// pointer so ParseTraits/ApplyTraits can tell "this trait wasn't included in this update" (nil) apart
// from "this trait was included, and its value happens to be zero".  Importants because pubsub
// device-update messages only carry the traits that actually changed, not a full snapshot, while
// FetchDevices' response carries all of them. See ApplyTraits.
type Traits struct {
	Info                          *InfoTrait                          `json:"sdm.devices.traits.Info,omitempty"`
	Temperature                   *TemperatureTrait                   `json:"sdm.devices.traits.Temperature,omitempty"`
	Humidity                      *HumidityTrait                      `json:"sdm.devices.traits.Humidity,omitempty"`
	ThermostatHvac                *ThermostatHvacTrait                `json:"sdm.devices.traits.ThermostatHvac,omitempty"`
	ThermostatMode                *ThermostatModeTrait                `json:"sdm.devices.traits.ThermostatMode,omitempty"`
	ThermostatEco                 *ThermostatEcoTrait                 `json:"sdm.devices.traits.ThermostatEco,omitempty"`
	ThermostatTemperatureSetpoint *ThermostatTemperatureSetpointTrait `json:"sdm.devices.traits.ThermostatTemperatureSetpoint,omitempty"`

	// FallbackName isn't an SDM trait at all — it's composed by FetchDevices from the device's
	// ParentRelations (structure/room assignment) and Info.CustomName (as a parenthetical label), for
	// when CustomName alone isn't the full picture the Nest/Home app shows. Always nil for Traits
	// parsed from a pubsub message, since that payload doesn't carry room/structure assignment at all.
	FallbackName *string `json:"-"`
}

type InfoTrait struct {
	CustomName string `json:"customName"`
}

type TemperatureTrait struct {
	AmbientTemperatureCelsius float32 `json:"ambientTemperatureCelsius"`
}

type HumidityTrait struct {
	AmbientHumidityPercent float32 `json:"ambientHumidityPercent"`
}

// ThermostatHvacTrait's Status reflects what the system is actually doing right now — one of "OFF",
// "HEATING", "COOLING" — as distinct from ThermostatModeTrait, which is the configured mode
// (independent of whether it's currently running).
type ThermostatHvacTrait struct {
	Status string `json:"status"`
}

// ThermostatModeTrait's Mode is the thermostat's configured mode: one of "HEAT", "COOL", "HEATCOOL",
// "OFF". A HEATCOOL-mode thermostat has both a heat and a cool setpoint meaningful at once, regardless
// of whether ThermostatHvacTrait.Status currently shows it actively running either one.
type ThermostatModeTrait struct {
	Mode string `json:"mode"`
}

// ThermostatEcoTrait's Mode is one of "MANUAL_ECO", "OFF".
type ThermostatEcoTrait struct {
	Mode string `json:"mode"`
}

// ThermostatTemperatureSetpointTrait's fields are independently optional, not just the trait as a
// whole: a GET only returns the setpoint(s) that apply to the thermostat's current mode — HeatCelsius
// alone in HEAT mode, CoolCelsius alone in COOL mode, both together in HEATCOOL mode.
type ThermostatTemperatureSetpointTrait struct {
	HeatCelsius *float32 `json:"heatCelsius,omitempty"`
	CoolCelsius *float32 `json:"coolCelsius,omitempty"`
}

// ParseTraits decodes a device's raw SDM traits payload. It's shared by both the poller (fetching a
// device's full trait set) and the pubsub subscriber (receiving a partial update of only the traits
// that changed), so the two paths interpret trait JSON identically.
func ParseTraits(raw googleapi.RawMessage) (*Traits, error) {
	var traits Traits

	if err := json.Unmarshal(raw, &traits); err != nil {
		return nil, fmt.Errorf("failed to parse device traits: %w", err)
	}

	return &traits, nil
}

// traitField couples one of a NestThermostat's lww.Register fields with the logic to pull its
// candidate value out of a Traits payload, so ApplyTraits can declare its fields as a plain table (see
// its fields slice) instead of hand-writing the same present-check/apply block once per trait.
type traitField[T any] struct {
	register *lww.Register[T]
	// extract returns this field's candidate value from traits and whether the corresponding trait
	// namespace was present at all — a message that doesn't mention a trait must never be treated as
	// "that trait's new value is the zero value".
	extract func(*Traits) (T, bool)
}

func (f traitField[T]) apply(timestamp time.Time, traits *Traits) bool {
	value, present := f.extract(traits)

	if !present {
		return false
	}

	return f.register.Set(timestamp, value)
}

// applier lets a set of traitField[T] instances be applied uniformly in a loop, independent of the type of T.
type applier interface {
	apply(timestamp time.Time, traits *Traits) bool
}

// ApplyTraits copies whatever trait fields are present in traits onto a NestThermostat.
// Thermostat attributes maintain a last update timestamp and only accept values when the timestamp is newer than the
// last update time. Absent traits (nil) are ignored.  In addition, Name is skipped if t.NameLocked is set.
// When unlocked, Name prefers the composed FallbackName (structure + room + CustomName label — see
// fallbackName) over the bare CustomName, since FallbackName already incorporates CustomName when one's
// set. FallbackName is only ever present on poll-sourced Traits, though, so a pubsub-only Info update
// (which never carries room/structure assignment) still falls back to the bare label alone — a
// temporary simplification that self-corrects on the next poll.
func ApplyTraits(t *entities.NestThermostat, timestamp time.Time, traits *Traits) bool {
	fields := []applier{
		traitField[string]{&t.Name, func(traits *Traits) (string, bool) {
			if t.NameLocked {
				return "", false
			}

			if traits.FallbackName != nil && *traits.FallbackName != "" {
				return *traits.FallbackName, true
			}

			if traits.Info != nil && traits.Info.CustomName != "" {
				return traits.Info.CustomName, true
			}

			return "", false
		}},
		traitField[float32]{&t.Temperature, func(traits *Traits) (float32, bool) {
			if traits.Temperature == nil {
				return 0, false
			}
			return traits.Temperature.AmbientTemperatureCelsius, true
		}},
		traitField[float32]{&t.Humidity, func(traits *Traits) (float32, bool) {
			if traits.Humidity == nil {
				return 0, false
			}
			return traits.Humidity.AmbientHumidityPercent, true
		}},
		traitField[string]{&t.HvacStatus, func(traits *Traits) (string, bool) {
			if traits.ThermostatHvac == nil {
				return "", false
			}
			return traits.ThermostatHvac.Status, true
		}},
		traitField[string]{&t.Mode, func(traits *Traits) (string, bool) {
			if traits.ThermostatMode == nil {
				return "", false
			}
			return traits.ThermostatMode.Mode, true
		}},
		traitField[string]{&t.EcoMode, func(traits *Traits) (string, bool) {
			if traits.ThermostatEco == nil {
				return "", false
			}
			return traits.ThermostatEco.Mode, true
		}},
		traitField[float32]{&t.HeatSetpoint, func(traits *Traits) (float32, bool) {
			if traits.ThermostatTemperatureSetpoint == nil || traits.ThermostatTemperatureSetpoint.HeatCelsius == nil {
				return 0, false
			}
			return *traits.ThermostatTemperatureSetpoint.HeatCelsius, true
		}},
		traitField[float32]{&t.CoolSetpoint, func(traits *Traits) (float32, bool) {
			if traits.ThermostatTemperatureSetpoint == nil || traits.ThermostatTemperatureSetpoint.CoolCelsius == nil {
				return 0, false
			}
			return *traits.ThermostatTemperatureSetpoint.CoolCelsius, true
		}},
	}

	applied := false

	for _, field := range fields {
		if field.apply(timestamp, traits) {
			applied = true
		}
	}

	return applied
}

type Client struct {
	httpClient *http.Client
	service    *smartdevicemanagement.Service
	options    ClientOptions
}

func NewClient(ctx context.Context, options ClientOptions) (*Client, error) {
	oauthConfig := &oauth2.Config{
		ClientID:     options.ClientId,
		ClientSecret: options.ClientSecret,
		// The refresh-token-to-access-token exchange always goes through Google's standard token
		// endpoint, regardless of the Partner Connections Manager hop the interactive consent flow
		// needs (see plugin.go's prepareOAuthAttempt) — PCM only affects the authorization step, not
		// token refresh. Setting this is what lets oauth2.Config.Client's returned http.Client
		// automatically mint an access token from options.RefreshToken on first use.
		Endpoint: google.Endpoint,
	}

	token := &oauth2.Token{
		RefreshToken: options.RefreshToken,
	}

	httpClient := oauthConfig.Client(ctx, token)

	service, err := smartdevicemanagement.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create SDM service: %w", err)
	}

	return &Client{
		httpClient: httpClient,
		service:    service,
		options:    options,
	}, nil
}

func (c *Client) FetchUserInfo(ctx context.Context) (UserInfo, error) {
	resp, err := c.httpClient.Get("https://www.googleapis.com/oauth2/v3/userinfo")

	if err != nil {
		return UserInfo{}, err
	}

	defer func() {
		err := resp.Body.Close()
		fmt.Println("Error closing response body: ", err)
	}()

	var info UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return info, err
	}

	return info, nil
}

// DeviceTraits pairs a device id with its parsed traits. FetchDevices hands these back raw (rather than
// pre-built entities.NestThermostat values) because merging them onto a thermostat requires comparing
// against that thermostat's existing per-trait timestamps — state only the caller (which owns the
// persisted thermostat, not this client) has access to. See ApplyTraits.
type DeviceTraits struct {
	Id     string
	Traits *Traits
}

func (c *Client) FetchDevices(ctx context.Context) ([]DeviceTraits, error) {
	parent := fmt.Sprintf("enterprises/%s", c.options.SdmProjectID)
	resp, err := c.service.Enterprises.Devices.List(parent).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}

	var result []DeviceTraits

	for _, dev := range resp.Devices {
		if dev.Type != "sdm.devices.types.THERMOSTAT" {
			continue
		}

		traits, err := ParseTraits(dev.Traits)

		if err != nil {
			fmt.Printf("Failed to parse traits for device %s: %v\n", dev.Name, err)
			traits = &Traits{}
		}

		var label string

		if traits.Info != nil {
			label = traits.Info.CustomName
		}

		if fallbackName := fallbackName(dev.ParentRelations, label); fallbackName != "" {
			traits.FallbackName = &fallbackName
		}

		result = append(result, DeviceTraits{Id: dev.Name, Traits: traits})
	}

	return result, nil
}

// fallbackName composes a display name for a device from its structure/room assignment (its
// ParentRelations — this is what the "Where" setting in the Nest app actually reflects) and label (its
// Info.CustomName). The structure and room names, if present, form the base name; label, if present, is
// appended in parentheses as a disambiguator — mirroring the Nest app's own documented convention that
// Label "appear[s] next to the location name in parentheses", used to tell apart multiple devices
// assigned to the same room, rather than replacing the location name outright. Including the structure
// name (not just the room) matters here specifically because, unlike the Nest app — where you're always
// looking at one structure at a time — this plugin may show devices from multiple structures side by
// side with nothing else to disambiguate them.
func fallbackName(
	parentRelations []*smartdevicemanagement.GoogleHomeEnterpriseSdmV1ParentRelation, label string) string {
	var structureName, roomName string

	for _, relation := range parentRelations {
		if relation == nil || relation.DisplayName == "" {
			continue
		}

		if strings.Contains(relation.Parent, "/rooms/") {
			roomName = relation.DisplayName
		} else if strings.Contains(relation.Parent, "/structures/") {
			structureName = relation.DisplayName
		}
	}

	name := strings.TrimSpace(structureName + " " + roomName)

	if label == "" {
		return name
	}

	if name == "" {
		return label
	}

	return fmt.Sprintf("%s (%s)", name, label)
}
