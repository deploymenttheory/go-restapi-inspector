package exporter

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

// RelaxValueSchema weakens only primitive assertions directly falsified by a
// confirmed accepted value. Conditional and structural assertions stay intact.
func RelaxValueSchema(schema map[string]any, value any) bool {
	changed := false
	for _, branch := range spec.Slice(schema["allOf"]) {
		changed = RelaxValueSchema(spec.Map(branch), value) || changed
	}
	var types []string
	switch declared := schema["type"].(type) {
	case string:
		types = []string{declared}
	case []any:
		for _, typ := range declared {
			types = append(types, spec.Str(typ))
		}
	}
	if len(types) > 0 {
		accepted := false
		for _, typ := range types {
			accepted = accepted || model.TypeMatches(value, typ)
		}
		if !accepted && model.Type(value) != "unknown" {
			types = append(types, model.Type(value))
			if slices.Contains(types, "number") {
				types = slices.DeleteFunc(types, func(t string) bool { return t == "integer" })
			}
			slices.Sort(types)
			if len(types) == 1 {
				schema["type"] = types[0]
			} else {
				values := make([]any, len(types))
				for i, typ := range types {
					values[i] = typ
				}
				schema["type"] = values
			}
			if schema["format"] == "int32" || schema["format"] == "int64" {
				delete(schema, "format")
			}
			changed = true
		}
	}
	for _, keyword := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength", "minItems", "maxItems"} {
		limit, exists := schema[keyword]
		if !exists {
			continue
		}
		input := model.Input{HasBody: true, Body: value}
		if (model.Predicate{Op: keyword, Field: "body:", Value: limit}).Eval(input) {
			continue
		}
		var replacement any
		switch {
		case strings.HasSuffix(keyword, "Length"):
			s, ok := value.(string)
			if !ok {
				continue
			}
			replacement = utf8.RuneCountInString(s)
		case strings.HasSuffix(keyword, "Items"):
			a, ok := value.([]any)
			if !ok {
				continue
			}
			replacement = len(a)
		default:
			replacement = value
		}
		key := keyword
		if keyword == "exclusiveMinimum" {
			key = "minimum"
			delete(schema, keyword)
		}
		if keyword == "exclusiveMaximum" {
			key = "maximum"
			delete(schema, keyword)
		}
		schema[key] = replacement
		changed = true
	}
	return changed
}

func repairField(schema map[string]any, pointer string, value any) bool {
	if pointer == "" {
		return RelaxValueSchema(schema, value)
	}
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var walk func(map[string]any, []string) bool
	walk = func(s map[string]any, remaining []string) bool {
		if len(remaining) == 0 {
			return RelaxValueSchema(s, value)
		}
		changed := false
		for _, branch := range spec.Slice(s["allOf"]) {
			changed = walk(spec.Map(branch), remaining) || changed
		}
		name := model.Unescape(remaining[0])
		if name == "0" {
			return walk(spec.Map(s["items"]), remaining[1:]) || changed
		}
		return walk(spec.Map(spec.Map(s["properties"])[name]), remaining[1:]) || changed
	}
	return walk(schema, parts)
}
