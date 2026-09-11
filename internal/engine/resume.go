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
	if c.BaseURL != saved.Config.BaseURL {
		return nil, fmt.Errorf("resume cannot change the recorded base-url; owned resources belong to %s", saved.Config.BaseURL)
	}
	if err := c.Validate(true); err != nil {
		return nil, err
	}
	d := saved.documentModel
	p := saved.plan
	if model.ID(c.Hints, c.Operations) != model.ID(saved.Config.Hints, saved.Config.Operations) {
		p, err = graph.Build(d, c)
		if err != nil {
			return nil, err
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
	r.Progress = map[string]*operationProgress{}
	historicalConfig := saved.Config
	for _, event := range events {
		switch event.Kind {
		case "run":
			var original Snapshot
			if err := journal.Decode(event.Data, &original); err != nil {
				return nil, err
			}
			historicalConfig = original.Config
		case "resumed":
			var resumed struct {
				Config config.Config `json:"config"`
			}
			if err := journal.Decode(event.Data, &resumed); err != nil {
				return nil, err
			}
			historicalConfig = resumed.Config
		case "operation-progress":
			var p operationProgress
			if err := journal.Decode(event.Data, &p); err != nil {
				return nil, err
			}
			r.Progress[p.Operation] = &p
		case "experiment":
			var o model.Observation
			if err := journal.Decode(event.Data, &o); err != nil {
				return nil, err
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
				return nil, err
			}
			r.Effects = append(r.Effects, effect)
		}
	}
	if !cleanupOnly {
		for operation, progress := range r.Progress {
			if progress.Signature == observationSignature(c, saved.Report.SpecHash, operation) {
				continue
			}
			for _, observation := range progress.Experiments {
				if observation.Outcome == "accepted" || observation.Outcome == "input-rejected" {
					return nil, fmt.Errorf("resume cannot mix observation contexts for %s; keep auth, oracles and hints unchanged or start a new inspection", operation)
				}
			}
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
