package exporter

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
)

func yamlValue(v any) any {
	switch x := v.(type) {
	case json.Number:
		tag := "!!float"
		if _, err := x.Int64(); err == nil {
			tag = "!!int"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: x.String()}
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			out[k] = yamlValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = yamlValue(v)
		}
		return out
	default:
		return v
	}
}
