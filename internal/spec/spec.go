// Package spec preserves the source document while presenting operation-local
// views for experiments. Declared constraints are hypotheses, not probe filters.
package spec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

type Document struct {
	Raw        map[string]any    `json:"raw"`
	Operations []model.Operation `json:"operations"`
	Hash       string            `json:"hash"`
	Warnings   []string          `json:"warnings,omitempty"`
}

func Load(ctx context.Context, source string) (*Document, error) {
	b, base, e := read(ctx, source)
	if e != nil {
		return nil, e
	}
	var raw map[string]any
	if e = decodeDocument(b, &raw); e != nil {
		return nil, fmt.Errorf("parse OpenAPI: %w", e)
	}
	version, _ := raw["openapi"].(string)
	if !strings.HasPrefix(version, "3.0.") && !strings.HasPrefix(version, "3.1.") {
		return nil, fmt.Errorf("supported input versions are OpenAPI 3.0 and 3.1, got %q", version)
	}
	raw = model.Clone(raw)
	if e = bundle(ctx, raw, base); e != nil {
		return nil, e
	}
	h := sha256.Sum256(b)
	d, e := FromMap(raw)
	if e != nil {
		return nil, e
	}
	d.Hash = hex.EncodeToString(h[:])
	return d, nil
}

func read(ctx context.Context, source string) ([]byte, string, error) {
	u, e := url.Parse(source)
	if e != nil {
		return nil, "", fmt.Errorf("spec location: %w", e)
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if e != nil {
			return nil, "", e
		}
		c := http.Client{Timeout: 30 * time.Second}
		r, e := c.Do(req)
		if e != nil {
			return nil, "", fmt.Errorf("read specification: %w", e)
		}
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("specification returned HTTP %d", r.StatusCode)
		}
		b, e := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
		if len(b) > 16<<20 {
			return nil, "", fmt.Errorf("specification exceeds 16 MiB")
		}
		return b, source, e
	}
	if u.Scheme == "file" {
		source = u.Path
	} else if u.Scheme != "" {
		return nil, "", fmt.Errorf("unsupported spec URI scheme %q", u.Scheme)
	}
	abs, e := filepath.Abs(source)
	if e != nil {
		return nil, "", e
	}
	b, e := os.ReadFile(abs)
	if e != nil {
		return nil, "", fmt.Errorf("read specification: %w", e)
	}
	return b, (&url.URL{Scheme: "file", Path: abs}).String(), nil
}

// bundle fetches only referenced documents, never scans neighbouring directories.
// External documents remain intact under an extension, preserving reference bases
// and cycles without recursively expanding schemas.
func bundle(ctx context.Context, root map[string]any, base string) error {
	docs := map[string]string{base: ""}
	external := map[string]any{}
	var walk func(any, string, string) error
	walk = func(v any, current, prefix string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				u, e := url.Parse(ref)
				if e != nil {
					return e
				}
				b, e := url.Parse(current)
				if e != nil {
					return e
				}
				dest := b.ResolveReference(u)
				if (b.Scheme == "https" || b.Scheme == "http") && dest.Scheme != "https" && dest.Scheme != "http" {
					return fmt.Errorf("remote specifications cannot reference local files: %s", ref)
				}
				frag := dest.Fragment
				dest.Fragment = ""
				address := dest.String()
				target, exists := docs[address]
				if !exists {
					if len(docs) >= 256 {
						return fmt.Errorf("more than 256 referenced documents")
					}
					target = "/x-inspector-documents/" + model.ID(address)
					docs[address] = target
					data, _, e := read(ctx, address)
					if e != nil {
						return fmt.Errorf("resolve %s: %w", ref, e)
					}
					var doc any
					if e = decodeDocument(data, &doc); e != nil {
						return e
					}
					doc = model.Clone(doc)
					external[model.ID(address)] = doc
					if e = walk(doc, address, target); e != nil {
						return e
					}
				}
				if frag != "" && !strings.HasPrefix(frag, "/") {
					return fmt.Errorf("named reference anchors are not supported for bundling: %s", ref)
				}
				x["$ref"] = "#" + target + frag
			}
			for _, k := range Keys(x) {
				if referenceMap(k) && Map(x[k]) != nil {
					for _, name := range Keys(Map(x[k])) {
						if e := walk(Map(x[k])[name], current, prefix); e != nil {
							return e
						}
					}
					continue
				}
				if k == "$ref" || literalValue(k) || k == "examples" && !isExampleMap(x[k]) {
					continue
				}
				if e := walk(x[k], current, prefix); e != nil {
					return e
				}
			}
		case []any:
			for _, item := range x {
				if e := walk(item, current, prefix); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(root, base, ""); e != nil {
		return e
	}
	if len(external) > 0 {
		root["x-inspector-documents"] = external
	}
	return nil
}

func isExampleMap(v any) bool { _, ok := v.(map[string]any); return ok }

func decodeDocument(b []byte, out any) error {
	if json.Valid(b) {
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		return decoder.Decode(out)
	}
	return yaml.Unmarshal(b, out)
}

func FromMap(raw map[string]any) (*Document, error) {
	d := &Document{Raw: model.Clone(raw)}
	b, e := json.Marshal(raw)
	if e != nil {
		return nil, e
	}
	doc, e := libopenapi.NewDocumentWithConfiguration(b, &datamodel.DocumentConfiguration{SkipCircularReferenceCheck: true})
	if e != nil {
		return nil, fmt.Errorf("OpenAPI document: %w", e)
	}
	if _, e = doc.BuildV3Model(); e != nil {
		d.Warnings = append(d.Warnings, "OpenAPI model diagnostics: "+e.Error())
	}
	paths := Map(raw["paths"])
	if paths == nil {
		return nil, fmt.Errorf("OpenAPI paths must be an object")
	}
	for _, path := range Keys(paths) {
		item, err := d.Resolve(Map(paths[path]))
		if err != nil {
			d.Warnings = append(d.Warnings, path+": "+err.Error())
			continue
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"} {
			if Map(item[method]) == nil {
				continue
			}
			opRaw := Map(item[method])
			opID, _ := opRaw["operationId"].(string)
			if opID == "" {
				opID = strings.ToUpper(method) + " " + path
			}
			body, bodyErr := d.Resolve(Map(opRaw["requestBody"]))
			media := Keys(Map(body["content"]))
			if len(media) == 0 {
				media = []string{""}
			}
			for _, mt := range media {
				op := model.Operation{Key: strings.ToUpper(method) + " " + path, ID: opID, Method: strings.ToUpper(method), Path: path, MediaType: mt, Raw: model.Clone(opRaw), BodyRequired: Bool(body["required"])}
				if len(media) > 1 {
					op.Key += " [" + mt + "]"
				}
				if bodyErr != nil {
					op.Warnings = append(op.Warnings, bodyErr.Error())
				}
				if mt != "" && !JSONMedia(mt) {
					op.Warnings = append(op.Warnings, "unsupported request media type: "+mt)
				}
				pm := map[string]map[string]any{}
				for _, src := range []map[string]any{item, opRaw} {
					for _, v := range Slice(src["parameters"]) {
						p, err := d.Resolve(Map(v))
						if err != nil {
							op.Warnings = append(op.Warnings, err.Error())
							continue
						}
						pm[Str(p["in"])+":"+Str(p["name"])] = p
					}
				}
				for _, key := range Keys(pm) {
					p := pm[key]
					in := Str(p["in"])
					s, err := d.Resolve(Map(p["schema"]))
					if err != nil {
						op.Warnings = append(op.Warnings, err.Error())
					}
					style := Str(p["style"])
					if style == "" {
						style = "simple"
						if in == "query" || in == "cookie" {
							style = "form"
						}
					}
					explode := style == "form"
					if x, ok := p["explode"].(bool); ok {
						explode = x
					}
					if _, ok := s["example"]; !ok {
						if v, ok := p["example"]; ok {
							s["example"] = v
						}
					}
					op.Fields = append(op.Fields, model.Field{ID: key, In: in, Name: Str(p["name"]), Schema: s, Required: Bool(p["required"]), Style: style, Explode: explode})
				}
				if mt != "" {
					content := Map(Map(body["content"])[mt])
					op.Schema, err = d.Resolve(Map(content["schema"]))
					if err != nil {
						op.Warnings = append(op.Warnings, err.Error())
					}
					if v, ok := content["example"]; ok {
						op.Schema["example"] = v
					}
					if ex := Map(content["examples"]); len(ex) > 0 {
						for _, name := range Keys(ex) {
							if v, ok := Map(ex[name])["value"]; ok {
								op.Schema["example"] = v
								break
							}
						}
					}
					op.Fields = append(op.Fields, Fields(op.Schema, "", "", 0)...)
				}
				if _, ok := op.Raw["security"]; !ok {
					op.Raw["security"] = raw["security"]
				}
				d.Operations = append(d.Operations, op)
			}
		}
	}
	return d, nil
}

func (d *Document) Find(id string) (model.Operation, bool) {
	for _, o := range d.Operations {
		if o.Key == id || o.ID == id {
			return o, true
		}
	}
	return model.Operation{}, false
}

func (d *Document) Resolve(v map[string]any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	var walk func(any, map[string]bool, int) (any, error)
	walk = func(v any, stack map[string]bool, depth int) (any, error) {
		if depth > 64 {
			return nil, fmt.Errorf("schema reference nesting exceeds 64")
		}
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				if stack[ref] {
					return model.Clone(x), nil
				}
				target, ok := model.Get(d.Raw, strings.TrimPrefix(ref, "#"))
				if !ok {
					return nil, fmt.Errorf("unresolved reference %s", ref)
				}
				next := model.Clone(stack)
				next[ref] = true
				resolved, e := walk(target, next, depth+1)
				if e != nil {
					return nil, e
				}
				m := Map(resolved)
				if m == nil {
					return nil, fmt.Errorf("reference %s does not point to an object", ref)
				}
				siblings := map[string]any{}
				for k, v := range x {
					if k != "$ref" {
						child, err := walk(v, stack, depth+1)
						if err != nil {
							return nil, err
						}
						siblings[k] = child
					}
				}
				if len(siblings) > 0 && (strings.Contains(ref, "/schemas/") || m["type"] != nil || m["properties"] != nil || m["allOf"] != nil) {
					return map[string]any{"allOf": []any{m, siblings}}, nil
				}
				for k, v := range siblings {
					m[k] = v
				}
				return m, nil
			}
			out := map[string]any{}
			for k, v := range x {
				if referenceMap(k) && Map(v) != nil {
					entries := map[string]any{}
					for name, child := range Map(v) {
						resolved, err := walk(child, stack, depth+1)
						if err != nil {
							return nil, err
						}
						entries[name] = resolved
					}
					out[k] = entries
					continue
				}
				if literalValue(k) || k == "examples" && !isExampleMap(v) {
					out[k] = model.Clone(v)
					continue
				}
				r, e := walk(v, stack, depth+1)
				if e != nil {
					return nil, e
				}
				out[k] = r
			}
			return out, nil
		case []any:
			out := make([]any, len(x))
			for i, v := range x {
				r, e := walk(v, stack, depth+1)
				if e != nil {
					return nil, e
				}
				out[i] = r
			}
			return out, nil
		default:
			return v, nil
		}
	}
	r, e := walk(v, map[string]bool{}, 0)
	if e != nil {
		return map[string]any{}, e
	}
	return Map(r), nil
}

// Shape collects inventory and generation hints from composition. It is never
// used as a substitute for JSON Schema validation or for rewriting allOf.
func Shape(s map[string]any) map[string]any {
	out := model.Clone(s)
	props := Map(out["properties"])
	if props == nil {
		props = map[string]any{}
	}
	req := Slice(out["required"])
	for _, keyword := range []string{"allOf", "oneOf", "anyOf"} {
		for _, v := range Slice(s[keyword]) {
			child := Shape(Map(v))
			for k, p := range Map(child["properties"]) {
				if old, ok := props[k]; ok {
					props[k] = map[string]any{"allOf": []any{old, p}}
				} else {
					props[k] = p
				}
			}
			if keyword == "allOf" {
				req = append(req, Slice(child["required"])...)
			}
			for k, v := range child {
				if _, ok := out[k]; !ok && k != "properties" && k != "required" {
					out[k] = v
				}
			}
		}
	}
	if len(props) > 0 {
		out["properties"] = props
		if out["type"] == nil {
			out["type"] = "object"
		}
	}
	if len(req) > 0 {
		out["required"] = req
	}
	return out
}

func Fields(s map[string]any, ptr, sp string, depth int) []model.Field {
	if depth > 12 {
		return nil
	}
	shape := Shape(s)
	var out []model.Field
	for _, name := range Keys(Map(shape["properties"])) {
		child := Map(Map(shape["properties"])[name])
		p := ptr + "/" + model.Escape(name)
		schemaPtr := sp + "/properties/" + model.Escape(name)
		required := false
		for _, v := range Slice(shape["required"]) {
			if v == name {
				required = true
			}
		}
		out = append(out, model.Field{ID: "body:" + p, In: "body", Name: name, Pointer: p, SchemaPointer: schemaPtr, Schema: child, Required: required})
		out = append(out, Fields(child, p, schemaPtr, depth+1)...)
	}
	if items := Map(shape["items"]); items != nil {
		out = append(out, Fields(items, ptr+"/0", sp+"/items", depth+1)...)
	}
	if ptr == "" && len(out) == 0 && len(s) > 0 {
		out = append(out, model.Field{ID: "body:", In: "body", Name: "requestBody", Schema: s})
	}
	return out
}

// Normalize30 converts schema syntax while retaining unrelated extensions.
func Normalize30(v any) {
	switch x := v.(type) {
	case map[string]any:
		if Bool(x["nullable"]) {
			switch t := x["type"].(type) {
			case string:
				x["type"] = []any{t, "null"}
			case []any:
				x["type"] = append(t, "null")
			}
			delete(x, "nullable")
		}
		for _, bound := range []string{"Minimum", "Maximum"} {
			k := "exclusive" + bound
			if b, ok := x[k].(bool); ok {
				delete(x, k)
				if b {
					old := strings.ToLower(bound[:1]) + bound[1:]
					if v, ok := x[old]; ok {
						x[k] = v
						delete(x, old)
					}
				}
			}
		}
		for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"} {
			for _, child := range Map(x[key]) {
				Normalize30(child)
			}
		}
		for _, key := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
			for _, child := range Slice(x[key]) {
				Normalize30(child)
			}
		}
		for _, key := range []string{"items", "contains", "additionalProperties", "unevaluatedProperties", "unevaluatedItems", "propertyNames", "not", "if", "then", "else"} {
			Normalize30(x[key])
		}
	case []any:
		for _, v := range x {
			Normalize30(v)
		}
	}
}

func (d *Document) Compile(s map[string]any) (*jsonschema.Schema, error) {
	root := model.Clone(d.Raw)
	NormalizeDocument(root)
	root["x-inspector-validation"] = model.Clone(s)
	Normalize30(root["x-inspector-validation"])
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	const loc = "https://inspector.invalid/document.json"
	if e := c.AddResource(loc, root); e != nil {
		return nil, e
	}
	return c.Compile(loc + "#/x-inspector-validation")
}

func NormalizeDocument(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "schema" {
				Normalize30(v)
			} else if k == "schemas" {
				for _, s := range Map(v) {
					Normalize30(s)
				}
			} else if k != "example" && k != "examples" && k != "default" {
				NormalizeDocument(v)
			}
		}
	case []any:
		for _, v := range x {
			NormalizeDocument(v)
		}
	}
}
func Validate(raw map[string]any) error {
	b, e := json.Marshal(raw)
	if e != nil {
		return e
	}
	d, e := libopenapi.NewDocumentWithConfiguration(b, &datamodel.DocumentConfiguration{SkipCircularReferenceCheck: true})
	if e != nil {
		return e
	}
	_, e = d.BuildV3Model()
	return e
}
func Keys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func Map(v any) map[string]any { m, _ := v.(map[string]any); return m }
func Slice(v any) []any        { s, _ := v.([]any); return s }
func Str(v any) string         { s, _ := v.(string); return s }
func Bool(v any) bool          { b, _ := v.(bool); return b }
func JSONMedia(s string) bool {
	return s == "application/json" || strings.HasSuffix(strings.Split(s, ";")[0], "+json")
}
