# Implementation Plan: Nest Thermostat Plugin (pmaas-plugin-nestthermostat)

## Objective
Implement a PMAAS plugin that integrates with the Google Smart Device Management (SDM) API and Google Cloud Pub/Sub to monitor and track Nest thermostats.

## 1. Requirements & Integration Method
- **Bootstrap/Discovery**: Use the **Google SDM API** REST endpoints at startup to discover authorized devices and fetch initial states.
- **Real-time Updates**: Use **Google Cloud Pub/Sub** to receive push notifications for state changes (traits).
- **Go Libraries**:
  - SDM API: `google.golang.org/api/smartdevicemanagement/v1`
  - Pub/Sub: `cloud.google.com/go/pubsub`
  - Auth: `golang.org/x/oauth2`

## 2. Configuration (`config/config.go`)
Define `PluginConfig` to include:
- `GcpProjectID`: GCP Project ID for Pub/Sub.
- `PubSubSubscriptionID`: The subscription name for SDM events.
- `SdmProjectID`: The Nest Device Access Project ID.
- `ClientId`, `ClientSecret`, `RefreshToken`: OAuth2 credentials for API access.
- `ThermostatIDs`: List of specific device IDs to track (optional, if filtering is desired).

## 3. Entity Definition (`entities/thermostat.go`)
Create a `NestThermostat` struct implementing `tracking.Trackable`:
- **State Fields**:
  - `Temperature` (float64) - Trait: `sdm.devices.traits.Temperature`
  - `Humidity` (float64) - Trait: `sdm.devices.traits.Humidity`
  - `HvacStatus` (string) - Trait: `sdm.devices.traits.ThermostatHvac`
  - `EcoMode` (string) - Trait: `sdm.devices.traits.ThermostatEco`
- **Logic**: Implement `Data()` to return `tracking.DataSample` containing the current values.

## 4. Internal Components
### SDM Client (`internal/sdm/client.go`)
- Logic to initialize the OAuth2 authorized client.
- `FetchDevices()`: Returns a list of thermostats and their current trait states.

### Pub/Sub Subscriber (`internal/pubsub/subscriber.go`)
- Background worker using `subscription.Receive`.
- Message Parser: Decode SDM event JSON to identify the `deviceId` and updated traits.
- Callback mechanism: Notify the plugin to update specific entity instances.

## 5. Plugin Lifecycle (`plugin.go`)
- **`Init`**: Store the `IPMAASContainer`.
- **`Start`**:
  1. Authenticate and initialize the SDM client.
  2. Call `FetchDevices` to bootstrap initial state.
  3. Register a `NestThermostat` entity in the container for each discovered device.
  4. Start the Pub/Sub subscriber goroutine.
- **`Stop`**:
  1. Cancel the Pub/Sub receiver context.
  2. Clean up entities and handles.

## 6. Implementation Steps for Junie
1. **Dependencies**: Update `go.mod` with `google-api-go-client`, `cloud.google.com/go/pubsub`, and `oauth2`.
2. **Scaffold**: Create `config/`, `entities/`, and `internal/` subdirectories.
3. **Config**: Implement `config.go` with SDM and Pub/Sub fields.
4. **Entity**: Implement `entities/thermostat.go` ensuring it satisfies `tracking.Trackable`.
5. **Logic**:
   - Implement SDM discovery logic.
   - Implement the Pub/Sub listener loop.
6. **Plugin**: Wire everything into `plugin.go`, following the structure of `pmaas-plugin-dblog` but using the push-based model.
7. **Environment Sync**: Ensure the entity type and traits are compatible with `pmaas-plugin-environment` for rendering.
