package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func build(s recording, observed map[string]any) Run {
	r := s.Report
	domains := map[string]any{}
	for _, c := range r.Coverage {
		domains[c.Operation] = c.Domains
	}
	v := Run{ID: r.RunID, Snapshot: s.identity, Target: r.BaseURL, SpecHash: r.SpecHash, State: r.State, Started: stamp(r.Started), Finished: stamp(r.Finished), LatestSession: stamp(s.lastSession), Phases: map[string]int{}, LatestPhases: s.latestPhases, Evidence: map[string]Evidence{}, Source: pretty(s.Document), Operations: []Operation{}, Rules: []Rule{}, Experiments: []Experiment{}, Requests: []Request{}, Changes: []Change{}, Resources: []Resource{}, Warnings: []string{}}
	if observed != nil {
		v.Contract = pretty(observed)
	}
	v.domains = model.ID(domains)
	for _, item := range spec.Map(s.Document["paths"]) {
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head", "options", "trace"} {
			if spec.Map(item)[method] != nil {
				v.Metrics.Inventory++
			}
		}
	}
	for _, q := range s.requests {
		v.Phases[q.Phase]++
		v.Metrics.HTTP++
	}
	v.Metrics.Confirmations = v.Phases["validation"]
	v.Metrics.Controls = v.Phases["control"] + v.Phases["validation-control"]
	if len(r.Coverage) > 0 {
		tested := 0
		for _, c := range r.Coverage {
			tested += c.Tested
		}
		v.Metrics.Discovery = &tested
	}
	selected := map[string]bool{}
	for _, c := range r.Coverage {
		method, path, _ := splitOperation(c.Operation)
		selected[method+" "+path] = true
	}
	v.Metrics.Selected = len(selected)
	for id, o := range s.wire {
		e := Evidence{ID: id, Operation: o.Operation, Phase: o.Phase, Outcome: o.Outcome, Status: o.Status, Sent: o.Sent, Control: o.Control, Input: pretty(o.Input), Response: pretty(o.Response), Headers: pretty(o.Headers), Reason: o.Reason, Started: stamp(o.Started), Duration: o.Duration.String()}
		if completed, ok := s.completed[id]; ok {
			e.Completion = pretty(completed.Response)
			e.Outcome = completed.Outcome
			e.Reason = completed.Reason
		}
		if effect, ok := s.effects[id]; ok {
			e.Before = pretty(effect.Before)
			e.After = pretty(effect.After)
			e.BeforeEvidence = effect.BeforeEvidence
			e.AfterEvidence = effect.ReadEvidence
		}
		v.Evidence[id] = e
	}
	for _, rule := range r.Rules {
		row := ruleView(rule, r.Rules)
		if rule.Status == "supported" {
			v.Metrics.Supported++
		}
		v.Rules = append(v.Rules, row)
	}
	sort.Slice(v.Rules, func(i, j int) bool {
		a, b := v.Rules[i], v.Rules[j]
		return a.Operation+a.Field+a.Summary+a.ID < b.Operation+b.Field+b.Summary+b.ID
	})
	for _, coverage := range r.Coverage {
		method, path, _ := splitOperation(coverage.Operation)
		op := Operation{Key: coverage.Operation, Method: method, Path: path, State: coverage.State, Role: role(s.Config, s.Document, coverage.Operation), Coverage: fmt.Sprintf("%d discovery cases; %s finite combinations", coverage.Tested, coverage.TotalCombinations), ModelConverged: coverage.ModelConverged, InteractionComplete: coverage.InteractionComplete, InputComplete: coverage.InputComplete, Reasons: coverage.Reasons, Fields: []Field{}, Before: schemaAt(s.Document, coverage.Operation), After: schemaAt(observed, coverage.Operation)}
		for _, domain := range coverage.Domains {
			f := domain.Field
			field := Field{ID: f.ID, Name: f.Name, Declared: declared(f), Schema: pretty(f.Schema), Rules: []string{}, Samples: []Sample{}}
			for _, rule := range v.Rules {
				if rule.Operation == op.Key && contains(rule.Fields, f.ID) {
					field.Rules = append(field.Rules, rule.ID)
				}
			}
			samples := map[string]*Sample{}
			for _, o := range s.logical {
				if o.Operation != op.Key || (o.Phase != "probe" && o.Phase != "validation") {
					continue
				}
				value, present := o.Input.Get(f.ID)
				label := "[omitted]"
				if present {
					label = pretty(value)
				}
				outcome := o.Outcome
				if !o.Sent {
					outcome = "unsent"
				}
				key := label + "\x00" + outcome
				if samples[key] == nil {
					samples[key] = &Sample{Value: label, Outcome: outcome}
				}
				samples[key].Evidence = append(samples[key].Evidence, o.ID)
			}
			for _, key := range keys(samples) {
				sample := *samples[key]
				sort.Strings(sample.Evidence)
				field.Samples = append(field.Samples, sample)
			}
			op.Fields = append(op.Fields, field)
		}
		if op.Before == "Unavailable" || op.After == "Unavailable" {
			op.Reasons = append(append([]string{}, op.Reasons...), "One or more saved schema fragments are unavailable.")
		}
		v.Operations = append(v.Operations, op)
	}
	sort.Slice(v.Operations, func(i, j int) bool { return v.Operations[i].Key < v.Operations[j].Key })
	for i := range v.Operations {
		op := &v.Operations[i]
		h := recordedHint(s.Config, s.Document, op.Key)
		for _, binding := range h.Bindings {
			producer := operationKey(s.Document, binding.Operation)
			for j := range v.Operations {
				if v.Operations[j].Key == producer {
					op.Related = append(op.Related, producer)
					v.Operations[j].Related = append(v.Operations[j].Related, op.Key)
				}
			}
		}
		op.Related = unique(op.Related)
	}
	for _, change := range r.Changes {
		if change.Status == "unresolved" {
			v.Metrics.Unresolved++
		}
		status := change.Status
		if status == "" {
			status = "applied"
		}
		v.Changes = append(v.Changes, Change{Operation: change.Operation, Field: change.Pointer, Status: status, Description: change.Description, Rule: change.Rule, Evidence: change.Evidence})
	}
	resources := s.resources
	if len(resources) == 0 {
		for _, resource := range r.Resources {
			resources[resource.ID] = resource
		}
	}
	for _, id := range keys(resources) {
		x := resources[id]
		v.Metrics.Resources++
		if x.State == "deleted" {
			v.Metrics.Deleted++
		}
		v.Resources = append(v.Resources, Resource{ID: id, Operation: x.Operation, State: x.State, Attempts: x.Attempts, Errors: x.Errors, Evidence: x.CreationEvidence})
	}
	buildExperiments(&v, s)
	for _, rule := range v.Rules {
		for _, id := range rule.Evidence {
			if _, ok := v.Evidence[id]; !ok {
				v.Warnings = append(v.Warnings, "Referenced evidence is unavailable: "+id)
			}
		}
	}
	if v.Metrics.Unlinked > 0 {
		v.Warnings = append(v.Warnings, fmt.Sprintf("%d HTTP requests have no provable experiment association; they remain visible in the request ledger.", v.Metrics.Unlinked))
	}
	v.Warnings = unique(v.Warnings)
	return v
}

func buildExperiments(v *Run, s recording) {
	experiments := map[string]*Experiment{}
	owners := map[string]string{}
	resourceOwners := map[string]string{}
	base := map[string]model.Input{}
	for _, id := range sortedObservations(s.logical) {
		o := s.logical[id]
		if o.Phase == "baseline" && o.Outcome == "accepted" {
			if _, ok := base[o.Operation]; !ok {
				base[o.Operation] = o.Input
			}
		}
	}
	for id, p := range s.plans {
		x := &Experiment{ID: id, Operation: p.Operation, Phase: p.Phase, Purpose: p.Purpose, Input: pretty(p.Input), Control: p.Control, Group: p.ConfirmationGroup, Changed: p.ChangedFields, Outcome: "inconclusive", Provenance: "recorded", Requests: []string{}}
		if p.Baseline != nil {
			x.Baseline = pretty(*p.Baseline)
		}
		if end, ok := s.ends[id]; ok {
			x.Observation, x.Outcome, x.Error = end.Observation, end.Outcome, end.Error
			if x.Outcome == "" {
				x.Outcome = "inconclusive"
			}
		} else {
			x.Error = "Experiment has no recorded end event."
		}
		experiments[id] = x
	}
	for _, id := range sortedObservations(s.logical) {
		o := s.logical[id]
		xid := o.ExperimentID
		if xid == "" {
			xid = o.Context
		}
		if xid == "" {
			xid = "legacy-" + id
		}
		x := experiments[xid]
		if x == nil {
			x = &Experiment{ID: xid, Operation: o.Operation, Phase: o.Phase, Purpose: "Not recorded (legacy " + o.Phase + ")", Input: pretty(o.Input), Control: o.Control, Provenance: "legacy links", Requests: []string{}}
			if baseline, ok := base[o.Operation]; ok {
				x.Baseline = pretty(baseline)
				x.Changed = changed(baseline, o.Input, s.Report.Coverage, o.Operation)
			}
			experiments[xid] = x
		}
		x.Observation, x.Outcome = id, o.Outcome
		if !o.Sent {
			x.Outcome = "unsent"
		}
		owners[id] = xid
		if _, ok := s.resources[id]; ok {
			resourceOwners[id] = xid
		}
		wire, exists := s.wire[id]
		if !exists {
			continue
		}
		// Explicit producer bindings can connect legacy fixture records without
		// relying on temporal proximity or assuming ownership of arbitrary IDs.
		hint := recordedHint(s.Config, s.Document, o.Operation)
		for field, binding := range hint.Bindings {
			value, present := wire.Input.Get(field)
			if !present {
				continue
			}
			var matches []string
			for rid, resource := range s.resources {
				if resource.Operation != operationKey(s.Document, binding.Operation) {
					continue
				}
				var candidate any
				var found bool
				switch binding.Source {
				case "", "response":
					candidate, found = model.Get(resource.Response, binding.Pointer)
				case "input":
					candidate, found = resource.Input.Get(binding.Pointer)
				}
				if found && model.Equal(value, candidate) {
					matches = append(matches, rid)
				}
			}
			if len(matches) == 1 {
				resourceOwners[matches[0]] = xid
				owners[matches[0]] = xid
			}
		}
	}
	for id, resource := range s.resources {
		if resource.ExperimentID != "" {
			resourceOwners[id] = resource.ExperimentID
			owners[resource.CreationEvidence] = resource.ExperimentID
		}
	}
	for id, o := range s.wire {
		if o.ExperimentID != "" {
			owners[id] = o.ExperimentID
		} else if experiments[o.Context] != nil {
			owners[id] = o.Context
		} else if o.Phase == "cleanup" && resourceOwners[o.Context] != "" {
			owners[id] = resourceOwners[o.Context]
		}
	}
	for _, q := range s.requests {
		if o, ok := s.wire[q.Evidence]; ok {
			q.Operation, q.Status, q.Outcome = o.Operation, o.Status, o.Outcome
			if completed, ok := s.completed[o.ID]; ok {
				q.Outcome = completed.Outcome
			}
		}
		if q.Experiment == "" {
			q.Experiment = owners[q.Evidence]
		}
		if x := experiments[q.Experiment]; x != nil {
			x.Requests = append(x.Requests, q.ID)
		} else {
			v.Metrics.Unlinked++
		}
		v.Requests = append(v.Requests, q)
	}
	for _, id := range keys(experiments) {
		v.Experiments = append(v.Experiments, *experiments[id])
	}
}

func sortedObservations(m map[string]model.Observation) []string {
	result := keys(m)
	sort.SliceStable(result, func(i, j int) bool {
		a, b := m[result[i]], m[result[j]]
		if a.Started.Equal(b.Started) {
			return a.ID < b.ID
		}
		return a.Started.Before(b.Started)
	})
	return result
}
func changed(a, b model.Input, coverage []model.Coverage, op string) []string {
	var out []string
	for _, c := range coverage {
		if c.Operation != op {
			continue
		}
		for _, d := range c.Domains {
			av, ap := a.Get(d.Field.ID)
			bv, bp := b.Get(d.Field.ID)
			if ap != bp || !model.Equal(av, bv) {
				out = append(out, d.Field.ID)
			}
		}
	}
	return unique(out)
}
func contains(values []string, value string) bool {
	for _, x := range values {
		if x == value {
			return true
		}
	}
	return false
}
func recordedHint(c config.Config, document map[string]any, key string) config.Hint {
	h, ok := c.Hints[key]
	if ok {
		return h
	}
	method, path, _ := splitOperation(key)
	id := spec.Str(spec.Map(spec.Map(spec.Map(document["paths"])[path])[strings.ToLower(method)])["operationId"])
	return c.Hints[id]
}
func operationKey(document map[string]any, id string) string {
	if strings.Contains(id, " /") {
		return id
	}
	for path, item := range spec.Map(document["paths"]) {
		for method, raw := range spec.Map(item) {
			if spec.Str(spec.Map(raw)["operationId"]) == id {
				return strings.ToUpper(method) + " " + path
			}
		}
	}
	return id
}
func role(c config.Config, document map[string]any, key string) string {
	if h := recordedHint(c, document, key); h.Role != "" {
		return h.Role
	}
	m, _, _ := splitOperation(key)
	switch m {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	}
	return "read"
}
func schemaAt(document map[string]any, key string) string {
	if document == nil {
		return "Unavailable"
	}
	method, path, media := splitOperation(key)
	d := &spec.Document{Raw: document}
	item, err := d.Resolve(spec.Map(spec.Map(document["paths"])[path]))
	if err != nil {
		return "Unavailable"
	}
	op := spec.Map(item[strings.ToLower(method)])
	if op == nil {
		return "Unavailable"
	}
	body, err := d.Resolve(spec.Map(op["requestBody"]))
	if err != nil {
		return "Unavailable"
	}
	content := spec.Map(body["content"])
	if len(content) == 0 {
		return pretty(op["parameters"])
	}
	if media == "" {
		media = spec.Keys(content)[0]
	}
	schema, err := d.Resolve(spec.Map(spec.Map(content[media])["schema"]))
	if err != nil {
		return "Unavailable"
	}
	return pretty(schema)
}
func declared(f model.Field) string {
	parts := []string{"Optional"}
	if f.Required {
		parts[0] = "Required"
	}
	if t, ok := f.Schema["type"]; ok {
		parts = append(parts, fmt.Sprint(t))
	}
	if spec.Bool(f.Schema["readOnly"]) {
		parts = append(parts, "read-only")
	}
	return strings.Join(parts, " · ")
}
