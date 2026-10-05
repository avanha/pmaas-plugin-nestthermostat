package config

import "time"

type PersistentConfigV1 struct {
	AccessToken               string
	AccessTokenExpirationTime time.Time
	AccessTokenScopes         []string
	AccessTokenType           string
	RefreshToken              string
	// RefreshTokenObtainedTime is when RefreshToken was last obtained via the OAuth/PCM consent flow, so
	// the status page can show the user how long it's been since they last authorized this plugin. Zero
	// for a config saved before this field existed, or if RefreshToken is empty.
	RefreshTokenObtainedTime time.Time
	// RefreshTokenExpirationTime is when Google said RefreshToken expires, if it said (it does for apps
	// whose consent screen is in "Testing" status, where refresh tokens last 7 days). Only meaningful if
	// RefreshTokenExpirationKnown; when it isn't known the plugin estimates one from
	// RefreshTokenObtainedTime instead, which isn't stored, since it depends on a setting that may change.
	// Both are zero for a config saved before these fields existed.
	RefreshTokenExpirationTime  time.Time
	RefreshTokenExpirationKnown bool
}
