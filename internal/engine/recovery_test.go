package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

func TestObservationContextNormalizesEmptyContainers(t *testing.T) {
	c := config.Defaults()
	want := observationSignature(c, "source", "POST /records")
	c.AuthProfiles = map[string]config.Auth{}
	c.Security = map[string]string{}
	c.Hints = map[string]config.Hint{}
	c.SensitiveFields = []string{}
	if got := observationSignature(c, "source", "POST /records"); got != want {
		t.Fatal("empty containers changed the observation context")
	}
	c.Auth.Type = "bearer"
	if got := observationSignature(c, "source", "POST /records"); got == want {
		t.Fatal("authentication changes did not change the observation context")
	}
}

func TestLoadReplacesReportAndConfigurationSnapshots(t *testing.T) {
	dir := t.TempDir()
	c := config.Defaults()
	c.Spec = "fixture"
	c.Hints = map[string]config.Hint{"POST /widgets": {Role: "action"}}
	old := model.Report{RunID: "replacement", State: "partial", Coverage: []model.Coverage{{Operation: "POST /widgets", Reasons: []string{"old gap"}}}, Rules: []model.Rule{{ID: "rule", When: &model.Predicate{Op: "present", Field: "body:/mode"}, Value: "old value"}}}
	latest := model.Report{RunID: "replacement", State: "complete", Coverage: []model.Coverage{{Operation: "POST /widgets", State: "complete"}}, Rules: []model.Rule{{ID: "rule", Assert: model.Predicate{Op: "present", Field: "body:/name"}}}}
	j, err := journal.Open(dir, journal.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append("run", Snapshot{Config: c, Document: document(widgetSchema()), Report: old}); err != nil {
		t.Fatal(err)
	}
	if err := j.Append("report", latest); err != nil {
		t.Fatal(err)
	}
	c.Hints = nil
	if err := j.Append("resumed", map[string]any{"config": c}); err != nil {
		t.Fatal(err)
	}
	j.Close()
	saved, _, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if model.ID(saved.Report) != model.ID(latest) {
		t.Fatalf("stale fields survived report replacement: %+v", saved.Report)
	}
	settings, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hints) != 0 || len(saved.Config.Hints) != 0 {
		t.Fatal("removed hints survived configuration replacement")
	}
}

func TestLoadRecoversDamagedMetadataOnlyFromMatchingSource(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching", true: "changed"}[changed], func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "source.json")
			raw := document(widgetSchema())
			b, _ := json.Marshal(raw)
			if err := os.WriteFile(source, b, 0600); err != nil {
				t.Fatal(err)
			}
			d, err := spec.Load(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			c := config.Defaults()
			c.Spec = source
			broken := model.Clone(d.Raw)
			broken["paths"].(map[string]any)["/oauth/token"] = map[string]any{"$ref": "[REDACTED]"}
			j, err := journal.Open(dir, journal.NewRedactor(nil))
			if err != nil {
				t.Fatal(err)
			}
			if err = j.Append("run", Snapshot{Config: c, Document: broken, Report: model.Report{RunID: "recovery", SpecHash: d.Hash}}); err != nil {
				t.Fatal(err)
			}
			j.Close()
			if changed {
				if err = os.WriteFile(source, append(b, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
			saved, _, _, err := Load(dir)
			if changed {
				if err == nil || !strings.Contains(err.Error(), "source hash differs") {
					t.Fatalf("changed source accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !saved.Recovered || damagedMetadata(saved.Document) || model.ID(saved.Document) != model.ID(d.Raw) {
				t.Fatal("original source metadata was not recovered")
			}
			if saved.documentModel == nil || saved.plan == nil {
				t.Fatal("resume would need to rebuild the prepared graph")
			}
			j, err = journal.Open(dir, journal.NewRedactor(nil))
			if err != nil {
				t.Fatal(err)
			}
			if err = j.Append("document-recovered", map[string]any{"document": saved.Document, "sourceSpecHash": d.Hash}); err != nil {
				t.Fatal(err)
			}
			c.MaxRequests = 1234
			if err = j.Append("resumed", map[string]any{"config": c}); err != nil {
				t.Fatal(err)
			}
			j.Close()
			if err = os.Remove(source); err != nil {
				t.Fatal(err)
			}
			saved, _, _, err = Load(dir)
			if err != nil || saved.Recovered || damagedMetadata(saved.Document) {
				t.Fatalf("persisted recovery failed: %v", err)
			}
			latest, err := LoadConfig(dir)
			if err != nil || latest.MaxRequests != 1234 {
				t.Fatalf("latest resume configuration lost: %+v %v", latest, err)
			}
		})
	}
}
