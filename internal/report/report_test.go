package report

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

const fixtureOperation = "POST /widgets"
const hostile = `</script><img src="https://example.invalid/tracker" onerror="window.reportInjected=true">`

func fixture(t *testing.T, dir string, legacy bool, count int) model.Report {
	t.Helper()
	j, err := journal.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	appendEvent := func(kind string, v any) {
		t.Helper()
		if err := j.Append(kind, v); err != nil {
			t.Fatal(err)
		}
	}
	c := config.Defaults()
	c.Spec, c.BaseURL = "https://example.invalid/spec", "https://example.invalid/api"
	c.Auth = config.Auth{Type: "exec", Command: []string{"/does/not/exist"}}
	c.SensitiveFields = []string{"privateNote"}
	properties := map[string]any{"name": map[string]any{"type": "string"}, "priority": map[string]any{"type": "integer"}, "mode": map[string]any{"type": "string"}}
	schema := map[string]any{"type": "object", "properties": properties, "required": []any{"priority"}}
	document := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Fixture", "version": "1"}, "paths": map[string]any{"/widgets": map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": schema}}}, "responses": map[string]any{"201": map[string]any{"description": "Created"}}}, "get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "OK"}}}}}}
	r := model.Report{RunID: "same-run-id", SpecHash: "source-hash", BaseURL: c.BaseURL, Started: time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC), State: "partial", Version: model.Version}
	appendEvent("run", map[string]any{"config": c, "document": document, "report": r})
	appendEvent("report", model.Report{RunID: r.RunID, SpecHash: r.SpecHash, State: "partial", Changes: []model.Change{{Status: "unresolved", Description: "obsolete warning"}}, Warnings: []string{"obsolete warning"}})
	appendEvent("request", map[string]any{"phase": "auth", "method": "POST"})
	base := model.Input{Body: map[string]any{"name": "baseline", "priority": 1, "mode": "advanced"}, HasBody: true}
	for i := range count {
		id := fmt.Sprintf("evidence-%03d", i)
		xid := fmt.Sprintf("experiment-%03d", i)
		phase, outcome, status := "probe", "accepted", 201
		if i == 0 {
			phase = "baseline"
		}
		if i == 1 {
			phase = "control"
		}
		if i == 2 {
			phase = "validation"
		}
		if i == 3 {
			phase = "validation-control"
		}
		if i%3 == 0 && i > 0 {
			outcome, status = "rejected", 400
		}
		input := model.Clone(base)
		input.Body.(map[string]any)["name"] = hostile
		input.Body.(map[string]any)["large"] = json.Number("900719925474099312345")
		input.Body.(map[string]any)["privateNote"] = "private-value"
		if i == 4 {
			delete(input.Body.(map[string]any), "name")
		}
		if i == 5 {
			input.Body.(map[string]any)["name"] = nil
		}
		obs := model.Observation{ID: id, ExperimentID: xid, Operation: fixtureOperation, Phase: phase, Sent: true, Input: input, Response: map[string]any{"name": hostile, "id": i, "token": "credential-value"}, Outcome: outcome, Status: status, Started: r.Started.Add(time.Duration(i) * time.Second), Context: xid, Duration: time.Millisecond}
		if i == 2 {
			obs.Control = "evidence-003"
		}
		if legacy {
			obs.ExperimentID = ""
		} else {
			appendEvent("experiment-plan", model.ExperimentPlan{ID: xid, Operation: fixtureOperation, Phase: phase, Purpose: "unary", Input: input, Baseline: &base, ChangedFields: []string{"body:/name"}, Control: obs.Control})
		}
		appendEvent("intent", map[string]any{"id": id, "operation": fixtureOperation, "phase": phase, "method": "POST"})
		q := map[string]any{"phase": phase, "method": "POST"}
		if !legacy {
			q["requestId"], q["experimentId"] = id, xid
		}
		appendEvent("request", q)
		appendEvent("observation", obs)
		appendEvent("experiment", obs)
		if i == 2 {
			appendEvent("effect", map[string]any{"experiment": id, "before": map[string]any{"name": "before", "privateNote": "before-secret"}, "after": map[string]any{"name": "after", "privateNote": "after-secret"}, "beforeEvidence": "evidence-000", "readEvidence": "evidence-001"})
		}
		if !legacy {
			appendEvent("experiment-end", map[string]any{"id": xid, "observation": id, "outcome": outcome})
		}
	}
	unsent := model.Observation{ID: "unsent", Context: "unsent-context", Operation: fixtureOperation, Phase: "probe", Outcome: "inconclusive", Reason: "request budget exhausted", Input: base}
	appendEvent("observation", unsent)
	appendEvent("experiment", unsent)
	fields := []model.Domain{}
	for _, name := range []string{"name", "priority", "mode"} {
		fields = append(fields, model.Domain{Field: model.Field{ID: "body:/" + name, Name: name, In: "body", Required: name == "priority", Schema: properties[name].(map[string]any)}, States: []model.State{{}, {Present: true, Value: nil}, {Present: true, Value: "advanced"}}})
	}
	r.State = "complete"
	r.Finished = r.Started.Add(time.Hour)
	r.Coverage = []model.Coverage{{Operation: fixtureOperation, State: "complete", Tested: count - 4, TotalCombinations: "1000", Domains: fields, ModelConverged: true, InteractionComplete: true}}
	r.Rules = []model.Rule{
		{ID: "required-priority", Operation: fixtureOperation, Kind: "fieldIsRequired", Field: "body:/priority", Assert: model.Predicate{Op: "present", Field: "body:/priority"}, Status: "supported", Trials: 3, Accepted: []string{"evidence-002"}, Rejected: []string{"evidence-006"}},
		{ID: "implied-or", Operation: fixtureOperation, Kind: "fieldsOr", Assert: model.Predicate{Op: "any", Args: []model.Predicate{{Op: "present", Field: "body:/priority"}, {Op: "present", Field: "body:/name"}}}, Status: "supported", Trials: 3},
		{ID: "conditional-name", Operation: fixtureOperation, Kind: "fieldRequiredWhenFieldValueIs", Field: "body:/name", When: &model.Predicate{Op: "eq", Field: "body:/mode", Value: "advanced"}, Assert: model.Predicate{Op: "present", Field: "body:/name"}, Status: "supported", Trials: 3, Accepted: []string{"evidence-002"}, Rejected: []string{"evidence-006"}},
	}
	r.Changes = []model.Change{{Operation: fixtureOperation, Pointer: "/properties/name", Rule: "conditional-name", Description: "Added conditional requirement", Evidence: []string{"evidence-002"}}}
	appendEvent("report", r)
	document["x-observed-behaviour"] = map[string]any{"runId": r.RunID, "sourceSpecHash": r.SpecHash}
	b, _ := json.Marshal(document)
	if err := journal.WriteFile(dir, "observed-contract-with-the-facts.openapi.yaml", b); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadAccountingLegacyAndRedaction(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			dir := t.TempDir()
			fixture(t, dir, legacy, 8)
			r, c, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if c.Auth.Type != "exec" || r.State != "complete" || r.Metrics.HTTP != 9 || r.Metrics.Inventory != 2 || r.Metrics.Selected != 1 || r.Metrics.Controls != 2 || r.Metrics.Confirmations != 1 || *r.Metrics.Discovery != 4 || r.Metrics.Unresolved != 0 {
				t.Fatalf("incorrect snapshot: %+v", r.Metrics)
			}
			if len(r.Experiments) != 9 || r.Metrics.Unlinked != 1 {
				t.Fatalf("incorrect provenance: %d experiments, %d unlinked", len(r.Experiments), r.Metrics.Unlinked)
			}
			encoded := pretty(r)
			for _, secret := range []string{"private-value", "credential-value", "before-secret", "after-secret", "obsolete warning"} {
				if strings.Contains(encoded, secret) {
					t.Errorf("retained %q", secret)
				}
			}
			if !strings.Contains(r.Evidence["evidence-002"].Input, "900719925474099312345") {
				t.Fatal("large integer lost precision")
			}
			if strings.Contains(r.Evidence["evidence-004"].Input, `"name"`) || !strings.Contains(r.Evidence["evidence-005"].Input, `"name": null`) {
				t.Fatal("omission and null were conflated")
			}
			if r.Evidence["evidence-002"].BeforeEvidence != "evidence-000" {
				t.Fatal("lost before-state evidence")
			}
			if !r.Rules[0].Implied && !r.Rules[1].Implied && !r.Rules[2].Implied {
				t.Fatal("redundant OR not identified")
			}
		})
	}
}

func TestOfflineRenderIsPortableAndDoesNotMutateInputs(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir, true, 8)
	before, err := os.ReadFile(filepath.Join(dir, "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := Generate(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.ndjson"))
	if !bytes.Equal(before, after) {
		t.Fatal("render changed journal")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(hostile)) || bytes.Contains(b, []byte("private-value")) || bytes.Contains(b, []byte(`<script src=`)) {
		t.Fatal("unsafe or external content in document")
	}
	start := bytes.Index(b, []byte(`<script id="report-data" type="application/octet-stream">`))
	if start < 0 {
		t.Fatal("missing embedded view")
	}
	encoded := b[start+len(`<script id="report-data" type="application/octet-stream">`):]
	encoded = encoded[:bytes.Index(encoded, []byte("</script>"))]
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	var v View
	if err = journal.Decode(decoded, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || v.Current.Metrics.HTTP != 9 || !strings.Contains(v.Current.Evidence["evidence-002"].Response, `\u003c/script\u003e`) {
		t.Fatal("embedded view lost evidence")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("report permissions: %v", info.Mode())
	}
	if _, err = Generate(dir, dir); err != nil {
		t.Fatal(err)
	}
}

func TestMissingContractAndInterruptedSession(t *testing.T) {
	dir := t.TempDir()
	r := fixture(t, dir, false, 8)
	if err := os.Remove(filepath.Join(dir, "observed-contract-with-the-facts.openapi.yaml")); err != nil {
		t.Fatal(err)
	}
	// A resumed session has newer traffic but no replacement analysis checkpoint.
	interrupted := t.TempDir()
	writer, err := journal.Open(interrupted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = writer.Append("run", map[string]any{"config": config.Defaults(), "report": r}); err != nil {
		t.Fatal(err)
	}
	if err = writer.Append("report", r); err != nil {
		t.Fatal(err)
	}
	if err = writer.Append("resumed", map[string]any{"config": config.Defaults()}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	run, _, err := Load(interrupted)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "running" || !strings.Contains(strings.Join(run.Warnings, " "), "previous checkpoint") {
		t.Fatalf("stale complete result: %s %v", run.State, run.Warnings)
	}
	if _, err = Generate(dir, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = Generate(t.TempDir(), ""); err == nil {
		t.Fatal("missing journal was accepted")
	}
}

func TestSemanticComparisonIgnoresIDsAndExplainsContext(t *testing.T) {
	a := model.Rule{ID: "old", Operation: fixtureOperation, Kind: "minimum", Field: "body:/priority", Assert: model.Predicate{Op: "gte", Field: "body:/priority", Value: 1}, Status: "supported"}
	b := model.Clone(a)
	b.ID = "new"
	b.Trials = 9
	b.Accepted = []string{"new-evidence"}
	old := Run{ID: "same", Snapshot: "old-hash", Rules: []Rule{ruleView(a, nil)}}
	current := Run{ID: "same", Snapshot: "new-hash", Rules: []Rule{ruleView(b, nil)}}
	c := config.Defaults()
	result := Compare(current, old, c, c)
	if len(result.Findings) != 0 || result.SameSnapshot {
		t.Fatalf("ID-only change was semantic: %+v", result)
	}
	b.Assert.Value = 2
	current.Rules = []Rule{ruleView(b, nil)}
	changed := c
	changed.ValidationTrials++
	current.domains = "expanded"
	result = Compare(current, old, changed, c)
	if len(result.Findings) != 1 || result.Findings[0].Status != "Revised finding" || len(result.ContextDifferences) != 2 {
		t.Fatalf("incorrect revision: %+v", result)
	}
	b = model.Clone(a)
	b.Status = "refuted"
	current.Rules = []Rule{ruleView(b, nil)}
	result = Compare(current, old, c, c)
	if len(result.Findings) != 1 || result.Findings[0].Status != "Status changed" {
		t.Fatalf("missing refutation: %+v", result)
	}
}

func TestComparisonCanonicalisesGroupsAndNumbersButPreservesScope(t *testing.T) {
	a := model.Rule{Kind: "fieldsOr", Operation: fixtureOperation, Assert: model.Predicate{Op: "any", Args: []model.Predicate{{Op: "eq", Field: "body:/priority", Value: json.Number("1.0")}, {Op: "present", Field: "body:/name"}}}}
	b := model.Clone(a)
	b.Assert.Args[0].Value = json.Number("1")
	b.Assert.Args[0], b.Assert.Args[1] = b.Assert.Args[1], b.Assert.Args[0]
	if ruleView(a, nil).semantic != ruleView(b, nil).semantic {
		t.Fatal("equivalent OR order or numeric spelling changed the finding")
	}
	b.Assert.Args[1].Value = "1"
	if ruleView(a, nil).semantic == ruleView(b, nil).semantic {
		t.Fatal("numeric string was conflated with a number")
	}
	b = model.Clone(a)
	b.Scope = map[string]string{"responsePointer": "/nested/priority"}
	if ruleView(a, nil).semantic == ruleView(b, nil).semantic {
		t.Fatal("changed readback representation was ignored")
	}
}

// TestBrowserFixtures shares the real journal loader and renderer with browser checks.
func TestBrowserFixtures(t *testing.T) {
	dir := os.Getenv("REPORT_BROWSER_DIR")
	if dir == "" {
		t.Skip("browser fixture output was not requested")
	}
	baseline := t.TempDir()
	current := t.TempDir()
	r := fixture(t, baseline, true, 8)
	fixture(t, current, false, 125)
	r.State = "partial"
	r.Coverage[0].State = "partial"
	r.Coverage[0].ModelConverged = false
	r.Rules[2].Status = "candidate"
	j, err := journal.Open(baseline, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Append("report", r); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	if _, err := Generate(baseline, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(current, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(current, baseline); err != nil {
		t.Fatal(err)
	}
	for _, output := range []struct{ from, subdir, name string }{{baseline, "baseline", Filename}, {current, "current", Filename}, {current, "current", ComparisonFilename}} {
		b, err := os.ReadFile(filepath.Join(output.from, output.name))
		if err != nil {
			t.Fatal(err)
		}
		if err = journal.WriteFile(filepath.Join(dir, output.subdir), output.name, b); err != nil {
			t.Fatal(err)
		}
	}
}
