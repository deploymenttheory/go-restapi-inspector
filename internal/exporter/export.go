// Package exporter projects reproducible observations into operation-local
// OpenAPI 3.1 schemas. Evidence and unverified claims remain inspectable.
package exporter

import (
	"fmt"
	"slices"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
	"gopkg.in/yaml.v3"
)

const ContractName = "observed-contract-with-the-facts.openapi.yaml"

func Save(dir string, d *spec.Document, report *model.Report, observations []model.Observation, redactor *journal.Redactor) error {
	raw, changes, err := Build(d, *report, observations)
	if err != nil {
		return err
	}
	report.Changes = changes
	for _, change := range changes {
		if change.Status == "unresolved" {
			report.State = "partial"
			for i := range report.Coverage {
				if report.Coverage[i].Operation == change.Operation {
					report.Coverage[i].State = "partial"
					reason := "exported request schema still disagrees with recorded classifications"
					if !slices.Contains(report.Coverage[i].Reasons, reason) {
						report.Coverage[i].Reasons = append(report.Coverage[i].Reasons, reason)
					}
				}
			}
		}
	}
	spec.Map(raw["x-observed-behaviour"])["state"] = report.State
	spec.Map(raw["x-observed-behaviour"])["coverage"] = report.Coverage
	if redactor == nil {
		redactor = journal.NewRedactor(nil)
	}
	// JSON round-tripping before YAML avoids yaml.v3 treating json.Number as a
	// quoted string, and ensures extension keys follow their JSON field names.
	clean := redactor.Redact(raw)
	bytes, err := yaml.Marshal(yamlValue(clean))
	if err != nil {
		return err
	}
	if err = journal.WriteFile(dir, ContractName, bytes); err != nil {
		return err
	}
	if err = journal.WriteJSON(dir, "evidence.json", redactor.Redact(map[string]any{"version": model.Version, "runId": report.RunID, "observations": observations, "rules": report.Rules})); err != nil {
		return err
	}
	return journal.WriteJSON(dir, "report.json", redactor.Redact(report))
}

func Build(d *spec.Document, report model.Report, observations []model.Observation) (map[string]any, []model.Change, error) {
	applicable := make([]model.Observation, 0, len(observations))
	for _, observation := range observations {
		if !observation.EvidenceOnly {
			applicable = append(applicable, observation)
		}
	}
	observations = applicable
	raw := model.Clone(d.Raw)
	normalizeDocument(raw)
	raw["openapi"] = "3.1.1"
	raw["jsonSchemaDialect"] = "https://spec.openapis.org/oas/3.1/dialect/base"
	raw["x-observed-behaviour"] = map[string]any{"version": model.Version, "runId": report.RunID, "sourceSpecHash": report.SpecHash, "baseUrl": report.BaseURL, "state": report.State, "coverage": report.Coverage, "interpretation": "Supported rules are scoped to recorded operations, auth, state and finite inputs. Original claims without corroborating evidence remain unverified.", "evidence": "evidence.json"}
	if report.SpecIdentity != nil {
		spec.Map(raw["x-observed-behaviour"])["specIdentity"] = report.SpecIdentity
	}
	if report.Baseline != nil {
		spec.Map(raw["x-observed-behaviour"])["baseline"] = report.Baseline
	}
	if report.HTMLReport != "" {
		spec.Map(raw["x-observed-behaviour"])["report"] = report.HTMLReport
	}
	var changes []model.Change
	for _, op := range d.Operations {
		paths := spec.Map(raw["paths"])
		item := spec.Map(paths[op.Path])
		if item == nil {
			continue
		}
		if item["$ref"] != nil {
			var err error
			item, err = d.Resolve(item)
			if err != nil {
				return nil, nil, err
			}
			paths[op.Path] = item
		}
		target := spec.Map(item[strings.ToLower(op.Method)])
		if target == nil {
			continue
		}
		var rules []model.Rule
		for _, r := range report.Rules {
			if r.Operation == op.Key {
				rules = append(rules, r)
			}
		}
		target["x-observed-behaviour"] = map[string]any{"version": model.Version, "rules": rules, "unverified": "Declared constraints not corrected or supported by these rules retain their original, unverified status."}
		schema := model.Clone(op.Schema)
		spec.Normalize30(schema)
		// Materialize inherited parameters into this operation before changing one.
		parameters := map[string]map[string]any{}
		for _, src := range []map[string]any{item, target} {
			for _, v := range spec.Slice(src["parameters"]) {
				p, err := d.Resolve(spec.Map(v))
				if err != nil {
					return nil, nil, err
				}
				parameters[spec.Str(p["in"])+":"+spec.Str(p["name"])] = p
			}
		}
		for _, rule := range rules {
			if rule.Status != "supported" {
				continue
			}
			field, hasField := op.Field(rule.Field)
			evidence := append(append([]string{}, rule.Accepted...), rule.Rejected...)
			changed := false
			description := ""
			if rule.Kind == "fieldAcceptsValue" && hasField && field.In == "body" {
				changed = repairField(schema, field.Pointer, rule.Value)
				description = "Relaxed primitive request assertions contradicted by a repeatedly accepted value."
			} else if rule.Kind == "fieldIsOptional" && hasField {
				if field.In == "body" {
					if field.Pointer == "" {
						changed = true
					} else {
						changed = relaxRequired(schema, strings.Split(strings.TrimPrefix(field.Pointer, "/"), "/"))
					}
				} else if field.In != "path" {
					if p := parameters[field.ID]; p != nil && spec.Bool(p["required"]) {
						p["required"] = false
						changed = true
					}
				}
				description = "Removed an unconditional required claim contradicted by reproducible accepted omissions. Conditional rules remain separate."
			} else if field.In != "body" && hasField {
				if rule.Kind == "fieldIsRequired" && field.In != "path" && parameters[field.ID] != nil {
					parameters[field.ID]["required"] = true
					changed = true
					description = "Added an observed required parameter."
				}
				if rule.Kind == "fieldType" && rule.Assert.Op == "type" && parameters[field.ID] != nil {
					p := parameters[field.ID]
					ps, _ := d.Resolve(spec.Map(p["schema"]))
					ps["type"] = rule.Assert.Value
					p["schema"] = ps
					changed = true
					description = "Applied an observed parameter type."
				}
			} else if rule.Assert.Op != "true" && rule.Kind != "fieldIsRequiredForCreateOnly" && rule.Kind != "fieldIsRequiredForUpdateOnly" {
				constraint, ok := RuleSchema(rule)
				if ok {
					schema["allOf"] = append(spec.Slice(schema["allOf"]), constraint)
					changed = true
					description = "Added an evidence-backed request constraint."
				}
			}
			if changed {
				changes = append(changes, model.Change{Operation: op.Key, Rule: rule.ID, Pointer: field.SchemaPointer, Description: description, Evidence: evidence})
			}
		}
		if op.MediaType != "" {
			body, err := d.Resolve(spec.Map(target["requestBody"]))
			if err != nil {
				return nil, nil, err
			}
			content := spec.Map(body["content"])
			if content == nil {
				content = map[string]any{}
				body["content"] = content
			}
			mt := spec.Map(content[op.MediaType])
			if mt == nil {
				mt = map[string]any{}
				content[op.MediaType] = mt
			}
			// Some accepted cases falsify a claim that is not expressible in the
			// current rule language. Record these counterexamples without silently
			// deleting every other constraint on the object.
			var conflicts []string
			var acceptedRejections []string
			bodyOnly := true
			for _, field := range op.Fields {
				if field.In != "body" && field.In != "path" {
					bodyOnly = false
				}
			}
			if compiled, err := d.Compile(schema); err == nil {
				for _, obs := range observations {
					if obs.Operation == op.Key && obs.Outcome == "accepted" && obs.Input.HasBody && obs.Phase != "readback" && obs.Phase != "cleanup" {
						if compiled.Validate(obs.Input.Body) != nil {
							conflicts = append(conflicts, obs.ID)
						}
					}
					if bodyOnly && obs.Operation == op.Key && obs.Outcome == "input-rejected" && obs.Input.HasBody && (obs.Phase == "probe" || obs.Phase == "validation") && compiled.Validate(obs.Input.Body) == nil {
						acceptedRejections = append(acceptedRejections, obs.ID)
					}
				}
			} else {
				return nil, nil, fmt.Errorf("compile exported schema for %s: %w", op.Key, err)
			}
			if len(conflicts) > 0 {
				schema["x-observed-counterexamples"] = map[string]any{"evidence": conflicts, "status": "unresolved declared constraints; inspect before SDK ingestion"}
				changes = append(changes, model.Change{Operation: op.Key, Status: "unresolved", Description: "Accepted inputs still contradict declared constraints. Resolve these before SDK ingestion.", Evidence: conflicts})
			}
			if len(acceptedRejections) > 0 {
				schema["x-observed-rejected-inputs"] = map[string]any{"evidence": acceptedRejections, "status": "unresolved missing request constraints"}
				changes = append(changes, model.Change{Operation: op.Key, Status: "unresolved", Description: "Exported request schema accepts inputs classified as rejected by the API.", Evidence: acceptedRejections})
			}
			mt["schema"] = schema
			for _, rule := range rules {
				if rule.Status == "supported" && rule.Kind == "fieldIsOptional" && rule.Field == "body:" {
					body["required"] = false
				}
			}
			target["requestBody"] = body
		}
		if len(parameters) > 0 {
			var values []any
			for _, k := range spec.Keys(parameters) {
				values = append(values, parameters[k])
			}
			target["parameters"] = values
		}
		responseChanges, err := responses(d, op, target, observations)
		if err != nil {
			return nil, nil, err
		}
		changes = append(changes, responseChanges...)
	}
	if err := spec.Validate(raw); err != nil {
		return nil, nil, fmt.Errorf("exported OpenAPI validation: %w", err)
	}
	return raw, changes, nil
}

func RuleSchema(rule model.Rule) (map[string]any, bool) {
	// dependentRequired is concise and widely recognized by SDK generators.
	if rule.When != nil && rule.When.Op == "present" && rule.Assert.Op == "present" {
		a, b := strings.TrimPrefix(rule.When.Field, "body:"), strings.TrimPrefix(rule.Assert.Field, "body:")
		if strings.HasPrefix(rule.When.Field, "body:/") && strings.HasPrefix(rule.Assert.Field, "body:/") && strings.Count(a, "/") == 1 && strings.Count(b, "/") == 1 {
			return map[string]any{"dependentRequired": map[string]any{model.Unescape(a[1:]): []any{model.Unescape(b[1:])}}}, true
		}
	}
	assertion, ok := PredicateSchema(rule.Assert)
	if !ok {
		return nil, false
	}
	if rule.When == nil {
		return assertion, true
	}
	condition, ok := PredicateSchema(*rule.When)
	if !ok {
		return nil, false
	}
	return map[string]any{"if": condition, "then": assertion}, true
}
func PredicateSchema(p model.Predicate) (map[string]any, bool) {
	switch p.Op {
	case "true":
		return map[string]any{}, true
	case "not":
		if len(p.Args) != 1 {
			return nil, false
		}
		s, ok := PredicateSchema(p.Args[0])
		return map[string]any{"not": s}, ok
	case "all", "any", "one", "atMostOne", "allOrNone":
		var parts []any
		for _, a := range p.Args {
			s, ok := PredicateSchema(a)
			if !ok {
				return nil, false
			}
			parts = append(parts, s)
		}
		switch p.Op {
		case "all":
			return map[string]any{"allOf": parts}, true
		case "any":
			return map[string]any{"anyOf": parts}, true
		case "one":
			return map[string]any{"oneOf": parts}, true
		case "allOrNone":
			var none []any
			for _, s := range parts {
				none = append(none, map[string]any{"not": s})
			}
			return map[string]any{"anyOf": []any{map[string]any{"allOf": parts}, map[string]any{"allOf": none}}}, true
		default:
			var clauses []any
			for i, a := range parts {
				for _, b := range parts[i+1:] {
					clauses = append(clauses, map[string]any{"not": map[string]any{"allOf": []any{a, b}}})
				}
			}
			return map[string]any{"allOf": clauses}, true
		}
	}
	if !strings.HasPrefix(p.Field, "body:") || p.Other != "" {
		return nil, false
	}
	ptr := strings.TrimPrefix(p.Field, "body:")
	var leaf map[string]any
	presence := false
	switch p.Op {
	case "present":
		leaf = map[string]any{}
		presence = true
	case "absent":
		s, ok := PredicateSchema(model.Predicate{Op: "present", Field: p.Field})
		return map[string]any{"not": s}, ok
	case "eq", "null":
		value := p.Value
		if p.Op == "null" {
			value = nil
		}
		leaf = map[string]any{"const": value}
		presence = true
	case "neq":
		s, ok := PredicateSchema(model.Predicate{Op: "eq", Field: p.Field, Value: p.Value})
		present, _ := PredicateSchema(model.Predicate{Op: "present", Field: p.Field})
		return map[string]any{"allOf": []any{present, map[string]any{"not": s}}}, ok
	case "type", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "minLength", "maxLength", "minItems", "maxItems", "format", "pattern", "enum":
		leaf = map[string]any{p.Op: p.Value}
	case "notType":
		leaf = map[string]any{"not": map[string]any{"type": p.Value}}
	case "integerStringRange":
		pattern, ok := integerRangePattern(p.Value)
		if !ok {
			return nil, false
		}
		leaf = map[string]any{"if": map[string]any{"type": "string", "pattern": model.IntegerStringPattern}, "then": map[string]any{"pattern": pattern}}
	default:
		return nil, false
	}
	if ptr == "" {
		if p.Op == "present" || p.Op == "absent" {
			return nil, false
		}
		return leaf, true
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		name := model.Unescape(parts[i])
		if name == "0" {
			if presence {
				leaf = map[string]any{"type": "array", "minItems": 1, "prefixItems": []any{leaf}}
			} else {
				leaf = map[string]any{"prefixItems": []any{leaf}}
			}
			continue
		}
		node := map[string]any{"properties": map[string]any{name: leaf}}
		if presence {
			node["type"] = "object"
			node["required"] = []any{name}
		}
		leaf = node
	}
	return leaf, true
}

func relaxRequired(s map[string]any, parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	changed := false
	for _, v := range spec.Slice(s["allOf"]) {
		if relaxRequired(spec.Map(v), parts) {
			changed = true
		}
	}
	name := model.Unescape(parts[0])
	if len(parts) == 1 {
		var required []any
		for _, v := range spec.Slice(s["required"]) {
			if v == name {
				changed = true
			} else {
				required = append(required, v)
			}
		}
		if len(required) == 0 {
			delete(s, "required")
		} else {
			s["required"] = required
		}
		return changed
	}
	if name == "0" {
		return relaxRequired(spec.Map(s["items"]), parts[1:]) || changed
	}
	return relaxRequired(spec.Map(spec.Map(s["properties"])[name]), parts[1:]) || changed
}

func responses(d *spec.Document, op model.Operation, target map[string]any, observations []model.Observation) ([]model.Change, error) {
	responses := spec.Map(target["responses"])
	if responses == nil {
		responses = map[string]any{}
		target["responses"] = responses
	}
	var changes []model.Change
	groups := map[string][]model.Observation{}
	for _, obs := range observations {
		if obs.Operation == op.Key && obs.Outcome == "accepted" && obs.Response != nil {
			key := fmt.Sprint(obs.Status)
			if obs.Status >= 200 && obs.Status < 300 {
				groups[key] = append(groups[key], obs)
			}
		}
	}
	for _, status := range spec.Keys(groups) {
		response, err := d.Resolve(spec.Map(responses[status]))
		if err != nil {
			return nil, err
		}
		if len(response) == 0 {
			response = map[string]any{"description": "Observed successful response"}
		}
		content := spec.Map(response["content"])
		if content == nil {
			content = map[string]any{}
			response["content"] = content
		}
		mt := "application/json"
		for k := range content {
			if spec.JSONMedia(k) {
				mt = k
				break
			}
		}
		media := spec.Map(content[mt])
		if media == nil {
			media = map[string]any{}
			content[mt] = media
		}
		original, err := d.Resolve(spec.Map(media["schema"]))
		if err != nil {
			return nil, err
		}
		var variants []any
		var evidence []string
		seen := map[string]bool{}
		compiled, compileErr := d.Compile(original)
		for _, obs := range groups[status] {
			if len(original) > 0 && compileErr == nil && compiled.Validate(obs.Response) == nil {
				continue
			}
			inferred := InferResponse(obs.Response)
			key := model.ID(inferred)
			if !seen[key] {
				seen[key] = true
				variants = append(variants, inferred)
			}
			evidence = append(evidence, obs.ID)
		}
		if len(variants) > 0 {
			if len(original) > 0 {
				variants = append([]any{original}, variants...)
			}
			schema := map[string]any{"anyOf": variants, "x-observed-behaviour": map[string]any{"version": model.Version, "evidence": evidence, "meaning": "Response shapes widened from observed successes; no frequency-based requiredness or closed enum inferred."}}
			media["schema"] = schema
			changes = append(changes, model.Change{Operation: op.Key, Pointer: "/responses/" + status, Description: "Widened response schema to include observed successful shapes.", Evidence: evidence})
		} else {
			media["schema"] = original
		}
		responses[status] = response
	}
	return changes, nil
}
func InferResponse(v any) map[string]any {
	out := map[string]any{"type": model.Type(v)}
	switch x := v.(type) {
	case map[string]any:
		properties := map[string]any{}
		for k, v := range x {
			properties[k] = InferResponse(v)
		}
		out["properties"] = properties
	case []any:
		var variants []any
		seen := map[string]bool{}
		for _, v := range x {
			s := InferResponse(v)
			key := model.ID(s)
			if !seen[key] {
				variants = append(variants, s)
				seen[key] = true
			}
		}
		if len(variants) > 0 {
			out["items"] = map[string]any{"anyOf": variants}
		}
	}
	return out
}
func normalizeDocument(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if k == "schema" {
				spec.Normalize30(v)
			} else if k == "schemas" {
				for _, s := range spec.Map(v) {
					spec.Normalize30(s)
				}
			} else if k != "example" && k != "examples" && k != "default" {
				normalizeDocument(v)
			}
		}
	case []any:
		for _, v := range x {
			normalizeDocument(v)
		}
	}
}
