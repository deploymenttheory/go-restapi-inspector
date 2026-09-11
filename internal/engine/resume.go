package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func Load(dir string) (Snapshot, []model.Observation, []journal.Event, error) {
	var snapshot Snapshot
	var observations []model.Observation
	events, err := journal.Read(dir)
	if err != nil {
		return snapshot, nil, nil, err
	}
	resources := map[string]model.Resource{}
	var order []string
	intent := map[string]struct {
		ID, Operation, Method string
		Input                 model.Input
	}{}
	finished := map[string]bool{}
	counts := map[string]int{}
	inherited := false
	for _, e := range events {
		switch e.Kind {
		case "request":
			var request struct{ Phase string }
			if err := journal.Decode(e.Data, &request); err != nil {
				return snapshot, nil, nil, err
			}
			counts[request.Phase]++
		case "run":
			if err := journal.Decode(e.Data, &snapshot); err != nil {
				return snapshot, nil, nil, err
			}
		case "resumed":
			var resumed struct {
				Config config.Config `json:"config"`
			}
			if err := journal.Decode(e.Data, &resumed); err != nil {
				return snapshot, nil, nil, err
			}
			snapshot.Config = resumed.Config
		case "document-recovered":
			var recovered struct {
				Document map[string]any `json:"document"`
				Hash     string         `json:"sourceSpecHash"`
			}
			if err := journal.Decode(e.Data, &recovered); err != nil {
				return snapshot, nil, nil, err
			}
			if recovered.Hash != snapshot.Report.SpecHash {
				return snapshot, nil, nil, fmt.Errorf("recovered document hash does not match the run")
			}
			snapshot.Document = recovered.Document
		case "report":
			var report model.Report
			if err := journal.Decode(e.Data, &report); err != nil {
				return snapshot, nil, nil, err
			}
			snapshot.Report = report
		case "inheritance":
			var state Inheritance
			if err := journal.Decode(e.Data, &state); err != nil {
				return snapshot, nil, nil, err
			}
			inherited = true
			snapshot.Report.Baseline = &state.Lineage
			snapshot.Report.Coverage = state.Coverage
			snapshot.Report.Rules = state.Rules
			observations = append(observations, state.Observations...)
		case "resource":
			var r model.Resource
			if err := journal.Decode(e.Data, &r); err != nil {
				return snapshot, nil, nil, err
			}
			if _, ok := resources[r.ID]; !ok {
				order = append(order, r.ID)
			}
			resources[r.ID] = r
		case "intent":
			var i struct {
				ID, Operation, Method string
				Input                 model.Input
			}
			if err := journal.Decode(e.Data, &i); err != nil {
				return snapshot, nil, nil, err
			}
			intent[i.ID] = i
		case "observation", "completion":
			var o model.Observation
			if err := journal.Decode(e.Data, &o); err != nil {
				return snapshot, nil, nil, err
			}
			observations = append(observations, o)
			finished[o.ID] = true
		}
	}
	if snapshot.Report.RunID == "" {
		return snapshot, nil, nil, fmt.Errorf("journal contains no run metadata")
	}
	if snapshot.Config.BaselineRun != "" && !inherited {
		return snapshot, nil, nil, fmt.Errorf("incremental run was interrupted before evidence inheritance; start a new inspection from the baseline")
	}
	if snapshot.DocumentFingerprint != "" && snapshot.DocumentFingerprint != spec.Fingerprint(snapshot.Document) {
		return snapshot, nil, nil, fmt.Errorf("saved document fingerprint does not match run metadata")
	}
	var d *spec.Document
	if damagedMetadata(snapshot.Document) {
		original, err := spec.Load(context.Background(), snapshot.Config.Spec)
		if err != nil {
			return snapshot, nil, nil, fmt.Errorf("recover redacted specification metadata: %w", err)
		}
		if original.Hash != snapshot.Report.SpecHash || snapshot.Report.SpecHash == "" {
			return snapshot, nil, nil, fmt.Errorf("cannot recover specification metadata: source hash differs from the recorded run")
		}
		snapshot.Document = original.Raw
		snapshot.Recovered = true
		d = original
	}
	// A process can die after the server commits but before the resource event
	// is synced. Keep every unresolved write explicit, never automatically retry.
	for id, i := range intent {
		if _, ok := resources[id]; ok {
			continue
		}
		if i.Method == "GET" || i.Method == "HEAD" || i.Method == "OPTIONS" {
			continue
		}
		if !finished[id] {
			resources[id] = model.Resource{ID: id, Operation: i.Operation, Input: i.Input, State: "ambiguous", CreationEvidence: id, Errors: []string{"write intent has no response; outcome unknown"}}
			order = append(order, id)
		}
	}
	if d == nil {
		d, err = spec.FromMap(snapshot.Document)
		if err != nil {
			return snapshot, nil, nil, err
		}
	}
	d.Hash = snapshot.Report.SpecHash
	snapshot.documentModel = d
	plan, err := graph.Build(d, snapshot.Config)
	if err != nil {
		return snapshot, nil, nil, err
	}
	snapshot.plan = plan
	for _, o := range observations {
		if o.Origin != nil {
			continue
		} // Inherited evidence never transfers ownership.
		op, ok := plan.Find(o.Operation)
		if !ok || graph.Role(op, snapshot.Config) != "create" || o.Outcome == "input-rejected" || !o.Sent {
			continue
		}
		if _, ok := resources[o.ID]; ok {
			continue
		}
		r := model.Resource{ID: o.ID, Operation: op.Key, Input: o.Input, Response: o.Response, CreationEvidence: o.ID, ReadOperation: plan.Companion(op, "GET", snapshot.Config), DeleteOperation: plan.Companion(op, "DELETE", snapshot.Config), State: "ambiguous"}
		r.ExperimentID = o.ExperimentID
		if o.Outcome == "accepted" {
			r.State = "owned"
		}
		for k, vs := range o.Headers {
			if strings.EqualFold(k, "Location") && len(vs) > 0 {
				r.Location = vs[0]
			}
		}
		resources[r.ID] = r
		order = append(order, r.ID)
	}
	snapshot.Report.Resources = nil
	if len(counts) > 0 {
		snapshot.Report.Requests = counts
	}
	for _, id := range order {
		snapshot.Report.Resources = append(snapshot.Report.Resources, resources[id])
	}
	return snapshot, observations, events, nil
}

func Resume(ctx context.Context, dir string, override *config.Config, cleanupOnly bool, log func(string)) (*Result, error) {
	saved, observations, events, err := Load(dir)
	if err != nil {
		return nil, err
	}
	c := saved.Config
	if override != nil {
		c = *override
	}
	if override != nil && (c.SpecExplicit || c.Spec != saved.Config.Spec) {
		provided, err := spec.Load(ctx, c.Spec)
		if err != nil {
			return nil, fmt.Errorf("validate resume specification: %w", err)
		}
		identity := recordedIdentity(saved)
		if identity.Algorithm != spec.FingerprintAlgorithm || provided.Identity("").Canonical != identity.Canonical {
			return nil, fmt.Errorf("resume specification differs from the recorded revision; use inspect --baseline-run %s --spec NEW_SPEC for incremental inspection", dir)
		}
	}
	if c.BaseURL != saved.Config.BaseURL {
		return nil, fmt.Errorf("resume cannot change the recorded base-url; owned resources belong to %s", saved.Config.BaseURL)
	}
	if !cleanupOnly && c.EvidenceContext != saved.Config.EvidenceContext {
		return nil, fmt.Errorf("resume cannot change evidence-context; use inspect --baseline-run for a new observation context")
	}
	if err := c.Validate(true); err != nil {
		return nil, err
	}
	d := saved.documentModel
	p := saved.plan
	if model.ID(c.Hints, c.Operations, c.SelectionExplicit) != model.ID(saved.Config.Hints, saved.Config.Operations, saved.Config.SelectionExplicit) {
		p, err = graph.Build(d, c)
		if err != nil {
			return nil, err
		}
	}
	fingerprints := saved.Fingerprints
	if fingerprints == nil {
		fingerprints, err = d.Fingerprints()
		if err != nil {
			return nil, err
		}
	}
	prepared := &Runner{Config: c, Document: d, Plan: p, Fingerprints: fingerprints}
	if err := prepared.restoreProgress(saved, events); err != nil {
		return nil, err
	}
	if !cleanupOnly {
		for operation, progress := range prepared.Progress {
			op, ok := p.Find(operation)
			if !ok {
				continue
			}
			if !compatibleProgress(prepared, op, progress) {
				for _, observation := range progress.Experiments {
					if observation.Sent && (observation.Outcome == "accepted" || observation.Outcome == "input-rejected") {
						return nil, fmt.Errorf("resume cannot mix observation contexts for %s; keep auth, oracles, dependencies and hints unchanged or start a new inspection", operation)
					}
				}
			}
		}
	}
	r, err := newWithPlan(c, d, dir, p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if saved.Recovered {
		if err := r.Journal.Append("document-recovered", map[string]any{"document": saved.Document, "sourceSpecHash": saved.Report.SpecHash}); err != nil {
			return nil, err
		}
	}
	r.Report = saved.Report
	r.Client.RestoreCounts(saved.Report.Requests)
	r.Observations = observations
	r.Progress, r.Effects, r.Fingerprints = prepared.Progress, prepared.Effects, fingerprints
	// Upgrade verified legacy signatures without changing their original events.
	for key, progress := range r.Progress {
		op, ok := p.Find(key)
		if ok && compatibleProgress(r, op, progress) {
			legacy := !strings.HasPrefix(progress.Signature, "v2:")
			progress.Signature = r.observationSignature(op)
			if legacy {
				if err := r.Journal.Append("operation-context-upgraded", progress); err != nil {
					return nil, err
				}
			}
		}
	}
	for n, coverage := range r.Report.Coverage {
		op, ok := p.Find(coverage.Operation)
		if ok && r.coverageCompatible(op, coverage) {
			r.Report.Coverage[n].PlanSignature = r.planSignature(op.Key)
		}
	}
	r.Log = log
	cleanupCtx, cancel := context.WithTimeout(ctx, c.Cleanup.Timeout)
	cleanupErr := r.Cleanup(cleanupCtx)
	cancel()
	if cleanupOnly || cleanupErr != nil {
		if cleanupErr != nil {
			r.Report.State = "partial"
		}
		err = r.checkpoint()
		return &Result{HTMLReport: c.ReportEnabled(), Dir: dir, Document: d, Report: r.Report, Observations: r.Observations, Redactor: r.Client.Redactor}, errorsJoin(cleanupErr, err)
	}
	if err := r.Journal.Append("resumed", map[string]any{"config": c, "policy": "reuse classified logical experiments; fresh fixtures for remaining trials"}); err != nil {
		return nil, err
	}
	return r.Run(ctx)
}
func errorsJoin(a, b error) error {
	if a != nil && b != nil {
		return fmt.Errorf("%w; checkpoint: %v", a, b)
	}
	if a != nil {
		return a
	}
	return b
}

func damagedMetadata(value any) bool {
	switch x := value.(type) {
	case map[string]any:
		for _, key := range []string{"$ref", "type"} {
			if text, ok := x[key].(string); ok && strings.Contains(text, "[REDACTED]") {
				return true
			}
		}
		for key, child := range x {
			if key == "example" || key == "examples" || key == "default" || key == "const" || key == "enum" {
				continue
			}
			if damagedMetadata(child) {
				return true
			}
		}
	case []any:
		for _, child := range x {
			if damagedMetadata(child) {
				return true
			}
		}
	}
	return false
}

// LoadConfig verifies journal integrity and reads settings without rebuilding
// the operation graph. Live recovery performs that work once in Resume.
func LoadConfig(dir string) (config.Config, error) {
	events, err := journal.Read(dir)
	if err != nil {
		return config.Config{}, err
	}
	var latest config.Config
	for _, event := range events {
		if event.Kind == "run" || event.Kind == "resumed" {
			var recorded struct {
				Config config.Config `json:"config"`
			}
			if err := journal.Decode(event.Data, &recorded); err != nil {
				return config.Config{}, err
			}
			latest = recorded.Config
		}
	}
	if latest.Spec == "" {
		return config.Config{}, fmt.Errorf("journal contains no run configuration")
	}
	return latest, nil
}

func (r *Runner) restoreProgress(saved Snapshot, events []journal.Event) error {
	r.Progress = map[string]*operationProgress{}
	historicalConfig := saved.Config
	for _, event := range events {
		switch event.Kind {
		case "run":
			var original Snapshot
			if err := journal.Decode(event.Data, &original); err != nil {
				return err
			}
			historicalConfig = original.Config
		case "resumed":
			var resumed struct {
				Config config.Config `json:"config"`
			}
			if err := journal.Decode(event.Data, &resumed); err != nil {
				return err
			}
			historicalConfig = resumed.Config
		case "inheritance":
			var inherited Inheritance
			if err := journal.Decode(event.Data, &inherited); err != nil {
				return err
			}
			for _, p := range inherited.Progress {
				r.Progress[p.Operation] = &p
			}
			for _, o := range inherited.Experiments {
				if p := r.Progress[o.Operation]; p != nil {
					p.Experiments = append(p.Experiments, o)
				}
			}
			r.Effects = append(r.Effects, inherited.Effects...)
		case "operation-progress":
			var p operationProgress
			if err := journal.Decode(event.Data, &p); err != nil {
				return err
			}
			r.Progress[p.Operation] = &p
		case "operation-context-upgraded":
			var p operationProgress
			if err := journal.Decode(event.Data, &p); err != nil {
				return err
			}
			if old := r.Progress[p.Operation]; old != nil {
				old.Signature = p.Signature
			}
		case "experiment":
			var o model.Observation
			if err := journal.Decode(event.Data, &o); err != nil {
				return err
			}
			p := r.Progress[o.Operation]
			if p == nil {
				p = &operationProgress{Operation: o.Operation, Signature: observationSignature(historicalConfig, saved.Report.SpecHash, o.Operation)}
				r.Progress[o.Operation] = p
			}
			p.Experiments = append(p.Experiments, o)
		case "effect":
			var effect Effect
			if err := journal.Decode(event.Data, &effect); err != nil {
				return err
			}
			r.Effects = append(r.Effects, effect)
		}
	}
	return nil
}
