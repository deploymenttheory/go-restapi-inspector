package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/go-viper/mapstructure/v2"
	"github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

// Viper treats configuration keys case-insensitively. API operation IDs,
// JSON pointers and user-defined profile names are case-sensitive data, so
// these maps are decoded without Viper's recursive key normalization.
func dynamicConfig(c, base *config.Config, file string) error {
	keys := []string{"hints", "domains", "rules", "auth-profiles", "security"}
	raw := map[string]any{}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		switch strings.ToLower(filepath.Ext(file)) {
		case ".json":
			err = journal.Decode(b, &raw)
		case ".toml":
			err = toml.Unmarshal(b, &raw)
		default:
			err = yaml.Unmarshal(b, &raw)
		}
		if err != nil {
			return fmt.Errorf("configuration: %w", err)
		}
	}
	if base != nil {
		c.Hints = base.Hints
		c.Domains = base.Domains
		c.Rules = base.Rules
		c.AuthProfiles = base.AuthProfiles
		c.Security = base.Security
	} else {
		c.Hints = nil
		c.Domains = nil
		c.Rules = nil
		c.AuthProfiles = nil
		c.Security = nil
	}
	selected := map[string]any{}
	for _, key := range keys {
		if v, ok := raw[key]; ok {
			selected[key] = v
		}
		env := "RESTAPI_INSPECTOR_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
		if value, ok := os.LookupEnv(env); ok && value != "" {
			var data any
			if err := journal.Decode([]byte(value), &data); err != nil {
				return fmt.Errorf("%s must be JSON: %w", env, err)
			}
			selected[key] = data
		}
	}
	numericDuration := func(_ reflect.Type, to reflect.Type, value any) (any, error) {
		if number, ok := value.(json.Number); ok && to == reflect.TypeFor[time.Duration]() {
			return number.Int64()
		}
		return value, nil
	}
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{Result: c, TagName: "mapstructure", WeaklyTypedInput: true, DecodeHook: mapstructure.ComposeDecodeHookFunc(numericDuration, mapstructure.StringToTimeDurationHookFunc())})
	if err != nil {
		return err
	}
	if err = decoder.Decode(selected); err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	return nil
}
