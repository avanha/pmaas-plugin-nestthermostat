package data

import "time"

type PluginStatus struct {
	GoogleUser string
	// GoogleUserPicture is a URL to the logged-in user's Google account profile picture, or empty if
	// they don't have one or GoogleUser itself is empty.
	GoogleUserPicture string

	// HasRefreshToken and RefreshTokenObtainedTime reflect whether an OAuth refresh token is currently
	// configured and, if so, when it was last obtained — RefreshTokenObtainedTime is meaningless while
	// HasRefreshToken is false.
	HasRefreshToken          bool
	RefreshTokenObtainedTime time.Time

	// RefreshTokenState is how far the refresh token can be relied on: "none", "valid", "expiring"
	// (expires within a day), "expired", or "rejected" (Google refused it). "rejected" is a fact,
	// whereas "expired" may rest on an estimate (see RefreshTokenExpirationKnown).
	RefreshTokenState string

	// RefreshTokenExpiration is when the refresh token expires. RefreshTokenExpirationKnown says whether
	// Google reported that time (true) or it's an estimate (false); the status page labels estimates as
	// such. RefreshTokenNoExpiry is true when there's no expiration to show (Google reported none and the
	// estimate is disabled, impossible, or was disproven), in which case RefreshTokenExpiration is zero.
	// RefreshTokenEstimateDisproven is true if the estimated expiration passed and the token kept
	// working anyway.
	RefreshTokenExpiration        time.Time
	RefreshTokenExpirationKnown   bool
	RefreshTokenNoExpiry          bool
	RefreshTokenEstimateDisproven bool

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
