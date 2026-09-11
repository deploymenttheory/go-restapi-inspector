// Package graph identifies producer/consumer relationships using paths,
// response schemas, OpenAPI links and explicit hints. Ambiguity is reported.
package graph

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

type Edge struct {
	Consumer string `json:"consumer"`
	Field    string `json:"field"`
	Producer string `json:"producer"`
	Pointer  string `json:"pointer"`
	Source   string `json:"source"`
	Basis    string `json:"basis"`
}
type Plan struct {
	Operations []model.Operation `json:"operations"`
	Edges      []Edge            `json:"edges"`
	Order      []string          `json:"order"`
	Warnings   []string          `json:"warnings,omitempty"`
}

func Build(d *spec.Document, c config.Config) (*Plan, error) {
	p := &Plan{Operations: model.Clone(d.Operations), Warnings: append([]string{}, d.Warnings...)}
	for i, op := range p.Operations {
		h := c.Hint(op)
		for _, f := range op.Fields {
			if _, fixed := h.Values[f.ID]; fixed {
				continue
			}
			var candidates []Edge
			if b, ok := h.Bindings[f.ID]; ok {
				producer, found := d.Find(b.Operation)
				if !found {
					return nil, fmt.Errorf("%s binding %s: unknown producer %s", op.Key, f.ID, b.Operation)
				}
				candidates = []Edge{{Consumer: op.Key, Field: f.ID, Producer: producer.Key, Pointer: b.Pointer, Source: b.Source, Basis: "configured"}}
			} else {
				// Links are explicit operation relations and outrank naming heuristics.
				candidates = linkEdges(d, op, f)
				if len(candidates) == 0 {
					for _, producer := range d.Operations {
						if producer.Key == op.Key || Role(producer, c) != "create" {
							continue
						}
						if f.In == "path" {
							prefix := op.Path[:strings.Index(op.Path, "{"+f.Name+"}")]
							prefix = strings.TrimSuffix(prefix, "/")
							if producer.Path != prefix {
								continue
							}
						} else if f.In == "body" || f.In == "query" {
							n := normalize(f.Name)
							resource := singular(lastLiteral(producer.Path))
							if n != resource+"id" && n != resource+"uuid" {
								continue
							}
						} else {
							continue
						}
						pointers := identifiers(d, producer, f.Name)
						for _, ptr := range pointers {
							candidates = append(candidates, Edge{Consumer: op.Key, Field: f.ID, Producer: producer.Key, Pointer: ptr, Source: "response", Basis: "path and identifier schema"})
						}
					}
				}
			}
			candidates = unique(candidates)
			if len(candidates) == 1 {
				p.Edges = append(p.Edges, candidates[0])
				for j := range p.Operations[i].Fields {
					if p.Operations[i].Fields[j].ID == f.ID {
						p.Operations[i].Fields[j].Schema["x-inspector-bound"] = true
					}
				}
			} else if len(candidates) > 1 {
				p.Warnings = append(p.Warnings, fmt.Sprintf("%s %s has %d possible producers; configure hints.bindings", op.Key, f.ID, len(candidates)))
			} else if f.In == "path" {
				p.Warnings = append(p.Warnings, op.Key+" needs a value or producer binding for "+f.ID)
			}
		}
	}
	selected := map[string]bool{}
	if len(c.Operations) == 0 {
		for _, o := range p.Operations {
			selected[o.Key] = true
		}
	} else {
		for _, id := range c.Operations {
			found := false
			for _, o := range p.Operations {
				if o.Key == id || o.ID == id {
					selected[o.Key] = true
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown selected operation %q", id)
			}
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("operation dependency cycle at %s; supply a concrete value to break it", key)
		}
		if done[key] {
			return nil
		}
		visiting[key] = true
		for _, e := range p.Edges {
			if e.Consumer == key {
				if err := visit(e.Producer); err != nil {
					return err
				}
			}
		}
		visiting[key] = false
		done[key] = true
		p.Order = append(p.Order, key)
		return nil
	}
	for _, key := range spec.Keys(selected) {
		if err := visit(key); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *Plan) Find(id string) (model.Operation, bool) {
	for _, o := range p.Operations {
		if o.Key == id || o.ID == id {
			return o, true
		}
	}
	return model.Operation{}, false
}
func (p *Plan) Bindings(key string) []Edge {
	var out []Edge
	for _, e := range p.Edges {
		if e.Consumer == key {
			out = append(out, e)
		}
	}
	return out
}
func Role(op model.Operation, c config.Config) string {
	if h := c.Hint(op); h.Role != "" {
		return h.Role
	}
	switch op.Method {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	default:
		return "read"
	}
}
func (p *Plan) Companion(op model.Operation, method string, c config.Config) string {
	h := c.Hint(op)
	id := h.Read
	if method == "DELETE" {
		id = h.Delete
	}
	if id != "" {
		if o, ok := p.Find(id); ok {
			return o.Key
		}
		return ""
	}
	path := op.Path
	if Role(op, c) == "create" {
		path += "/"
	}
	var matches []string
	for _, o := range p.Operations {
		if o.Method != method {
			continue
		}
		if o.Path == op.Path && Role(op, c) != "create" {
			matches = append(matches, o.Key)
		} else if strings.HasPrefix(o.Path, path) && strings.Count(o.Path, "/") == strings.Count(path, "/") && strings.HasPrefix(strings.TrimPrefix(o.Path, path), "{") {
			matches = append(matches, o.Key)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// ResourceInput binds only to the particular owned resource. It never falls
// back to creating a replacement resource while deleting or reading one.
func (p *Plan) ResourceInput(target model.Operation, r model.Resource) (model.Input, error) {
	in := model.Input{Parameters: map[string]any{}}
	creator, _ := p.Find(r.Operation)
	for _, f := range target.Fields {
		if f.In != "path" {
			continue
		}
		if v, ok := r.Input.Get(f.ID); ok {
			in.Parameters[f.ID] = v
			continue
		}
		found := false
		for _, e := range p.Bindings(target.Key) {
			if e.Field == f.ID && e.Producer == r.Operation {
				if v, ok := Extract(e, r.Input, r.Response, nil); ok {
					in.Parameters[f.ID] = v
					found = true
					break
				}
			}
		}
		if !found && r.Location != "" {
			if v, ok := locationValue(target.Path, f.Name, r.Location); ok {
				in.Parameters[f.ID] = v
				found = true
			}
		}
		if !found && creator.Path != "" && strings.HasPrefix(target.Path, creator.Path+"/") {
			for _, ptr := range []string{"/" + model.Escape(f.Name), "/id", "/uuid", "/data/id", "/data/" + model.Escape(f.Name)} {
				if v, ok := model.Get(r.Response, ptr); ok {
					in.Parameters[f.ID] = v
					found = true
					break
				}
			}
		}
		if !found {
			return in, fmt.Errorf("cannot bind %s to owned resource %s", f.ID, r.ID)
		}
	}
	for id, value := range in.Parameters {
		if s, ok := value.(string); ok && strings.Contains(s, "[REDACTED]") {
			return in, fmt.Errorf("owned resource %s has a redacted identifier %s; cannot address it safely", r.ID, id)
		}
	}
	return in, nil
}
func Extract(e Edge, in model.Input, response any, headers map[string][]string) (any, bool) {
	switch e.Source {
	case "input", "request":
		return in.Get(e.Pointer)
	case "header":
		for k, vs := range headers {
			if strings.EqualFold(k, e.Pointer) && len(vs) > 0 {
				return vs[0], true
			}
		}
		return nil, false
	default:
		return model.Get(response, e.Pointer)
	}
}
func locationValue(template, name, location string) (any, bool) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, false
	}
	want := strings.Split(strings.Trim(template, "/"), "/")
	got := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(got) < len(want) {
		return nil, false
	}
	got = got[len(got)-len(want):]
	for i, part := range want {
		if part == "{"+name+"}" {
			value, err := url.PathUnescape(got[i])
			return value, err == nil
		}
	}
	return nil, false
}
func identifiers(d *spec.Document, op model.Operation, name string) []string {
	var found []string
	for status, v := range spec.Map(op.Raw["responses"]) {
		if !strings.HasPrefix(status, "2") {
			continue
		}
		response, _ := d.Resolve(spec.Map(v))
		for mt, content := range spec.Map(response["content"]) {
			if !spec.JSONMedia(mt) {
				continue
			}
			schema, _ := d.Resolve(spec.Map(spec.Map(content)["schema"]))
			for _, f := range spec.Fields(schema, "", "", 0) {
				n := normalize(f.Name)
				if n == normalize(name) || n == "id" || n == "uuid" {
					found = append(found, f.Pointer)
				}
			}
		}
	}
	sort.Strings(found)
	var out []string
	for _, v := range found {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}
func linkEdges(d *spec.Document, consumer model.Operation, f model.Field) []Edge {
	var out []Edge
	for _, producer := range d.Operations {
		for _, v := range spec.Map(producer.Raw["responses"]) {
			response, _ := d.Resolve(spec.Map(v))
			for _, lv := range spec.Map(response["links"]) {
				link, _ := d.Resolve(spec.Map(lv))
				if spec.Str(link["operationId"]) != consumer.ID {
					continue
				}
				value := spec.Str(spec.Map(link["parameters"])[f.Name])
				if value == "" {
					value = spec.Str(spec.Map(link["parameters"])[f.In+"."+f.Name])
				}
				e := Edge{Consumer: consumer.Key, Producer: producer.Key, Field: f.ID, Basis: "OpenAPI link"}
				switch {
				case strings.HasPrefix(value, "$response.body#"):
					e.Source = "response"
					e.Pointer = strings.TrimPrefix(value, "$response.body#")
				case strings.HasPrefix(value, "$response.header."):
					e.Source = "header"
					e.Pointer = strings.TrimPrefix(value, "$response.header.")
				default:
					continue
				}
				out = append(out, e)
			}
		}
	}
	return out
}
func unique(es []Edge) []Edge {
	seen := map[string]bool{}
	var out []Edge
	for _, e := range es {
		k := model.ID(e.Producer, e.Pointer, e.Source)
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}
func normalize(s string) string {
	return strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(s))
}
func singular(s string) string {
	s = normalize(s)
	if strings.HasSuffix(s, "ies") {
		return strings.TrimSuffix(s, "ies") + "y"
	}
	return strings.TrimSuffix(s, "s")
}
func lastLiteral(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if !strings.Contains(parts[i], "{") {
			return parts[i]
		}
	}
	return ""
}
