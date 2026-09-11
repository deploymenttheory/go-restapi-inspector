package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
	"gopkg.in/yaml.v3"
)

// Load reads a verified snapshot without initializing transport or credentials.
func Load(dir string) (Run, config.Config, error) {
	events, err := journal.Read(dir)
	if err != nil {
		return Run{}, config.Config{}, err
	}
	s := recording{wire: map[string]model.Observation{}, completed: map[string]model.Observation{}, logical: map[string]model.Observation{}, plans: map[string]model.ExperimentPlan{}, ends: map[string]struct{ Observation, Outcome, Error string }{}, effects: map[string]struct {
		Before, After                any
		BeforeEvidence, ReadEvidence string
	}{}, resources: map[string]model.Resource{}, latestPhases: map[string]int{}}
	var pending struct{ ID, Operation, Method, Phase string }
	hasRun := false
	for _, e := range events {
		var err error
		switch e.Kind {
		case "run":
			if hasRun {
				return Run{}, s.Config, fmt.Errorf("journal contains multiple run headers")
			}
			hasRun = true
			var initial struct {
				Config   config.Config
				Document map[string]any
				Report   model.Report
			}
			err = journal.Decode(e.Data, &initial)
			s.Config, s.Document, s.Report = initial.Config, initial.Document, initial.Report
			s.lastSession, s.lastSessionSequence = e.Time, e.Sequence
		case "resumed":
			var resumed struct{ Config config.Config }
			err = journal.Decode(e.Data, &resumed)
			s.Config = resumed.Config
			s.lastSession, s.lastSessionSequence = e.Time, e.Sequence
			s.latestPhases = map[string]int{}
		case "document-recovered":
			var recovered struct {
				Document map[string]any
				Hash     string `json:"sourceSpecHash"`
			}
			err = journal.Decode(e.Data, &recovered)
			if recovered.Hash != s.Report.SpecHash {
				return Run{}, s.Config, fmt.Errorf("recovered source hash does not match the run")
			}
			s.Document = recovered.Document
		case "report":
			var report model.Report
			err = journal.Decode(e.Data, &report)
			s.Report, s.hasReport = report, true
			s.reportSequence = e.Sequence
		case "intent":
			pending = struct{ ID, Operation, Method, Phase string }{}
			err = journal.Decode(e.Data, &pending)
		case "request":
			var q struct{ Phase, Method, RequestID, ExperimentID string }
			err = journal.Decode(e.Data, &q)
			id := q.RequestID
			if id == "" && pending.ID != "" && pending.Phase == q.Phase && pending.Method == q.Method {
				id = pending.ID
			}
			row := Request{ID: fmt.Sprintf("request-%d", e.Sequence), Evidence: id, Experiment: q.ExperimentID, Phase: q.Phase, Method: q.Method, Started: stamp(e.Time), Outcome: "inconclusive"}
			if id != "" {
				row.Operation = pending.Operation
			}
			s.requests = append(s.requests, row)
			s.latestPhases[q.Phase]++
			if id == pending.ID {
				pending.ID = ""
			}
		case "observation", "completion", "experiment":
			var o model.Observation
			err = journal.Decode(e.Data, &o)
			switch e.Kind {
			case "experiment":
				s.logical[o.ID] = o
			case "completion":
				s.completed[o.ID] = o
			default:
				s.wire[o.ID] = o
			}
			if pending.ID == o.ID {
				pending.ID = ""
			}
		case "experiment-plan":
			var p model.ExperimentPlan
			err = journal.Decode(e.Data, &p)
			s.plans[p.ID] = p
		case "experiment-end":
			var end struct{ ID, Observation, Outcome, Error string }
			err = journal.Decode(e.Data, &end)
			s.ends[end.ID] = struct{ Observation, Outcome, Error string }{end.Observation, end.Outcome, end.Error}
		case "effect":
			var effect struct {
				Experiment                   string
				Before, After                any
				BeforeEvidence, ReadEvidence string
			}
			err = journal.Decode(e.Data, &effect)
			s.effects[effect.Experiment] = struct {
				Before, After                any
				BeforeEvidence, ReadEvidence string
			}{effect.Before, effect.After, effect.BeforeEvidence, effect.ReadEvidence}
		case "resource":
			var resource model.Resource
			err = journal.Decode(e.Data, &resource)
			s.resources[resource.ID] = resource
		}
		if err != nil {
			return Run{}, s.Config, fmt.Errorf("event %d (%s): %w", e.Sequence, e.Kind, err)
		}
		s.identity = e.Hash
	}
	if !hasRun || s.Report.RunID == "" {
		return Run{}, s.Config, fmt.Errorf("journal contains no run metadata")
	}
	redactor := journal.NewRedactor(s.Config.SensitiveFields)
	// Sanitize structured values before formatting them as display strings.
	clean, err := json.Marshal(redactor.Redact(s))
	if err != nil {
		return Run{}, s.Config, err
	}
	var public struct {
		Config   config.Config
		Document map[string]any
		Report   model.Report
	}
	if err = journal.Decode(clean, &public); err != nil {
		return Run{}, s.Config, err
	}
	s.Config, s.Document, s.Report = public.Config, public.Document, public.Report
	for _, value := range []any{&s.wire, &s.logical, &s.completed, &s.plans, &s.effects, &s.resources, &s.ends} {
		b, err := json.Marshal(redactor.Redact(value))
		if err != nil {
			return Run{}, s.Config, err
		}
		if err = journal.Decode(b, value); err != nil {
			return Run{}, s.Config, err
		}
	}
	observed := map[string]any{}
	warnings := append([]string{}, s.Report.Warnings...)
	path := filepath.Join(dir, "observed-contract-with-the-facts.openapi.yaml")
	if b, e := os.ReadFile(path); e == nil {
		if e = yaml.Unmarshal(b, &observed); e != nil {
			warnings = append(warnings, "Saved contract cannot be parsed; observed schema fragments are unavailable.")
			observed = nil
		} else {
			meta := spec.Map(observed["x-observed-behaviour"])
			if meta["runId"] != s.Report.RunID || meta["sourceSpecHash"] != s.Report.SpecHash {
				warnings = append(warnings, "Saved contract does not match this snapshot; observed schema fragments are unavailable.")
				observed = nil
			} else if state, ok := meta["state"]; ok && state != s.Report.State {
				warnings = append(warnings, "Saved contract has a different determination from the latest checkpoint; observed schema fragments are unavailable.")
				observed = nil
			}
		}
	} else if os.IsNotExist(e) {
		warnings = append(warnings, "No saved observed contract; schema changes remain available from the report.")
	} else {
		return Run{}, s.Config, e
	}
	if observed != nil {
		observed = spec.Map(redactor.Redact(observed))
	}
	run := build(s, observed)
	run.Warnings = append(run.Warnings, warnings...)
	if !s.hasReport {
		run.State = "running"
		run.Warnings = append(run.Warnings, "No analysis checkpoint has been recorded yet.")
	} else if s.reportSequence < s.lastSessionSequence {
		run.State = "running"
		run.Warnings = append(run.Warnings, "The latest session has no analysis checkpoint; findings reflect the previous checkpoint.")
	}
	return run, s.Config, nil
}

func pretty(value any) string {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "Unavailable: " + err.Error()
	}
	return string(b)
}
func stamp(t time.Time) string {
	if t.IsZero() {
		return "Not recorded"
	}
	return t.UTC().Format(time.RFC3339)
}
func unique(values []string) []string {
	set := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if v != "" && !set[v] {
			set[v] = true
			out = append(out, v)
		}
	}
	return out
}
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func splitOperation(key string) (method, path, media string) {
	method, path, _ = strings.Cut(key, " ")
	if i := strings.LastIndex(path, " ["); i >= 0 && strings.HasSuffix(path, "]") {
		media, path = path[i+2:len(path)-1], path[:i]
	}
	return
}
