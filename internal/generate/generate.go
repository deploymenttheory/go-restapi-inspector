package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func Value(schema map[string]any, all bool, depth int) any {
	if depth > 12 {
		return nil
	}
	s := spec.Shape(schema)
	if v, ok := s["example"]; ok {
		return model.Clone(v)
	}
	if ex := spec.Slice(s["examples"]); len(ex) > 0 {
		return model.Clone(ex[0])
	}
	if v, ok := s["const"]; ok {
		return model.Clone(v)
	}
	if en := spec.Slice(s["enum"]); len(en) > 0 {
		return model.Clone(en[0])
	}
	if v, ok := s["default"]; ok {
		return model.Clone(v)
	}
	t := spec.Str(s["type"])
	if ts := spec.Slice(s["type"]); len(ts) > 0 {
		for _, v := range ts {
			if v != "null" {
				t = spec.Str(v)
				break
			}
		}
	}
	if t == "" {
		if s["properties"] != nil {
			t = "object"
		} else {
			t = "string"
		}
	}
	switch t {
	case "object":
		out := map[string]any{}
		required := map[string]bool{}
		for _, v := range spec.Slice(s["required"]) {
			required[spec.Str(v)] = true
		}
		for _, k := range spec.Keys(spec.Map(s["properties"])) {
			child := spec.Map(spec.Map(s["properties"])[k])
			if all || required[k] {
				out[k] = Value(child, all, depth+1)
			}
		}
		return out
	case "array":
		n := 1
		if v, ok := model.Number(s["minItems"]); ok && v.IsInt() {
			n = int(v.Num().Int64())
		}
		if n < 0 {
			n = 0
		}
		if n > 64 {
			n = 64
		}
		out := make([]any, n)
		for i := range out {
			out[i] = Value(spec.Map(s["items"]), all, depth+1)
		}
		return out
	case "boolean":
		return true
	case "integer", "number":
		if v, ok := s["minimum"]; ok {
			return v
		}
		if v, ok := model.Number(s["exclusiveMinimum"]); ok {
			return number(new(big.Rat).Add(v, big.NewRat(1, 1)))
		}
		return json.Number("1")
	case "null":
		return nil
	default:
		formats := map[string]string{"uuid": "8fda9e68-a5c1-4c01-9333-08d84e4c8dfe", "date": "2026-01-15", "date-time": "2026-01-15T12:00:00Z", "email": "inspector@example.invalid", "uri": "https://example.invalid/inspector", "url": "https://example.invalid/inspector", "ipv4": "192.0.2.1", "ipv6": "2001:db8::1"}
		if v, ok := formats[spec.Str(s["format"])]; ok {
			return v
		}
		n := 9
		if v, ok := model.Number(s["minLength"]); ok && v.IsInt() && v.Num().Int64() > int64(n) {
			n = int(v.Num().Int64())
		}
		if v, ok := model.Number(s["maxLength"]); ok && v.IsInt() && v.Num().Int64() < int64(n) {
			n = int(v.Num().Int64())
		}
		if n < 0 {
			n = 0
		}
		if n > 4096 {
			n = 4096
		}
		return strings.Repeat("a", n)
	}
}

func Baselines(op model.Operation, c config.Config) []model.Input {
	var out []model.Input
	seen := map[string]bool{}
	add := func(in model.Input) {
		h := c.Hint(op)
		for id, v := range h.Values {
			_ = in.Set(id, model.State{Present: true, Value: v})
		}
		key := model.ID(in)
		if !seen[key] {
			seen[key] = true
			out = append(out, in)
		}
	}
	for _, all := range []bool{false, true} {
		in := model.Input{Parameters: map[string]any{}}
		if op.MediaType != "" {
			in.HasBody = true
			in.Body = Value(op.Schema, all, 0)
		}
		for _, f := range op.Fields {
			if f.In == "body" {
				continue
			}
			if f.Required || all || f.In == "path" {
				in.Parameters[f.ID] = Value(f.Schema, all, 0)
			}
		}
		add(in)
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		for _, variant := range spec.Slice(op.Schema[keyword]) {
			in := model.Clone(out[0])
			in.Body = Value(spec.Map(variant), true, 0)
			add(in)
		}
	}
	// Missing alternatives and conditional blocks often make the all-fields
	// candidate invalid. A removal is a baseline candidate, not a learned rule.
	if len(out) > 1 {
		full := model.Clone(out[1])
		for _, f := range op.Fields {
			if f.In == "path" {
				continue
			}
			in := model.Clone(full)
			if in.Set(f.ID, model.State{}) == nil {
				add(in)
			}
		}
	}
	return out
}

func Domains(op model.Operation, base model.Input, c config.Config, interaction bool) []model.Domain {
	var domains []model.Domain
	for _, f := range op.Fields {
		if f.In == "path" || f.In == "cookie" || strings.EqualFold(f.Name, "Authorization") {
			continue
		}
		if f.In == "body" && f.Pointer != "" {
			parent := f.Pointer[:strings.LastIndex(f.Pointer, "/")]
			if v, ok := model.Get(base.Body, parent); !ok || v == nil {
				continue
			}
		}
		d := model.Domain{Field: f}
		add := func(s model.State) {
			if !s.Present {
				s.Value = nil
			}
			for _, old := range d.States {
				if old.Present == s.Present && model.Equal(old.Value, s.Value) {
					return
				}
			}
			d.States = append(d.States, s)
		}
		v, ok := base.Get(f.ID)
		if !ok {
			v = Value(f.Schema, true, 0)
		}
		add(model.State{Present: ok, Value: v})
		add(model.State{Present: true, Value: v})
		add(model.State{})
		if explicit, ok := c.Domains[op.Key+"#"+f.ID]; ok {
			d.States = model.Clone(explicit)
		} else if explicit, ok := c.Domains[f.ID]; ok {
			d.States = model.Clone(explicit)
		} else if !Bound(f) {
			s := spec.Shape(f.Schema)
			for _, v := range spec.Slice(s["enum"]) {
				add(model.State{Present: true, Value: v})
			}
			if model.Type(v) == "boolean" {
				add(model.State{Present: true, Value: false})
				add(model.State{Present: true, Value: true})
			}
			if !interaction {
				if n, numeric := model.Number(v); numeric && (s["type"] == "integer" || s["type"] == "number") {
					text := string(number(n))
					for _, value := range []string{text, " " + text + " ", "0", "1.5", "+" + text, "0" + text, "-1"} {
						add(model.State{Present: true, Value: value})
					}
				}
				add(model.State{Present: true, Value: nil})
				for _, v := range []any{"", " ", "inspector-outside-enum", json.Number("0"), json.Number("1.5"), false, []any{}, map[string]any{}} {
					add(model.State{Present: true, Value: v})
				}
				for _, k := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
					if b, ok := model.Number(s[k]); ok {
						for _, delta := range []int64{-1, 0, 1} {
							add(model.State{Present: true, Value: number(new(big.Rat).Add(b, big.NewRat(delta, 1)))})
						}
					}
				}
				for _, k := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
					if b, ok := model.Number(s[k]); ok && b.IsInt() {
						for _, delta := range []int64{-1, 0, 1} {
							n := b.Num().Int64() + delta
							if n < 0 || n > 4096 {
								continue
							}
							var v any
							if strings.HasSuffix(k, "Length") {
								v = strings.Repeat("b", int(n))
							} else {
								arr := make([]any, int(n))
								for i := range arr {
									arr[i] = Value(spec.Map(s["items"]), true, 0)
								}
								v = arr
							}
							add(model.State{Present: true, Value: v})
						}
					}
				}
			}
		}
		if len(d.States) > 0 {
			domains = append(domains, d)
		}
	}
	sort.SliceStable(domains, func(i, j int) bool { return domains[i].Field.ID < domains[j].Field.ID })
	return domains
}
func Bound(f model.Field) bool { return spec.Bool(f.Schema["x-inspector-bound"]) }
func number(r *big.Rat) json.Number {
	if r.IsInt() {
		return json.Number(r.Num().String())
	}
	return json.Number(r.FloatString(12))
}

func Materialize(base model.Input, domains []model.Domain, row []int) (model.Input, error) {
	in := model.Clone(base)
	for i, d := range domains {
		if i >= len(row) || row[i] < 0 || row[i] >= len(d.States) {
			return in, fmt.Errorf("invalid domain assignment")
		}
	}
	for i, d := range domains {
		s := d.States[row[i]]
		if d.Field.In == "body" && d.Field.Pointer != "" && s.Present {
			for j, parent := range domains {
				if parent.Field.In == "body" && parent.Field.Pointer != "" && strings.HasPrefix(d.Field.Pointer, parent.Field.Pointer+"/") {
					p := parent.States[row[j]]
					if !p.Present || p.Value == nil {
						return in, fmt.Errorf("unrealizable child %s under omitted/null parent %s", d.Field.ID, parent.Field.ID)
					}
					if model.Type(p.Value) != "object" && model.Type(p.Value) != "array" {
						return in, fmt.Errorf("child under scalar parent")
					}
				}
			}
		}
		if e := in.Set(d.Field.ID, s); e != nil {
			return in, e
		}
	}
	return in, nil
}
func Cardinality(domains []model.Domain) string {
	n := big.NewInt(1)
	for _, d := range domains {
		n.Mul(n, big.NewInt(int64(len(d.States))))
	}
	return n.String()
}
func Cartesian(ctx context.Context, sizes []int, visit func([]int) error) error {
	row := make([]int, len(sizes))
	for _, n := range sizes {
		if n < 1 {
			return fmt.Errorf("empty finite domain")
		}
	}
	for {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := visit(append([]int(nil), row...)); e != nil {
			return e
		}
		i := len(row) - 1
		for i >= 0 {
			row[i]++
			if row[i] < sizes[i] {
				break
			}
			row[i] = 0
			i--
		}
		if i < 0 {
			return nil
		}
	}
}
func Sizes(domains []model.Domain) []int {
	out := make([]int, len(domains))
	for i, d := range domains {
		out[i] = len(d.States)
	}
	return out
}

type tuple struct {
	fields, values []int
	covered        bool
}

// Cover builds a deterministic covering array by incremental horizontal growth
// and vertical completion. Constraints are not borrowed from the untrusted spec.
func Cover(ctx context.Context, sizes []int, order, maxTuples int) ([][]int, error) {
	if len(sizes) == 0 {
		return [][]int{{}}, nil
	}
	if order > len(sizes) {
		order = len(sizes)
	}
	if order < 1 {
		return nil, fmt.Errorf("interaction order must be positive")
	}
	var rows [][]int
	if e := Cartesian(ctx, sizes[:order], func(row []int) error {
		if len(rows) >= maxTuples {
			return fmt.Errorf("covering-array memory guard exceeded (%d rows)", maxTuples)
		}
		rows = append(rows, row)
		return nil
	}); e != nil {
		return nil, e
	}
	for f := order; f < len(sizes); f++ {
		if err := CheckCombinations(f, order-1, maxTuples); err != nil {
			return nil, err
		}
		var ts []tuple
		for _, scope := range Combinations(f, order-1) {
			fields := append(scope, f)
			sub := make([]int, len(fields))
			for i, k := range fields {
				sub[i] = sizes[k]
			}
			e := Cartesian(ctx, sub, func(values []int) error {
				if len(ts) >= maxTuples {
					return fmt.Errorf("interaction tuple count exceeds max-planning-tuples=%d; reduce interaction order or explicitly increase the memory guard", maxTuples)
				}
				ts = append(ts, tuple{fields: fields, values: values})
				return nil
			})
			if e != nil {
				return nil, e
			}
		}
		match := func(row []int, t tuple) bool {
			for j, k := range t.fields {
				if k >= len(row) || row[k] != t.values[j] {
					return false
				}
			}
			return true
		}
		for i, row := range rows {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			best, bestScore := 0, -1
			row = append(row, 0)
			for v := 0; v < sizes[f]; v++ {
				row[f] = v
				score := 0
				for _, t := range ts {
					if !t.covered && match(row, t) {
						score++
					}
				}
				if score > bestScore {
					best, bestScore = v, score
				}
			}
			row[f] = best
			rows[i] = row
			for j := range ts {
				if match(row, ts[j]) {
					ts[j].covered = true
				}
			}
		}
		for j := range ts {
			if ts[j].covered {
				continue
			}
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			row := make([]int, f+1)
			for i := range row {
				row[i] = -1
			}
			for k, field := range ts[j].fields {
				row[field] = ts[j].values[k]
			}
			for field := range row {
				if row[field] >= 0 {
					continue
				}
				best, bestScore := 0, -1
				for v := 0; v < sizes[field]; v++ {
					row[field] = v
					score := 0
					for _, t := range ts {
						if t.covered {
							continue
						}
						compatible := true
						for i, k := range t.fields {
							if row[k] >= 0 && row[k] != t.values[i] {
								compatible = false
								break
							}
						}
						if compatible {
							score++
						}
					}
					if score > bestScore {
						best, bestScore = v, score
					}
				}
				row[field] = best
			}
			rows = append(rows, row)
			for k := range ts {
				if match(row, ts[k]) {
					ts[k].covered = true
				}
			}
		}
	}
	return rows, nil
}

func CheckCombinations(n, k, limit int) error {
	count := new(big.Int).Binomial(int64(n), int64(k))
	if count.Cmp(big.NewInt(int64(limit))) > 0 {
		return fmt.Errorf("interaction scopes exceed max-planning-tuples=%d", limit)
	}
	return nil
}
func Combinations(n, k int) [][]int {
	if k < 0 || k > n {
		return nil
	}
	var out [][]int
	var walk func(int, []int)
	walk = func(start int, cur []int) {
		if len(cur) == k {
			out = append(out, append([]int{}, cur...))
			return
		}
		for i := start; i <= n-(k-len(cur)); i++ {
			walk(i+1, append(cur, i))
		}
	}
	walk(0, nil)
	return out
}

// Minimize reduces the changed fields, not the entire request. A result is
// 1-minimal only when every final single-change removal has been classified.
func Minimize(ctx context.Context, base, failed model.Input, fields []string, rejects func(model.Input) (bool, error)) (model.Input, error) {
	current := model.Clone(failed)
	changed := append([]string(nil), fields...)
	granularity := 2
	for len(changed) > 0 {
		if e := ctx.Err(); e != nil {
			return current, e
		}
		if granularity > len(changed) {
			granularity = len(changed)
		}
		size := (len(changed) + granularity - 1) / granularity
		reduced := false
		for start := 0; start < len(changed); start += size {
			end := start + size
			if end > len(changed) {
				end = len(changed)
			}
			candidate := model.Clone(current)
			for _, id := range changed[start:end] {
				v, ok := base.Get(id)
				if e := candidate.Set(id, model.State{Present: ok, Value: v}); e != nil {
					return current, e
				}
			}
			yes, e := rejects(candidate)
			if e != nil {
				return current, e
			}
			if yes {
				current = candidate
				changed = append(changed[:start], changed[end:]...)
				if granularity > 2 {
					granularity--
				}
				reduced = true
				break
			}
		}
		if !reduced {
			if granularity >= len(changed) {
				break
			}
			granularity *= 2
		}
	}
	return current, nil
}
