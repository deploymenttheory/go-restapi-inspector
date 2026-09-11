package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

const FingerprintAlgorithm = "inspector-canonical-v1"

// Fingerprint uses sorted object keys and exact rational numbers. Array order
// and descriptions are significant; JSON whitespace and numeric spelling are not.
func Fingerprint(v any) string {
	h := sha256.New()
	var walk func(any)
	walk = func(v any) {
		if n, ok := model.Number(v); ok {
			fmt.Fprintf(h, "n%s;", n.RatString())
			return
		}
		switch x := v.(type) {
		case map[string]any:
			h.Write([]byte("{"))
			for _, k := range Keys(x) {
				walk(k)
				walk(x[k])
			}
			h.Write([]byte("}"))
		case []any:
			h.Write([]byte("["))
			for _, child := range x {
				walk(child)
			}
			h.Write([]byte("]"))
		default:
			b, _ := json.Marshal(v)
			h.Write(b)
			h.Write([]byte(";"))
		}
	}
	// Normalize structs and typed collections without converting numbers to float.
	walk(model.Clone[any](v))
	return hex.EncodeToString(h.Sum(nil))
}

func (d *Document) Identity(release string) model.SpecIdentity {
	return model.SpecIdentity{Algorithm: FingerprintAlgorithm, SHA256: d.Hash, Canonical: Fingerprint(d.Raw), Dialect: Str(d.Raw["openapi"]), Version: Str(Map(d.Raw["info"])["version"]), Release: release}
}

type OperationFingerprint struct {
	Full          string `json:"full"`
	Request       string `json:"request"`
	Response      string `json:"response"`
	Documentation string `json:"documentation"`
	Security      string `json:"security"`
}

// ReferenceClosure retains cycles as pointers and follows each target once.
func (d *Document) ReferenceClosure(value any) (map[string]any, error) {
	found := map[string]any{}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				if _, seen := found[ref]; !seen {
					if !strings.HasPrefix(ref, "#/") && ref != "#" {
						return fmt.Errorf("unbundled reference %q", ref)
					}
					target, ok := model.Get(d.Raw, strings.TrimPrefix(ref, "#"))
					if !ok {
						return fmt.Errorf("unresolved reference %q", ref)
					}
					found[ref] = target
					if err := walk(target); err != nil {
						return err
					}
				}
			}
			for _, key := range Keys(x) {
				if referenceMap(key) && Map(x[key]) != nil {
					for _, name := range Keys(Map(x[key])) {
						if err := walk(Map(x[key])[name]); err != nil {
							return err
						}
					}
					continue
				}
				if literalValue(key) || key == "examples" && !isExampleMap(x[key]) {
					continue
				}
				if err := walk(x[key]); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return found, walk(value)
}

func literalValue(key string) bool {
	return key == "example" || key == "default" || key == "const" || key == "enum" || key == "value"
}

// These objects contain user-defined names, not OpenAPI keyword positions.
func referenceMap(key string) bool {
	switch key {
	case "properties", "patternProperties", "$defs", "definitions", "schemas", "responses", "headers", "links", "examples", "securitySchemes", "requestBodies", "parameters", "callbacks", "pathItems":
		return true
	}
	return false
}

func (d *Document) operationDigest(v any) (string, error) {
	refs, err := d.ReferenceClosure(v)
	if err != nil {
		return "", err
	}
	return Fingerprint(map[string]any{"value": v, "references": refs}), nil
}

func (d *Document) Fingerprints() (map[string]OperationFingerprint, error) {
	out := map[string]OperationFingerprint{}
	for _, op := range d.Operations {
		item, err := d.Resolve(Map(Map(d.Raw["paths"])[op.Path]))
		if err != nil {
			return nil, err
		}
		effective := model.Clone(op.Raw)
		params := map[string]any{}
		for _, container := range []map[string]any{item, op.Raw} {
			for _, raw := range Slice(container["parameters"]) {
				p, err := d.Resolve(Map(raw))
				if err != nil {
					return nil, err
				}
				params[Str(p["in"])+":"+Str(p["name"])] = p
			}
		}
		effective["parameters"] = params
		if _, ok := effective["security"]; !ok {
			effective["security"] = d.Raw["security"]
		}
		if _, ok := effective["servers"]; !ok {
			effective["servers"] = item["servers"]
			if effective["servers"] == nil {
				effective["servers"] = d.Raw["servers"]
			}
		}
		// Adding another representation does not change this representation.
		if op.MediaType != "" {
			body, err := d.Resolve(Map(effective["requestBody"]))
			if err != nil {
				return nil, err
			}
			body = model.Clone(body)
			body["content"] = map[string]any{op.MediaType: Map(body["content"])[op.MediaType]}
			effective["requestBody"] = body
		}
		security := map[string]any{"requirements": effective["security"]}
		schemes := Map(Map(d.Raw["components"])["securitySchemes"])
		relevantSchemes := map[string]any{}
		for _, group := range Slice(effective["security"]) {
			for name := range Map(group) {
				relevantSchemes[name] = schemes[name]
			}
		}
		security["securitySchemes"] = relevantSchemes
		security["x-required-privileges"] = effective["x-required-privileges"]
		metadata := map[string]any{"dialect": d.Raw["openapi"], "schemaDialect": d.Raw["jsonSchemaDialect"], "apiDescription": Map(d.Raw["info"])["description"]}
		pathMetadata := map[string]any{}
		for key, v := range item {
			if key == "summary" || key == "description" || strings.HasPrefix(key, "x-") {
				pathMetadata[key] = v
			}
		}
		metadata["pathMetadata"] = pathMetadata
		var tags []any
		for _, tag := range Slice(d.Raw["tags"]) {
			for _, name := range Slice(effective["tags"]) {
				if Map(tag)["name"] == name {
					tags = append(tags, tag)
				}
			}
		}
		metadata["tagDefinitions"] = tags
		operationMetadata := map[string]any{}
		for key, v := range effective {
			if key != "parameters" && key != "requestBody" && key != "responses" && key != "security" && key != "x-required-privileges" {
				operationMetadata[key] = v
			}
		}
		metadata["operationMetadata"] = operationMetadata
		f := OperationFingerprint{}
		for _, facet := range []struct {
			value any
			dest  *string
		}{
			{map[string]any{"parameters": params, "body": effective["requestBody"]}, &f.Request},
			{effective["responses"], &f.Response}, {metadata, &f.Documentation}, {security, &f.Security},
			{map[string]any{"operation": effective, "metadata": metadata, "security": security}, &f.Full},
		} {
			*facet.dest, err = d.operationDigest(facet.value)
			if err != nil {
				return nil, fmt.Errorf("fingerprint %s: %w", op.Key, err)
			}
		}
		out[op.Identity()] = f
	}
	return out, nil
}
