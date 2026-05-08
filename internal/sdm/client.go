package sdm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/avanha/pmaas-plugin-nestthermostat/entities"
	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	smartdevicemanagement "google.golang.org/api/smartdevicemanagement/v1"
)

type ClientOptions struct {
	ClientId     string
	ClientSecret string
	RefreshToken string
	SdmProjectID string
}

type Traits struct {
	Info Info `json:"sdm.devices.traits.Info"`
}

type Info struct {
	CustomName string `json:"customName"`
}

type Client struct {
	service *smartdevicemanagement.Service
	options ClientOptions
}

func NewClient(ctx context.Context, options ClientOptions) (*Client, error) {
	oauthConfig := &oauth2.Config{
		ClientID:     options.ClientId,
		ClientSecret: options.ClientSecret,
		//Endpoint:     google.Endpoint,
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
		service: service,
		options: options,
	}, nil
}

func (c *Client) FetchDevices(ctx context.Context) ([]entities.NestThermostat, error) {
	parent := fmt.Sprintf("enterprises/%s", c.options.SdmProjectID)
	resp, err := c.service.Enterprises.Devices.List(parent).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list devices: %w", err)
	}

	var thermostats []entities.NestThermostat

	for _, dev := range resp.Devices {
		if dev.Type != "sdm.devices.types.THERMOSTAT" {
			continue
		}

		t := entities.NestThermostat{
			Id:   dev.Name,
			Name: "Nest Thermostat", // Default name, override from traits
		}

		var traits Traits
		err := json.Unmarshal(dev.Traits, &traits)

		if err == nil {
			if traits.Info.CustomName != "" {
				t.Name = traits.Info.CustomName
			}
		}

		c.UpdateFromTraits(&t, &traits)
		thermostats = append(thermostats, t)
	}

	return thermostats, nil
}

func (c *Client) UpdateFromTraits(t *entities.NestThermostat, traits *Traits) {

}
