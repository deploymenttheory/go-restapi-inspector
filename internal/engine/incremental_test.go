package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/exporter"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/httpclient"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func deltaDocument() map[string]any {
	get := func() any {
		return map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "OK"}}}}
	}
	raw := map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Delta fixture", "version": "1"}, "paths": map[string]any{"/stable": get(), "/removed": get(), "/changed": get()}}
	spec.Map(spec.Map(spec.Map(raw["paths"])["/changed"])["get"])["parameters"] = []any{map[string]any{"in": "query", "name": "mode", "required": true, "schema": map[string]any{"type": "string", "enum": []any{"a", "b"}, "example": "a"}}}
	return raw
}

func saveSpec(t *testing.T, raw map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.json")
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func deltaConfig(t *testing.T, target string, raw map[string]any) config.Config {
	t.Helper()
	c := config.Defaults()
	c.BaseURL = target
	c.Spec = saveSpec(t, raw)
	c.Output = t.TempDir()
	c.Wait = 0
	c.BoundarySteps = 0
	c.InteractionOrder = 1
	c.ValidationTrials = 2
	c.Mode = "exhaustive"
	c.ConfirmationReserve = -1
	c.Domains = map[string][]model.State{"query:mode": {{Present: true, Value: "a"}, {Present: true, Value: "b"}, {Present: true, Value: "c"}, {Present: false}}}
	return c
}

func TestIncrementalExecutesOnlyRevisionDelta(t *testing.T) {
	var version atomic.Int32
	var stableRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path == "/stable" {
			stableRequests.Add(1)
		}
		if req.URL.Path == "/changed" {
			mode := req.URL.Query().Get("mode")
			if mode != "a" && mode != "b" && (version.Load() != 2 || mode != "c") {
				w.WriteHeader(422)
				_, _ = w.Write([]byte(`{"error":"invalid mode"}`))
				return
			}
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	raw := deltaDocument()
	c := deltaConfig(t, server.URL, raw)
	c.SpecRelease = "v1"
	first, err := Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Report.State != "complete" {
		t.Fatalf("baseline incomplete: %+v", first.Report.Coverage)
	}
	before, err := os.ReadFile(filepath.Join(first.Dir, "journal.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	stableBefore := stableRequests.Load()
	version.Store(2)
	next := model.Clone(raw)
	spec.Map(next["info"])["version"] = "2"
	paths := spec.Map(next["paths"])
	delete(paths, "/removed")
	paths["/added"] = model.Clone(paths["/stable"])
	param := spec.Map(spec.Slice(spec.Map(spec.Map(paths["/changed"])["get"])["parameters"])[0])
	spec.Map(param["schema"])["enum"] = []any{"a", "b", "c"}
	c.Spec = saveSpec(t, next)
	c.BaselineRun = first.Dir
	c.SpecRelease = "v2"
	second, err := Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Report.State != "complete" {
		t.Fatalf("incremental incomplete: %+v", second.Report.Coverage)
	}
	if stableRequests.Load() != stableBefore {
		t.Fatal("unchanged completed endpoint was reprobed")
	}
	if second.Report.Baseline == nil || second.Report.Baseline.Spec.Release != "v1" || second.Report.SpecIdentity.Release != "v2" {
		t.Fatal("missing revision provenance")
	}
	actions := map[string]string{}
	for _, d := range second.Report.Baseline.Operations {
		actions[d.Operation] = d.Action
	}
	if actions["GET /stable"] != "inherit" || actions["GET /changed"] != "inspect" || actions["GET /added"] != "inspect" || actions["GET /removed"] != "history" {
		t.Fatalf("bad delta: %v", actions)
	}
	oldC, newC := false, false
	for _, o := range first.Observations {
		if o.Operation == "GET /changed" && o.Input.Parameters["query:mode"] == "c" && o.Outcome == "input-rejected" {
			oldC = true
		}
	}
	for _, o := range second.Observations {
		if o.Operation == "GET /changed" && o.Origin != nil {
			t.Fatal("invalidated old classifications survived into current evidence")
		}
		if o.Operation == "GET /changed" && o.Input.Parameters["query:mode"] == "c" && o.Outcome == "accepted" {
			newC = true
		}
	}
	if !oldC || !newC {
		t.Fatalf("same payload wasn't retested against changed behaviour: old=%v new=%v", oldC, newC)
	}
	after, _ := os.ReadFile(filepath.Join(first.Dir, "journal.ndjson"))
	if !bytes.Equal(before, after) {
		t.Fatal("parent journal mutated")
	}
	loaded, observations, events, err := Load(second.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Report.Resources) != 0 || len(observations) != len(second.Observations) {
		t.Fatal("inheritance lost evidence or transferred ownership")
	}
	count := 0
	for _, e := range events {
		if e.Kind == "request" {
			count++
		}
	}
	total := 0
	for _, n := range loaded.Report.Requests {
		total += n
	}
	if total != count {
		t.Fatal("inherited traffic counted as current HTTP")
	}
	if err := exporter.Save(second.Dir, second.Document, &second.Report, second.Observations, second.Redactor); err != nil {
		t.Fatal(err)
	}
	// A linked run must resume with its embedded evidence, without its parent.
	if err := os.Rename(first.Dir, first.Dir+"-offline"); err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(context.Background(), second.Dir, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Report.Requests["baseline"] != second.Report.Requests["baseline"] {
		t.Fatal("completed linked run replayed requests")
	}
}

func TestResumeSpecGatePrecedesJournalAndTransport(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(204) }))
	defer server.Close()
	raw := deltaDocument()
	c := deltaConfig(t, server.URL, raw)
	c.Operations = []string{"GET /stable"}
	first, err := Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(first.Dir, "journal.ndjson"))
	count := requests.Load()
	next := model.Clone(raw)
	spec.Map(next["info"])["version"] = "2"
	c.Spec = saveSpec(t, next)
	c.SpecExplicit = true
	// Missing credentials would fail later, if the revision gate were bypassed.
	c.Auth = config.Auth{Type: "bearer", TokenEnv: "INSPECTOR_GATE_UNSET"}
	if _, err := Resume(context.Background(), first.Dir, &c, false, nil); err == nil || !strings.Contains(err.Error(), "specification differs") {
		t.Fatalf("unexpected gate result: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(first.Dir, "journal.ndjson"))
	if !bytes.Equal(before, after) || requests.Load() != count {
		t.Fatal("rejected resume changed evidence or sent a request")
	}
	c.Auth = firstConfig(t, first.Dir).Auth
	b, _ := json.MarshalIndent(raw, "", "  ")
	path := saveSpec(t, raw)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	c.Spec = path
	if _, err := Resume(context.Background(), first.Dir, &c, false, nil); err != nil {
		t.Fatalf("equivalent formatting rejected: %v", err)
	}
	if requests.Load() != count {
		t.Fatal("complete same-revision run replayed")
	}
	// An explicit source at the original path must also be read and compared.
	c.Spec = firstConfig(t, first.Dir).Spec
	b, _ = json.Marshal(next)
	if err := os.WriteFile(c.Spec, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(context.Background(), first.Dir, &c, false, nil); err == nil {
		t.Fatal("changed source at same path bypassed gate")
	}
	if _, err := Resume(context.Background(), first.Dir, nil, false, nil); err != nil {
		t.Fatalf("saved snapshot resume required source: %v", err)
	}
}

func firstConfig(t *testing.T, dir string) config.Config {
	t.Helper()
	c, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIncrementalScopeCoverageAndContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	raw := deltaDocument()
	c := deltaConfig(t, server.URL, raw)
	c.Operations = []string{"GET /stable"}
	first, err := Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.BaselineRun = first.Dir
	c.Operations = nil
	d, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareIncremental(c, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range plan.Lineage.Operations {
		if delta.Operation != "GET /stable" && delta.Selected {
			t.Fatal("scope expanded")
		}
	}
	c.Operations = []string{"GET /changed"}
	plan, err = PrepareIncremental(c, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range plan.Lineage.Operations {
		if delta.Operation == "GET /changed" && delta.Action != "inspect" {
			t.Fatal("uncovered unchanged operation skipped")
		}
	}
	c.Operations = []string{"GET /stable"}
	c.EvidenceContext = "new-principal"
	plan, err = PrepareIncremental(c, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range plan.Lineage.Operations {
		if delta.Operation == "GET /stable" && delta.Action != "inspect" {
			t.Fatal("changed context reused evidence")
		}
	}
	c.EvidenceContext = ""
	delete(spec.Map(raw["paths"]), "/stable")
	d, err = spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = PrepareIncremental(c, d)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Plan.Order) != 0 {
		t.Fatal("removed entire allowlist became all operations")
	}
}

func TestDependencyFingerprintInvalidatesConsumer(t *testing.T) {
	raw := lifecycleDoc()
	c := config.Defaults()
	c.BaseURL = "http://example.invalid"
	makeRunner := func(raw map[string]any) *Runner {
		t.Helper()
		d, err := spec.FromMap(raw)
		if err != nil {
			t.Fatal(err)
		}
		p, err := graph.Build(d, c)
		if err != nil {
			t.Fatal(err)
		}
		f, err := d.Fingerprints()
		if err != nil {
			t.Fatal(err)
		}
		return &Runner{Config: c, Document: d, Plan: p, Fingerprints: f}
	}
	a := makeRunner(raw)
	var consumer model.Operation
	for _, op := range a.Plan.Operations {
		if op.Method == "PATCH" {
			consumer = op
		}
	}
	if consumer.Key == "" {
		t.Fatal("missing fixture update")
	}
	next := model.Clone(raw)
	post := spec.Map(spec.Map(spec.Map(next["paths"])["/widgets"])["post"])
	post["description"] = "Producer IDs now depend on tenant settings"
	b := makeRunner(next)
	other, _ := b.Plan.Find(consumer.Key)
	if a.Fingerprints[consumer.Identity()].Full != b.Fingerprints[other.Identity()].Full {
		t.Fatal("fixture must change producer alone")
	}
	if a.observationSignature(consumer) == b.observationSignature(other) {
		t.Fatal("producer change did not invalidate consumer")
	}
}

func TestConfirmationLedgerCompletesOnlyMissingPairs(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		if req.URL.Query().Get("mode") == "bad" {
			w.WriteHeader(422)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	c := deltaConfig(t, server.URL, deltaDocument())
	d, err := spec.Load(context.Background(), c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c, d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	op, _ := r.Plan.Find("GET /changed")
	if _, err := r.progressFor(op); err != nil {
		t.Fatal(err)
	}
	good := model.Input{Parameters: map[string]any{"query:mode": "a"}}
	bad := model.Input{Parameters: map[string]any{"query:mode": "bad"}}
	r.Config.ValidationTrials = 1
	if _, err := r.confirmPair(context.Background(), op, good, bad, "input-rejected"); err != nil {
		t.Fatal(err)
	}
	// A durable control without its trial must not count as a completed pair.
	if _, err := r.experiment(context.Background(), op, good, "validation-control", ""); err != nil {
		t.Fatal(err)
	}
	r.Config.ValidationTrials = 3
	pairs, err := r.confirmPair(context.Background(), op, good, bad, "input-rejected")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 3 || requests.Load() != 7 {
		t.Fatalf("confirmation delta: pairs=%d requests=%d", len(pairs), requests.Load())
	}
}

func TestBaselineRejectsUnknownWrites(t *testing.T) {
	raw := deltaDocument()
	c := deltaConfig(t, "http://127.0.0.1:1", raw)
	d, err := spec.Load(context.Background(), c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c, d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Journal.Append("run", r.snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := r.Journal.Append("intent", map[string]any{"id": "unknown", "operation": "POST /gone", "method": "POST", "input": model.Input{}}); err != nil {
		t.Fatal(err)
	}
	c.BaselineRun = r.Journal.Dir
	r.Close()
	if _, err := PrepareIncremental(c, d); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("unknown write ignored: %v", err)
	}
	_, err = journal.Read(c.BaselineRun)
	if err != nil {
		t.Fatal(err)
	}
}

func TestIncrementalCompletesUnchangedPartialProbes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if mode := req.URL.Query().Get("mode"); mode != "a" && mode != "b" {
			w.WriteHeader(422)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	raw := deltaDocument()
	c := deltaConfig(t, server.URL, raw)
	c.Operations = []string{"GET /changed"}
	c.MaxRequests = 7
	first, err := Inspect(context.Background(), c, nil)
	if !errors.Is(err, httpclient.ErrBudget) || first.Report.State != "partial" {
		t.Fatalf("expected budget interruption: %v", err)
	}
	oldCases := map[string]bool{}
	events, err := journal.Read(first.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "experiment" {
			var o model.Observation
			if err := journal.Decode(e.Data, &o); err != nil {
				t.Fatal(err)
			}
			if o.Phase == "probe" && reusable(o) {
				oldCases[model.ID(o.Input)] = true
			}
		}
	}
	if len(oldCases) == 0 {
		t.Fatal("fixture did not complete any discovery cases")
	}
	spec.Map(raw["info"])["version"] = "2"
	c.Spec = saveSpec(t, raw)
	c.BaselineRun = first.Dir
	c.MaxRequests = 500
	second, err := Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Report.State != "complete" {
		t.Fatalf("child incomplete: %+v", second.Report.Coverage)
	}
	events, err = journal.Read(second.Dir)
	if err != nil {
		t.Fatal(err)
	}
	freshControl := false
	for _, e := range events {
		if e.Kind == "experiment" {
			var o model.Observation
			if err := journal.Decode(e.Data, &o); err != nil {
				t.Fatal(err)
			}
			if o.Phase == "probe" && oldCases[model.ID(o.Input)] {
				t.Fatal("child replayed a completed parent probe")
			}
			freshControl = freshControl || o.Phase == "resume-control"
		}
	}
	if !freshControl {
		t.Fatal("inherited partial operation did not check a fresh baseline")
	}
}

func TestAddedMediaPreservesEvidenceAndHints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	raw := document(map[string]any{"type": "object"})
	c := deltaConfig(t, server.URL, raw)
	c.Operations = []string{"POST /widgets"}
	c.Hints = map[string]config.Hint{"POST /widgets": {Role: "action"}}
	first, err := Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := spec.Map(spec.Map(spec.Map(spec.Map(raw["paths"])["/widgets"])["post"])["requestBody"])
	spec.Map(request["content"])["application/merge-patch+json"] = map[string]any{"schema": map[string]any{"type": "object"}}
	d, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	c.BaselineRun = first.Dir
	plan, err := PrepareIncremental(c, d)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range plan.Lineage.Operations {
		switch delta.Operation {
		case "POST /widgets [application/json]":
			if delta.Action != "inherit" {
				t.Fatalf("original media invalidated: %+v", delta)
			}
		case "POST /widgets [application/merge-patch+json]":
			if delta.Selected {
				t.Fatal("original representation allowlist expanded to new media")
			}
		}
	}
	if plan.Config.Hints["POST /widgets [application/json]"].Role != "action" {
		t.Fatal("original role hint lost")
	}
}

func TestOperationContextSeparatesPrincipalAndUnrelatedSettings(t *testing.T) {
	raw := deltaDocument()
	d, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	c.Auth = config.Auth{Type: "oauth2-client-credentials", ClientIDEnv: "DELTA_CLIENT_ID", ClientSecretEnv: "DELTA_CLIENT_SECRET", TokenURL: "https://example.invalid/token"}
	t.Setenv("DELTA_CLIENT_ID", "principal-one")
	p, err := graph.Build(d, c)
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.Fingerprints()
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Config: c, Document: d, Plan: p, Fingerprints: f}
	op, _ := p.Find("GET /stable")
	before := r.observationSignature(op)
	r.Config.MaxRequests = 500
	r.Config.Auth.TokenRefreshBuffer = time.Minute
	r.Config.Hints = map[string]config.Hint{"GET /changed": {Role: "action"}}
	if r.observationSignature(op) != before {
		t.Fatal("budget, token refresh timing or unrelated hint invalidated evidence")
	}
	t.Setenv("DELTA_CLIENT_ID", "principal-two")
	if r.observationSignature(op) == before {
		t.Fatal("changed client ID behind the same environment name was ignored")
	}
}
