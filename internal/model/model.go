// Package model defines the versioned, serializable vocabulary shared by the
// planner, experiment runner, learner, and exporter.
package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const Version = 1

const IntegerStringPattern = `^\s*[+-]?[0-9]+\s*$`

type Field struct {
	ID            string         `json:"id"`
	In            string         `json:"in"`
	Name          string         `json:"name"`
	Pointer       string         `json:"pointer,omitempty"`
	SchemaPointer string         `json:"schemaPointer,omitempty"`
	Schema        map[string]any `json:"schema"`
	Required      bool           `json:"required,omitempty"`
	Style         string         `json:"style,omitempty"`
	Explode       bool           `json:"explode,omitempty"`
}

type Operation struct {
	Key          string         `json:"key"`
	ID           string         `json:"id"`
	Method       string         `json:"method"`
	Path         string         `json:"path"`
	MediaType    string         `json:"mediaType,omitempty"`
	Fields       []Field        `json:"fields"`
	Schema       map[string]any `json:"schema,omitempty"`
	Raw          map[string]any `json:"raw"`
	BodyRequired bool           `json:"bodyRequired,omitempty"`
	Warnings     []string       `json:"warnings,omitempty"`
}

// Identity remains stable when another request media type is added or an
// operationId is renamed. Key is retained as the human-facing selector.
func (o Operation) Identity() string { return o.Method + " " + o.Path + " [" + o.MediaType + "]" }

func (o Operation) Field(id string) (Field, bool) {
	for _, f := range o.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return Field{}, false
}

// Input preserves absence separately from JSON null. Parameter keys are in:name;
// body field identifiers are body:/RFC6901/pointers (including concrete indexes).
type Input struct {
	Body       any            `json:"body,omitempty"`
	HasBody    bool           `json:"hasBody"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

func (r Input) Get(id string) (any, bool) {
	if strings.HasPrefix(id, "body:") {
		if !r.HasBody {
			return nil, false
		}
		return Get(r.Body, strings.TrimPrefix(id, "body:"))
	}
	v, ok := r.Parameters[id]
	return v, ok
}

func (r *Input) Set(id string, s State) error {
	if strings.HasPrefix(id, "body:") {
		if !r.HasBody && !s.Present {
			return nil
		}
		if !r.HasBody {
			r.Body = map[string]any{}
			r.HasBody = true
		}
		ptr := strings.TrimPrefix(id, "body:")
		if ptr == "" {
			r.HasBody = s.Present
			r.Body = Clone(s.Value)
			return nil
		}
		var err error
		r.Body, err = Set(r.Body, ptr, s.Value, !s.Present)
		return err
	}
	if r.Parameters == nil {
		r.Parameters = map[string]any{}
	}
	if s.Present {
		r.Parameters[id] = Clone(s.Value)
	} else {
		delete(r.Parameters, id)
	}
	return nil
}

type State struct {
	Present bool `json:"present"`
	Value   any  `json:"value,omitempty"`
}

type Domain struct {
	Field  Field   `json:"field"`
	States []State `json:"states"`
}

// Predicate is an expression tree. Conditions are evaluated on requests, never
// on undocumented assumptions about the server's implementation.
type Predicate struct {
	Op    string      `json:"op"`
	Field string      `json:"field,omitempty"`
	Other string      `json:"other,omitempty"`
	Value any         `json:"value,omitempty"`
	Args  []Predicate `json:"args,omitempty"`
}

func (p Predicate) Eval(in Input) bool {
	v, present := in.Get(p.Field)
	switch p.Op {
	case "true":
		return true
	case "present":
		return present
	case "absent":
		return !present
	case "null":
		return present && v == nil
	case "eq":
		return present && Equal(v, p.Value)
	case "neq":
		return present && !Equal(v, p.Value)
	case "type":
		return !present || TypeMatches(v, fmt.Sprint(p.Value))
	case "notType":
		return !present || !TypeMatches(v, fmt.Sprint(p.Value))
	case "integerStringRange":
		s, ok := v.(string)
		if !present || !ok {
			return true
		}
		n, valid := IntegerString(s)
		if !valid {
			return true
		}
		bounds, ok := p.Value.(map[string]any)
		if !ok {
			return false
		}
		low, lok := Number(bounds["minimum"])
		high, hok := Number(bounds["maximum"])
		return lok && hok && new(big.Rat).SetInt(n).Cmp(low) >= 0 && new(big.Rat).SetInt(n).Cmp(high) <= 0
	case "not":
		return len(p.Args) == 1 && !p.Args[0].Eval(in)
	case "all", "any", "one", "atMostOne", "allOrNone":
		n := 0
		for _, a := range p.Args {
			if a.Eval(in) {
				n++
			}
		}
		switch p.Op {
		case "all":
			return n == len(p.Args)
		case "any":
			return n > 0
		case "one":
			return n == 1
		case "atMostOne":
			return n <= 1
		default:
			return n == 0 || n == len(p.Args)
		}
	case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
		if !present {
			return true
		}
		a, ok := Number(v)
		b, bok := Number(p.Value)
		if !ok || !bok {
			return true
		}
		c := a.Cmp(b)
		switch p.Op {
		case "minimum":
			return c >= 0
		case "maximum":
			return c <= 0
		case "exclusiveMinimum":
			return c > 0
		default:
			return c < 0
		}
	case "minLength", "maxLength", "minItems", "maxItems":
		if !present {
			return true
		}
		n := -1
		if x, ok := v.(string); ok && strings.HasSuffix(p.Op, "Length") {
			n = utf8.RuneCountInString(x)
		}
		if x, ok := v.([]any); ok && strings.HasSuffix(p.Op, "Items") {
			n = len(x)
		}
		if n < 0 {
			return true
		}
		lim, ok := Number(p.Value)
		if !ok {
			return false
		}
		c := new(big.Rat).SetInt64(int64(n)).Cmp(lim)
		if strings.HasPrefix(p.Op, "min") {
			return c >= 0
		}
		return c <= 0
	case "format":
		if !present {
			return true
		}
		s, ok := v.(string)
		return !ok || ValidFormat(s, fmt.Sprint(p.Value))
	case "pattern":
		if !present {
			return true
		}
		s, ok := v.(string)
		if !ok {
			return true
		}
		re, err := regexp.Compile(fmt.Sprint(p.Value))
		return err == nil && re.MatchString(s)
	case "enum":
		if !present {
			return true
		}
		values, _ := p.Value.([]any)
		for _, x := range values {
			if Equal(v, x) {
				return true
			}
		}
		return false
	case "le", "lt", "ge", "gt", "equalFields":
		w, exists := in.Get(p.Other)
		if !present || !exists {
			return true
		}
		if p.Op == "equalFields" {
			return Equal(v, w)
		}
		a, aok := Number(v)
		b, bok := Number(w)
		c := 0
		if aok && bok {
			c = a.Cmp(b)
		} else {
			sa, sok := v.(string)
			sb, tok := w.(string)
			if !sok || !tok {
				return true
			}
			ta, e1 := time.Parse(time.RFC3339, sa)
			tb, e2 := time.Parse(time.RFC3339, sb)
			if e1 != nil || e2 != nil {
				return true
			}
			c = ta.Compare(tb)
		}
		switch p.Op {
		case "le":
			return c <= 0
		case "lt":
			return c < 0
		case "ge":
			return c >= 0
		default:
			return c > 0
		}
	}
	return false
}

func (p Predicate) Fields() []string {
	m := map[string]bool{}
	var walk func(Predicate)
	walk = func(a Predicate) {
		if a.Field != "" {
			m[a.Field] = true
		}
		if a.Other != "" {
			m[a.Other] = true
		}
		for _, b := range a.Args {
			walk(b)
		}
	}
	walk(p)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type Rule struct {
	Origin    *EvidenceOrigin   `json:"origin,omitempty"`
	ID        string            `json:"id"`
	Operation string            `json:"operation"`
	Kind      string            `json:"kind"`
	Field     string            `json:"field,omitempty"`
	When      *Predicate        `json:"when,omitempty"`
	Assert    Predicate         `json:"assert"`
	Status    string            `json:"status"`
	Accepted  []string          `json:"accepted,omitempty"`
	Rejected  []string          `json:"rejected,omitempty"`
	Trials    int               `json:"trials"`
	Scope     map[string]string `json:"scope,omitempty"`
	Notes     []string          `json:"notes,omitempty"`
	Value     any               `json:"value,omitempty"`
}

func (r Rule) Holds(in Input) bool { return r.When != nil && !r.When.Eval(in) || r.Assert.Eval(in) }
func (r Rule) Fields() []string {
	p := r.Assert
	if r.When != nil {
		p = Predicate{Op: "all", Args: []Predicate{p, *r.When}}
	}
	return p.Fields()
}
func (r *Rule) Identify() { r.ID = ID(r.Operation, r.Kind, r.Field, r.When, r.Assert) }

type Observation struct {
	EvidenceOnly bool                `json:"evidenceOnly,omitempty"`
	Origin       *EvidenceOrigin     `json:"origin,omitempty"`
	ExperimentID string              `json:"experimentId,omitempty"`
	Sent         bool                `json:"sent"`
	ID           string              `json:"id"`
	Operation    string              `json:"operation"`
	Phase        string              `json:"phase"`
	Input        Input               `json:"input"`
	Outcome      string              `json:"outcome"`
	Status       int                 `json:"status"`
	Response     any                 `json:"response,omitempty"`
	Headers      map[string][]string `json:"headers,omitempty"`
	Reason       string              `json:"reason,omitempty"`
	Started      time.Time           `json:"started"`
	Duration     time.Duration       `json:"duration"`
	Control      string              `json:"control,omitempty"`
	Context      string              `json:"context,omitempty"`
}

type Resource struct {
	ExperimentID     string   `json:"experimentId,omitempty"`
	ID               string   `json:"id"`
	Operation        string   `json:"operation"`
	Input            Input    `json:"input"`
	Response         any      `json:"response,omitempty"`
	Location         string   `json:"location,omitempty"`
	Parents          []string `json:"parents,omitempty"`
	ReadOperation    string   `json:"readOperation,omitempty"`
	DeleteOperation  string   `json:"deleteOperation,omitempty"`
	State            string   `json:"state"`
	Attempts         int      `json:"attempts"`
	Errors           []string `json:"errors,omitempty"`
	CreationEvidence string   `json:"creationEvidence"`
}

type Coverage struct {
	Origin              *EvidenceOrigin `json:"origin,omitempty"`
	PlanSignature       string          `json:"planSignature,omitempty"`
	Operation           string          `json:"operation"`
	Mode                string          `json:"mode"`
	InteractionOrder    int             `json:"interactionOrder"`
	Domains             []Domain        `json:"domains,omitempty"`
	InteractionDomains  []Domain        `json:"interactionDomains,omitempty"`
	TotalCombinations   string          `json:"totalCombinations"`
	Tested              int             `json:"tested"`
	ModelConverged      bool            `json:"modelConverged"`
	InputComplete       bool            `json:"inputComplete"`
	InteractionComplete bool            `json:"interactionComplete"`
	State               string          `json:"state"`
	Reasons             []string        `json:"reasons,omitempty"`
}

type Report struct {
	SpecIdentity *SpecIdentity  `json:"specIdentity,omitempty"`
	Baseline     *Lineage       `json:"baseline,omitempty"`
	HTMLReport   string         `json:"htmlReport,omitempty"`
	Version      int            `json:"version"`
	RunID        string         `json:"runId"`
	BaseURL      string         `json:"baseUrl"`
	SpecHash     string         `json:"specHash"`
	Started      time.Time      `json:"started"`
	Finished     time.Time      `json:"finished"`
	State        string         `json:"state"`
	Rules        []Rule         `json:"rules"`
	Coverage     []Coverage     `json:"coverage"`
	Resources    []Resource     `json:"resources"`
	Requests     map[string]int `json:"requests"`
	Changes      []Change       `json:"changes"`
	Warnings     []string       `json:"warnings,omitempty"`
}

type Change struct {
	Status      string   `json:"status,omitempty"`
	Operation   string   `json:"operation"`
	Rule        string   `json:"rule,omitempty"`
	Pointer     string   `json:"pointer"`
	Description string   `json:"description"`
	Evidence    []string `json:"evidence,omitempty"`
}

func ID(v ...any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:12])
}
func Clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		panic(err)
	}
	return out
}
func Equal(a, b any) bool {
	x, xok := Number(a)
	y, yok := Number(b)
	if xok && yok {
		return x.Cmp(y) == 0
	}
	if x, ok := a.(map[string]any); ok {
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			w, ok := y[k]
			if !ok || !Equal(v, w) {
				return false
			}
		}
		return true
	}
	if x, ok := a.([]any); ok {
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i, v := range x {
			if !Equal(v, y[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}
func Number(v any) (*big.Rat, bool) {
	var s string
	switch x := v.(type) {
	case json.Number:
		s = x.String()
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	case float64:
		s = strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'g', -1, 32)
	default:
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

func IntegerString(s string) (*big.Int, bool) {
	if matched, _ := regexp.MatchString(IntegerStringPattern, s); !matched {
		return nil, false
	}
	return new(big.Int).SetString(strings.TrimSpace(s), 10)
}
func Type(v any) string {
	if v == nil {
		return "null"
	}
	switch v.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	if n, ok := Number(v); ok {
		if n.IsInt() {
			return "integer"
		}
		return "number"
	}
	return "unknown"
}
func TypeMatches(v any, t string) bool {
	s := Type(v)
	return s == t || t == "number" && s == "integer"
}
func ValidFormat(s, f string) bool {
	switch f {
	case "date-time":
		_, e := time.Parse(time.RFC3339, s)
		return e == nil
	case "date":
		_, e := time.Parse("2006-01-02", s)
		return e == nil
	case "email":
		a, e := mail.ParseAddress(s)
		return e == nil && a.Address == s
	case "ipv4":
		ip := net.ParseIP(s)
		return ip != nil && ip.To4() != nil
	case "ipv6":
		ip := net.ParseIP(s)
		return ip != nil && ip.To4() == nil
	case "uri", "url":
		u, e := url.ParseRequestURI(s)
		return e == nil && u.Scheme != ""
	case "uuid":
		ok, _ := regexp.MatchString(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`, s)
		return ok
	default:
		return false
	}
}

func Escape(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
func Unescape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}
func Get(v any, ptr string) (any, bool) {
	if ptr == "" {
		return v, true
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, false
	}
	for _, part := range strings.Split(ptr[1:], "/") {
		p := Unescape(part)
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[p]
			if !ok {
				return nil, false
			}
		case []any:
			i, e := strconv.Atoi(p)
			if e != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// Set returns the possibly replaced root, preserving array indexes on property
// omission. Removing an array element is a separate length experiment.
func Set(v any, ptr string, value any, remove bool) (any, error) {
	if ptr == "" {
		if remove {
			return nil, nil
		}
		return Clone(value), nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", ptr)
	}
	parts := strings.SplitN(ptr[1:], "/", 2)
	key := Unescape(parts[0])
	tail := ""
	if len(parts) == 2 {
		tail = "/" + parts[1]
	}
	if v == nil {
		if remove {
			return nil, nil
		}
		v = map[string]any{}
	}
	switch x := v.(type) {
	case map[string]any:
		if tail == "" {
			if remove {
				delete(x, key)
			} else {
				x[key] = Clone(value)
			}
			return x, nil
		}
		child, exists := x[key]
		if !exists && remove {
			return x, nil
		}
		updated, e := Set(child, tail, value, remove)
		if e != nil {
			return nil, e
		}
		x[key] = updated
		return x, nil
	case []any:
		i, e := strconv.Atoi(key)
		if e != nil || i < 0 || i >= len(x) {
			return nil, fmt.Errorf("array pointer out of bounds: %q", ptr)
		}
		if tail == "" && remove {
			return append(x[:i], x[i+1:]...), nil
		}
		updated, e := Set(x[i], tail, value, remove)
		if e != nil {
			return nil, e
		}
		x[i] = updated
		return x, nil
	default:
		if remove {
			return v, nil
		}
		return nil, fmt.Errorf("cannot traverse %q through %T", ptr, v)
	}
}
