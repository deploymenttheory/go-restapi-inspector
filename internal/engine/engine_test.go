package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/exporter"
	"github.com/deploymenttheory/go-restapi-inspector/internal/graph"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func document(schema map[string]any) map[string]any {
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Fixture", "version": "1"}, "paths": map[string]any{"/widgets": map[string]any{"post": map[string]any{"operationId": "createWidget", "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}, "responses": map[string]any{"201": map[string]any{"description": "Created", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}}}}}}}}}}
}
func widgetSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1, "example": "sample"}, "mode": map[string]any{"type": "string", "enum": []any{"simple", "advanced"}}, "certificate": map[string]any{"type": "string"}, "certificatePassword": map[string]any{"type": "string"}}, "required": []any{"name", "mode", "certificate"}}
}
func widgetValid(body map[string]any) bool {
	if body == nil {
		return false
	}
	name, ok := body["name"].(string)
	if !ok || len(name) == 0 {
		return false
	}
	mode := "simple"
	if v, ok := body["mode"]; ok {
		mode, ok = v.(string)
		if !ok || mode != "simple" && mode != "advanced" {
			return false
		}
	}
	_, cert := body["certificate"]
	_, password := body["certificatePassword"]
	if mode == "advanced" && !cert || mode == "simple" && cert || cert && !password {
		return false
	}
	for _, name := range []string{"certificate", "certificatePassword"} {
		if v, ok := body[name]; ok {
			if _, ok := v.(string); !ok {
				return false
			}
		}
	}
	return true
}

func TestInspectionInfersDependenciesAndExportsContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if !widgetValid(body) {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"error":"invalid fields"}`))
			return
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()
	c := config.Defaults()
	c.BaseURL = server.URL
	c.Spec = "fixture"
	c.Wait = 0
	c.ValidationTrials = 2
	c.Operations = []string{"createWidget"}
	c.Hints = map[string]config.Hint{"createWidget": {Role: "action", Values: map[string]any{"body:/name": "sample"}}}
	// Start from a deliberately inaccurate unconditional required list.
	d, err := spec.FromMap(document(widgetSchema()))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c, d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Journal.Append("run", Snapshot{Config: c, Document: d.Raw, Report: r.Report}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, rule := range result.Report.Rules {
		if rule.Status == "supported" {
			kinds[rule.Kind]++
		}
	}
	if kinds["fieldIsRequired"] == 0 || kinds["fieldRequiredWith"] == 0 || kinds["fieldBlockedWhen"] == 0 || kinds["fieldIsOptional"] == 0 {
		t.Fatalf("missing learned rules: %v; coverage=%+v", kinds, result.Report.Coverage)
	}
	if err = exporter.Save(result.Dir, d, &result.Report, result.Observations, result.Redactor); err != nil {
		t.Fatal(err)
	}
	out, err := spec.Load(context.Background(), filepath.Join(result.Dir, exporter.ContractName))
	if err != nil {
		t.Fatal(err)
	}
	op, _ := out.Find("createWidget")
	compiled, err := out.Compile(op.Schema)
	if err != nil {
		t.Fatal(err)
	}
	// Check semantic behavior, not exact generated YAML syntax.
	for _, mode := range []string{"simple", "advanced"} {
		for _, cert := range []bool{false, true} {
			for _, password := range []bool{false, true} {
				body := map[string]any{"name": "sample", "mode": mode}
				if cert {
					body["certificate"] = "abc"
				}
				if password {
					body["certificatePassword"] = "def"
				}
				if actual := compiled.Validate(body) == nil; actual != widgetValid(body) {
					t.Errorf("export mismatch on %v: accepted=%v", body, actual)
				}
			}
		}
	}
	if compiled.Validate(map[string]any{"name": "sample"}) != nil {
		t.Error("original required certificate/mode claims still block a valid omission")
	}
}

func lifecycleDoc() map[string]any {
	raw := document(map[string]any{"type": "object", "required": []any{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string", "example": "fixture"}}})
	item := map[string]any{"parameters": []any{map[string]any{"name": "widgetId", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}}
	for _, method := range []string{"get", "delete", "patch"} {
		operation := map[string]any{"operationId": method + "Widget", "responses": map[string]any{"200": map[string]any{"description": "OK"}}}
		if method == "patch" {
			operation["requestBody"] = map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "example": "changed"}}}}}}
		}
		item[method] = operation
	}
	spec.Map(raw["paths"])["/widgets/{widgetId}"] = item
	return raw
}

func TestFreshUpdateFixturesAndCleanup(t *testing.T) {
	var mu sync.Mutex
	objects := map[string]map[string]any{}
	created, deleted := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "POST" {
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			created++
			id := fmt.Sprint(created)
			body["id"] = id
			objects[id] = body
			w.Header().Set("Location", "/widgets/"+id)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		id := strings.TrimPrefix(req.URL.Path, "/widgets/")
		object, ok := objects[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		switch req.Method {
		case "GET":
			_ = json.NewEncoder(w).Encode(object)
		case "PATCH":
			var body map[string]any
			_ = json.NewDecoder(req.Body).Decode(&body)
			for k, v := range body {
				object[k] = v
			}
			_ = json.NewEncoder(w).Encode(object)
		case "DELETE":
			delete(objects, id)
			deleted++
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	c := config.Defaults()
	c.Spec = "fixture"
	c.BaseURL = server.URL
	c.Wait = 0
	d, err := spec.FromMap(lifecycleDoc())
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c, d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err = r.Journal.Append("run", Snapshot{Config: c, Document: d.Raw, Report: r.Report}); err != nil {
		t.Fatal(err)
	}
	op, _ := r.Plan.Find("patchWidget")
	if len(r.Plan.Bindings(op.Key)) != 1 {
		t.Fatalf("missing ID binding: %+v", r.Plan)
	}
	for range 2 {
		obs, err := r.experiment(context.Background(), op, r.baselines(op)[1], "probe", "")
		if err != nil || obs.Outcome != "accepted" {
			t.Fatalf("experiment: %+v %v", obs, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if created != 2 || deleted != 2 || len(objects) != 0 {
		t.Fatalf("fixtures were reused/leaked: created=%d deleted=%d remaining=%v", created, deleted, objects)
	}
	for _, resource := range r.Report.Resources {
		if resource.State != "deleted" {
			t.Fatalf("not cleaned: %+v", resource)
		}
		if resource.ExperimentID == "" {
			t.Fatal("resource lost experiment provenance")
		}
	}
	events, err := journal.Read(r.Journal.Dir)
	if err != nil {
		t.Fatal(err)
	}
	plans, ends := map[string]bool{}, map[string]bool{}
	phases := map[string]bool{}
	for _, event := range events {
		switch event.Kind {
		case "experiment-plan":
			var plan model.ExperimentPlan
			if err := journal.Decode(event.Data, &plan); err != nil {
				t.Fatal(err)
			}
			plans[plan.ID] = true
		case "request":
			var request struct{ Phase, ExperimentID, RequestID string }
			if err := journal.Decode(event.Data, &request); err != nil {
				t.Fatal(err)
			}
			if !plans[request.ExperimentID] || request.RequestID == "" {
				t.Fatalf("dispatch has no preceding experiment plan or request ID: %+v", request)
			}
			phases[request.Phase] = true
		case "experiment-end":
			var end struct{ ID string }
			if err := journal.Decode(event.Data, &end); err != nil {
				t.Fatal(err)
			}
			ends[end.ID] = true
		}
	}
	if len(plans) != 2 || len(ends) != 2 || !phases["setup"] || !phases["probe"] || !phases["cleanup"] {
		t.Fatalf("incomplete lifecycle provenance: plans=%v ends=%v phases=%v", plans, ends, phases)
	}
}

func TestInterruptedWriteIsNotReplayed(t *testing.T) {
	dir := t.TempDir()
	c := config.Defaults()
	c.Spec = "fixture"
	c.BaseURL = "http://127.0.0.1:1"
	d, err := spec.FromMap(lifecycleDoc())
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(dir, journal.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Append("run", Snapshot{Config: c, Document: d.Raw, Report: model.Report{RunID: "test"}}); err != nil {
		t.Fatal(err)
	}
	if err = j.Append("intent", map[string]any{"id": "pending", "operation": "POST /widgets", "method": "POST", "input": model.Input{}}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	saved, _, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Report.Resources) != 1 || saved.Report.Resources[0].State != "ambiguous" {
		t.Fatalf("lost pending write: %+v", saved.Report.Resources)
	}
	result, err := Resume(context.Background(), dir, nil, true, nil)
	if err == nil || result == nil {
		t.Fatal("ambiguous write should require reconciliation")
	}
	if len(result.Report.Requests) != 0 {
		t.Fatalf("cleanup sent a speculative request: %v", result.Report.Requests)
	}
}

func TestGraphRequiresHintsForAmbiguousIDs(t *testing.T) {
	raw := lifecycleDoc()
	response := spec.Map(spec.Map(spec.Map(spec.Map(raw["paths"])["/widgets"])["post"])["responses"])
	schema := spec.Map(spec.Map(spec.Map(spec.Map(response["201"])["content"])["application/json"])["schema"])
	spec.Map(schema["properties"])["uuid"] = map[string]any{"type": "string"}
	d, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := graph.Build(d, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Edges) != 0 || len(p.Warnings) == 0 {
		t.Fatal("ambiguous identifiers were guessed")
	}
}

func TestCleanupIsBoundedAndKeepsParentsOfLeftovers(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { calls[req.URL.Path]++; w.WriteHeader(503) }))
	defer server.Close()
	c := config.Defaults()
	c.Spec = "fixture"
	c.BaseURL = server.URL
	c.Wait = 0
	c.Cleanup.Backoff = 0
	d, err := spec.FromMap(lifecycleDoc())
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c, d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	r.Report.Resources = []model.Resource{{ID: "parent", Operation: "POST /widgets", Response: map[string]any{"id": "parent"}, DeleteOperation: "DELETE /widgets/{widgetId}", State: "owned"}, {ID: "child", Operation: "POST /widgets", Response: map[string]any{"id": "child"}, DeleteOperation: "DELETE /widgets/{widgetId}", State: "owned", Parents: []string{"parent"}}}
	if err := r.Cleanup(context.Background()); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	if err := r.Cleanup(context.Background()); err == nil {
		t.Fatal("leftover resources were hidden")
	}
	if calls["/widgets/child"] != 3 || calls["/widgets/parent"] != 0 {
		t.Fatalf("unbounded cleanup or deleted prerequisite: %v", calls)
	}
}
