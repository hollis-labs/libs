package eventbus

import (
	"os"
	"strings"
)

// Environment variables read by [FromEnv].
const (
	// EnvURL is the hub ingest endpoint. Unset means no hub: FromEnv
	// returns a [Noop].
	EnvURL = "HOLLIS_EVENTS_URL"
	// EnvSpoolDir enables the disk spool.
	EnvSpoolDir = "HOLLIS_EVENTS_SPOOL_DIR"
	// tokenFilePrefix plus the upper-cased app name names the variable that
	// holds the path of the app's token file.
	tokenFilePrefix = "HOLLIS_AUTH_" + "TOKEN_FILE_"
)

// TokenFileEnv returns the name of the variable holding the token file path
// for app, for example "HOLLIS_AUTH_TOKEN_FILE_CERBERUS" for "cerberus".
func TokenFileEnv(app string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(app) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return tokenFilePrefix + b.String()
}

// FromEnv builds a Publisher for app from the environment. With no
// HOLLIS_EVENTS_URL it returns a [Noop], so an app runs unchanged without a
// hub. base carries any other settings; its URL, TokenFile and SpoolDir are
// overwritten by the environment when set.
func FromEnv(app string, base Config) (Publisher, error) {
	url := strings.TrimSpace(os.Getenv(EnvURL))
	if url == "" {
		return Noop{}, nil
	}
	base.URL = url
	if f := os.Getenv(TokenFileEnv(app)); f != "" {
		base.TokenFile = f
	}
	if d := os.Getenv(EnvSpoolDir); d != "" {
		base.SpoolDir = d
	}
	return New(base)
}
