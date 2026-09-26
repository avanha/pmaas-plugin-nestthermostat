package config

import "time"

type PersistentConfigV1 struct {
	AccessToken               string
	AccessTokenExpirationTime time.Time
	AccessTokenScopes         []string
	AccessTokenType           string
	RefreshToken              string
}
