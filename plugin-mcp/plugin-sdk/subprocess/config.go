package subprocess

import (
	"fmt"
	"strconv"
)

// ConfigReader exposes resolved plugin configuration to plugin code.
// The host resolves config via its precedence rules (plugin_settings DB ->
// env var -> plugin.yaml default -> zero value) before passing the map
// through InitParams.Config. ConfigReader is the SDK-level view of that
// resolved map.
type ConfigReader interface {
	// String returns the value for key, or the empty string if missing.
	String(key string) string

	// Bool returns the value for key parsed as a boolean
	// (strconv.ParseBool rules). Returns false if missing or unparseable.
	Bool(key string) bool

	// Int returns the value for key parsed as a signed integer. Returns
	// 0 if missing or unparseable.
	Int(key string) int

	// Secret returns the value for key and records it with the logger's
	// secret tracker so that subsequent log calls containing the value
	// as a field write REDACTED rather than the cleartext. Use this for
	// API keys, tokens, and other sensitive strings.
	Secret(key string) string

	// Required returns the value for key and an error if the key is
	// missing or blank.
	Required(key string) (string, error)

	// Has reports whether the key is present in the resolved config.
	Has(key string) bool
}

// mapConfigReader is the default ConfigReader backed by a plain map.
type mapConfigReader struct {
	values  map[string]string
	secrets *secretTracker
}

// newConfigReader constructs a ConfigReader over the given resolved
// config map. Passing nil values is safe; the reader behaves as if all
// keys are missing.
func newConfigReader(values map[string]string, secrets *secretTracker) ConfigReader {
	if values == nil {
		values = map[string]string{}
	}
	return &mapConfigReader{values: values, secrets: secrets}
}

func (c *mapConfigReader) String(key string) string { return c.values[key] }

func (c *mapConfigReader) Bool(key string) bool {
	b, _ := strconv.ParseBool(c.values[key])
	return b
}

func (c *mapConfigReader) Int(key string) int {
	i, _ := strconv.Atoi(c.values[key])
	return i
}

func (c *mapConfigReader) Secret(key string) string {
	v := c.values[key]
	if c.secrets != nil {
		c.secrets.add(v)
	}
	return v
}

func (c *mapConfigReader) Required(key string) (string, error) {
	v, ok := c.values[key]
	if !ok || v == "" {
		return "", fmt.Errorf("required config key %q is missing or empty", key)
	}
	return v, nil
}

func (c *mapConfigReader) Has(key string) bool {
	_, ok := c.values[key]
	return ok
}
