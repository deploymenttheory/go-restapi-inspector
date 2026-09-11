package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/engine"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/report"
)

func TestIncrementalCLIAndPortableReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	dir := t.TempDir()
	source := filepath.Join(dir, "spec.json")
	data := []byte(`{"openapi":"3.1.0","info":{"title":"Test","version":"1"},"paths":{"/a":{"get":{"operationId":"readA","responses":{"204":{"description":"OK"}}}},"/b":{"get":{"responses":{"204":{"description":"OK"}}}}}}`)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	c := config.Defaults()
	c.Spec = source
	c.BaseURL = server.URL
	c.Output = dir
	c.Wait = 0
	c.Operations = []string{"readA"}
	baseline, err := engine.Inspect(context.Background(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"plan", "inspect"} {
		root := New()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs([]string{name, "--baseline-run", baseline.Dir, "--spec", source, "--spec-release", "trial-v2"})
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out.String())
		}
		if name == "plan" {
			var plan struct {
				Baseline     model.Lineage
				SpecIdentity model.SpecIdentity
			}
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			if plan.SpecIdentity.Release != "trial-v2" {
				t.Fatal("release flag lost")
			}
			for _, delta := range plan.Baseline.Operations {
				if delta.Operation == "GET /a" && delta.Action != "inherit" {
					t.Fatal("plan lost completed evidence")
				}
				if delta.Operation == "GET /b" && delta.Selected {
					t.Fatal("plan expanded saved selection")
				}
			}
			continue
		}
		var runDir string
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(line, "Run: ") {
				runDir = strings.TrimPrefix(line, "Run: ")
			}
		}
		if runDir == "" {
			t.Fatalf("missing run path: %s", out.String())
		}
		view, _, err := report.Load(runDir)
		if err != nil {
			t.Fatal(err)
		}
		if view.Baseline == nil || view.Metrics.HTTP != 0 || len(view.Evidence) == 0 || view.SpecIdentity.Release != "trial-v2" {
			t.Fatalf("invalid portable inherited report: %+v", view.Metrics)
		}
		for _, e := range view.Evidence {
			if e.Origin == nil || e.Origin.RunID != baseline.Report.RunID {
				t.Fatal("inherited origin unavailable in report")
			}
		}
		if _, err := os.Stat(filepath.Join(runDir, "report.html")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(runDir, "baseline-journal.ndjson")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExplicitResumeSpecFromFlagFileAndEnvironment(t *testing.T) {
	saved := config.Defaults()
	saved.Spec = "saved.json"
	for _, source := range []string{"saved", "flag", "file", "environment"} {
		t.Run(source, func(t *testing.T) {
			root := New()
			args := []string{"resume", "--run", "unused"}
			switch source {
			case "flag":
				args = append(args, "--spec", "saved.json")
			case "file":
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte("spec: saved.json\n"), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--config", path)
			case "environment":
				t.Setenv("RESTAPI_INSPECTOR_SPEC", "saved.json")
			}
			cmd, _, err := root.Find(args)
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.ParseFlags(args[1:]); err != nil {
				t.Fatal(err)
			}
			c, err := resolved(cmd, &saved)
			if err != nil {
				t.Fatal(err)
			}
			if c.SpecExplicit != (source != "saved") {
				t.Fatalf("explicit source detection: %+v", c)
			}
		})
	}
}
