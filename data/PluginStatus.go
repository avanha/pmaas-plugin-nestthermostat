package data

import "time"

type PluginStatus struct {
	GoogleUser string

	// HasRefreshToken and RefreshTokenObtainedTime reflect whether an OAuth refresh token is currently
	// configured and, if so, when it was last obtained — RefreshTokenObtainedTime is meaningless while
	// HasRefreshToken is false.
	HasRefreshToken          bool
	RefreshTokenObtainedTime time.Time

	// LastPollTime and LastPubSubMessageTime are the last time each of this plugin's two independent
	// data-ingestion paths actually delivered device data — not merely "was attempted" (a failed
	// attempt instead updates LastErrorTime below, without moving these forward).
	LastPollTime          time.Time
	LastPubSubMessageTime time.Time

	// LastErrorMessage/LastErrorTime record the most recent error from any of this plugin's error
	// sources (OAuth token exchange, polling, pubsub, and SDM trait processing) that overwrote a
	// previous one, if any.
	LastErrorMessage string
	LastErrorTime    time.Time
}
