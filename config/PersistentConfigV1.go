package config

import "time"

type PersistentConfigV1 struct {
	AccessToken               string
	AccessTokenExpirationTime time.Time
	AccessTokenScopes         []string
	AccessTokenType           string
	RefreshToken              string
	// RefreshTokenObtainedTime is when RefreshToken was last obtained via the OAuth/PCM consent flow —
	// purely informational (nothing here expires or rotates it), so the status page can show the user
	// how long it's been since they last authorized this plugin. Zero for a config saved before this
	// field existed, or if RefreshToken is empty.
	RefreshTokenObtainedTime time.Time
}
