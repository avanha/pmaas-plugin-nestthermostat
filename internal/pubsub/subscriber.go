package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

type SubscriberOptions struct {
	ProjectId           string
	SubscriptionId      string
	ServiceAccountCreds []byte
}

type MessagePayload struct {
	EventId        string    `json:"eventId"`
	Timestamp      time.Time `json:"timestamp"`
	ResourceUpdate struct {
		Name string `json:"name"`
		//Traits map[string]interface{} `json:"traits"`
		Traits googleapi.RawMessage `json:"traits,omitempty"`
	} `json:"resourceUpdate"`
}

type Callback func(deviceId string, timestamp time.Time, traits googleapi.RawMessage)

// Subscriber forwards every device-update message it receives to deviceUpdateHandler. There's no
// device allowlist here: which devices this subscription can even receive messages for is already
// decided by the user during the SDM/PCM consent flow, and the plugin itself discards updates for any
// device it doesn't otherwise know about (see plugin.handleDeviceUpdate).
type Subscriber struct {
	options             SubscriberOptions
	subscriptionID      string
	deviceUpdateHandler Callback
	errorHandlerFn      func(err error)
}

func NewSubscriber(
	options SubscriberOptions, deviceUpdateHandler Callback, errorHandlerFn func(err error)) *Subscriber {
	return &Subscriber{
		options:             options,
		deviceUpdateHandler: deviceUpdateHandler,
		errorHandlerFn:      errorHandlerFn,
	}
}

func (s *Subscriber) Run(ctx context.Context) {
	client, err := pubsub.NewClient(
		ctx,
		s.options.ProjectId,
		option.WithAuthCredentialsJSON(option.ServiceAccount, s.options.ServiceAccountCreds))

	if err != nil {
		s.errorHandlerFn(fmt.Errorf("pubsub: failed to create pubsub client: %w", err))
		return
	}

	defer func() {
		err := client.Close()
		if err != nil {
			s.errorHandlerFn(fmt.Errorf("pubsub: failed to close pubsub client: %w", err))
		}
	}()

	subscription := client.Subscriber(s.options.SubscriptionId)

	// Blocks until the context is canceled.
	err = subscription.Receive(ctx, s.onMessageReceived)

	if err != nil {
		s.errorHandlerFn(fmt.Errorf("pubsub: receive error: %w", err))
	}

	fmt.Printf("PubSub subscriber stopped\n")
}

func (s *Subscriber) onMessageReceived(ctx context.Context, msg *pubsub.Message) {
	defer msg.Ack()

	var payload MessagePayload

	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		s.errorHandlerFn(fmt.Errorf("pubsub: error unmarshalling message: %w", err))
		return
	}

	if payload.ResourceUpdate.Name == "" || payload.ResourceUpdate.Traits == nil {
		return
	}

	s.deviceUpdateHandler(payload.ResourceUpdate.Name, payload.Timestamp.Local(), payload.ResourceUpdate.Traits)
}
