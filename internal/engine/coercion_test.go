package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/exporter"
	"github.com/deploymenttheory/go-restapi-inspector/internal/httpclient"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func scalarRecord(body map[string]any) (map[string]any, bool) {
	name := ""
	switch v := body["name"].(type) {
	case nil:
	case string:
		name = v
	case bool, float64, json.Number:
		b, _ := json.Marshal(v)
		name = string(b)
	default:
		return nil, false
	}
	priority := 0
	switch v := body["priority"].(type) {
	case float64:
		if math.Trunc(v) < 1 || math.Trunc(v) > 20 {
			return nil, false
		}
		priority = int(v)
	case json.Number:
		x, e := v.Float64()
		if e != nil || math.Trunc(x) < 1 || math.Trunc(x) > 20 {
			return nil, false
		}
		priority = int(x)
	case string:
		n, e := strconv.Atoi(strings.TrimSpace(v))
		if e != nil {
			return nil, false
		}
		priority = n
	default:
		return nil, false
	}
	if priority < 1 || priority > 20 {
		return nil, false
	}
	return map[string]any{"name": name, "priority": priority}, true
}

func coercingFixture(t *testing.T) (*httptest.Server, func()) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]map[string]any{}
	created, deleted := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(req.URL.Path, "/records/")
		if req.Method == "GET" || req.Method == "DELETE" {
			object, ok := objects[id]
			if !ok {
				w.WriteHeader(404)
				return
			}
			if req.Method == "GET" {
				_ = json.NewEncoder(w).Encode(object)
			} else {
				delete(objects, id)
				deleted++
				w.WriteHeader(204)
			}
			return
		}
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		object, ok := scalarRecord(body)
		if !ok {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid input"}`))
			return
		}
		if req.Method == "POST" {
			created++
			id = strconv.Itoa(created)
			object["id"] = id
			objects[id] = object
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
			return
		}
		if _, ok := objects[id]; !ok {
			w.WriteHeader(404)
			return
		}
		object["id"] = id
		objects[id] = object
		response := model.Clone(object)
		if body["name"] == nil {
			response["name"] = nil
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	return server, func() {
		mu.Lock()
		defer mu.Unlock()
		if len(objects) != 0 || created != deleted {
			t.Fatalf("fixture leak: created=%d deleted=%d remaining=%d", created, deleted, len(objects))
		}
	}
}

func coercingDocument() map[string]any {
	schema := map[string]any{"type": "object", "required": []any{"name", "priority"}, "properties": map[string]any{
		"id":       map[string]any{"type": "string", "minLength": 1, "readOnly": true, "example": "1"},
		"name":     map[string]any{"type": "string", "example": "fixture"},
		"priority": map[string]any{"type": "integer", "format": "int32", "example": 9},
	}}
	response := func(s map[string]any) map[string]any {
		return map[string]any{"description": "OK", "content": map[string]any{"application/json": map[string]any{"schema": s}}}
	}
	create := map[string]any{"operationId": "createRecord", "requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schema}}}, "responses": map[string]any{"201": response(map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}})}}
	update := model.Clone(create)
	update["operationId"] = "updateRecord"
	update["responses"] = map[string]any{"200": response(schema)}
	return map[string]any{"openapi": "3.0.1", "info": map[string]any{"title": "Scalar fixture", "version": "1"}, "paths": map[string]any{
		"/records":      map[string]any{"post": create},
		"/records/{id}": map[string]any{"parameters": []any{map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string"}}}, "get": map[string]any{"operationId": "readRecord", "responses": map[string]any{"200": response(schema)}}, "put": update, "delete": map[string]any{"operationId": "deleteRecord", "responses": map[string]any{"204": map[string]any{"description": "Deleted"}}}},
	}}
}

func coercingConfig(server *httptest.Server) config.Config {
	c := config.Defaults()
	c.Spec = "fixture"
	c.BaseURL = server.URL
	c.Wait = 0
	c.InteractionOrder = 2
	c.BoundarySteps = 8
	c.Operations = []string{"createRecord", "updateRecord"}
	c.Hints = map[string]config.Hint{"updateRecord": {Values: map[string]any{"body:/name": "updated"}}}
	return c
}

func startFixtureRun(t *testing.T, c config.Config) (*Runner, *spec.Document) {
	t.Helper()
	d, e := spec.FromMap(coercingDocument())
	if e != nil {
		t.Fatal(e)
	}
	r, e := New(c, d, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Journal.Append("run", Snapshot{Config: c, Document: d.Raw, Report: r.Report}); e != nil {
		t.Fatal(e)
	}
	return r, d
}

func TestCoercionBoundariesOmissionAndExport(t *testing.T) {
	server, clean := coercingFixture(t)
	defer server.Close()
	c := coercingConfig(server)
	r, d := startFixtureRun(t, c)
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := r.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = exporter.Save(result.Dir, d, &result.Report, result.Observations, result.Redactor); err != nil {
		t.Fatal(err)
	}
	if result.Report.State != "complete" {
		t.Fatalf("partial result: coverage=%+v changes=%+v", result.Report.Coverage, result.Report.Changes)
	}
	for _, coverage := range result.Report.Coverage {
		if !coverage.ModelConverged || !coverage.InteractionComplete {
			t.Fatalf("missing determination: %+v", coverage)
		}
	}
	foundClear := false
	for _, rule := range result.Report.Rules {
		if rule.Kind == "fieldOmissionClears" && rule.Field == "body:/name" && rule.Status == "supported" {
			foundClear = true
		}
	}
	if !foundClear {
		t.Fatal("update omission was not recorded as clearing")
	}
	raw, _, err := exporter.Build(d, result.Report, result.Observations)
	if err != nil {
		t.Fatal(err)
	}
	out, err := spec.FromMap(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"createRecord", "updateRecord"} {
		op, _ := out.Find(key)
		validator, e := out.Compile(op.Schema)
		if e != nil {
			t.Fatal(e)
		}
		for _, name := range []any{nil, "", true, 3.25, []any{}, map[string]any{}} {
			for _, priority := range []any{nil, 0.0, 0.75, 1.0, 1.25, 19.75, 20.999, 21.0, "0", "1", " +09 ", "020", "21", "1.5", false, []any{}} {
				body := map[string]any{"name": name, "priority": priority}
				_, want := scalarRecord(body)
				if got := validator.Validate(model.Clone(body)) == nil; got != want {
					t.Fatalf("%s schema mismatch for %v: got=%v want=%v", key, body, got, want)
				}
			}
		}
		if validator.Validate(map[string]any{"priority": json.Number("9")}) != nil {
			t.Fatal("accepted omission still rejected by export")
		}
	}
	clean()
	t.Logf("requests=%v resources=%d rules=%d", result.Report.Requests, len(result.Report.Resources), len(result.Report.Rules))
}

func TestResumeReusesClassifiedExperimentsAndConfirmationPairs(t *testing.T) {
	server, clean := coercingFixture(t)
	defer server.Close()
	c := coercingConfig(server)
	c.Operations = []string{"createRecord"}
	c.MaxRequests = 150
	r, _ := startFixtureRun(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	first, err := r.Run(ctx)
	if !errors.Is(err, ErrConfirmationReserve) && !errors.Is(err, httpclient.ErrBudget) {
		t.Fatalf("expected budget interruption, got %v", err)
	}
	if first.Report.State != "partial" {
		t.Fatal("budget interruption was marked complete")
	}
	before := map[string]int{}
	for _, o := range r.Progress["POST /records"].Experiments {
		if o.Phase == "probe" {
			before[model.ID(o.Input)]++
		}
	}
	dir := first.Dir
	r.Close()
	clean()
	c.MaxRequests = 2000
	result, err := Resume(ctx, dir, &c, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.State != "complete" {
		t.Fatalf("resume still partial: %+v", result.Report.Coverage)
	}
	events, err := journal.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	after := map[string]int{}
	epochs := 0
	rechecked := false
	for _, event := range events {
		if event.Kind == "operation-progress" {
			epochs++
		}
		if event.Kind != "experiment" {
			continue
		}
		var o model.Observation
		if err = journal.Decode(event.Data, &o); err != nil {
			t.Fatal(err)
		}
		if o.Phase == "probe" {
			after[model.ID(o.Input)]++
		}
		if o.Phase == "resume-control" {
			rechecked = true
		}
	}
	if epochs != 1 || !rechecked {
		t.Fatalf("progress not reused with fresh control: epochs=%d rechecked=%v", epochs, rechecked)
	}
	for input, count := range before {
		if after[input] != count {
			t.Fatal("replayed a classified probe after resume")
		}
	}
	clean()
}

func TestRedactedExperimentsCannotBeReused(t *testing.T) {
	o := model.Observation{Outcome: "accepted", Input: model.Input{HasBody: true, Body: map[string]any{"name": "[REDACTED]"}}}
	if reusable(o) {
		t.Fatal("redacted request was reusable")
	}
	o.Input.Body = map[string]any{"name": "known"}
	if !reusable(o) {
		t.Fatal("classified plain request was not reusable")
	}
}
