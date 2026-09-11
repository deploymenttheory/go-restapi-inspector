package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/engine"
	"github.com/deploymenttheory/go-restapi-inspector/internal/exporter"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
	"github.com/spf13/cobra"
)

func TestHTMLReportConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name      string
		saved     *bool
		env, file string
		flag      []string
		want      bool
	}{
		{name: "legacy defaults on", want: true},
		{name: "saved off", saved: new(false)},
		{name: "environment off", env: "false"},
		{name: "file off", file: "html-report: false\n"},
		{name: "flag wins", env: "false", flag: []string{"--html-report=true"}, want: true},
		{name: "flag off", flag: []string{"--html-report=false"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := config.Defaults()
			base.HTMLReport = tc.saved
			if tc.env != "" {
				t.Setenv("RESTAPI_INSPECTOR_HTML_REPORT", tc.env)
			}
			root := New()
			var got config.Config
			root.AddCommand(&cobra.Command{Use: "resolve-test", RunE: func(cmd *cobra.Command, _ []string) error { var err error; got, err = resolved(cmd, &base); return err }})
			args := append([]string{"resolve-test"}, tc.flag...)
			if tc.file != "" {
				file := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(file, []byte(tc.file), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--config", file)
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if got.ReportEnabled() != tc.want {
				t.Fatalf("enabled=%v; want %v", got.ReportEnabled(), tc.want)
			}
		})
	}
}

func reportResult(t *testing.T, state string, enabled bool) *engine.Result {
	t.Helper()
	d, err := spec.FromMap(map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "Fixture", "version": "1"}, "paths": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := config.Defaults()
	c.Auth = config.Auth{Type: "exec", Command: []string{"/does/not/exist"}}
	r := &engine.Result{HTMLReport: enabled, Dir: dir, Document: d, Report: model.Report{RunID: "fixture", SpecHash: d.Hash, State: state}, Redactor: journal.NewRedactor(nil)}
	j, err := journal.Open(dir, r.Redactor)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Append("run", engine.Snapshot{Config: c, Document: d.Raw, Report: r.Report}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	return r
}

func TestAutomaticAndExplicitHTMLReporting(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		r := reportResult(t, "partial", enabled)
		var output bytes.Buffer
		cmd := &cobra.Command{Use: "inspect"}
		cmd.SetOut(&output)
		if err := finish(cmd, r, nil); !errors.Is(err, ErrPartial) {
			t.Fatalf("lost partial determination: %v", err)
		}
		_, err := os.Stat(filepath.Join(r.Dir, "report.html"))
		if enabled && err != nil || !enabled && !os.IsNotExist(err) {
			t.Fatalf("automatic generation enabled=%v: %v", enabled, err)
		}
		contract, err := os.ReadFile(filepath.Join(r.Dir, exporter.ContractName))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(contract), "report: report.html") != enabled {
			t.Fatal("incorrect companion link")
		}
		journalBefore, _ := os.ReadFile(filepath.Join(r.Dir, "journal.ndjson"))
		explicit := New()
		explicit.SetOut(&output)
		explicit.SetArgs([]string{"report", "--run", r.Dir, "--html-report=false"})
		if err := explicit.Execute(); err != nil {
			t.Fatalf("offline partial rendering must succeed: %v", err)
		}
		journalAfter, _ := os.ReadFile(filepath.Join(r.Dir, "journal.ndjson"))
		if !bytes.Equal(journalBefore, journalAfter) {
			t.Fatal("explicit report mutated journal")
		}
		if !strings.Contains(output.String(), "report.html") {
			t.Fatal("missing output path")
		}
	}
}

func TestHTMLFailurePreservesContractAndDetermination(t *testing.T) {
	r := reportResult(t, "complete", true)
	if err := os.Mkdir(filepath.Join(r.Dir, "report.html"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{Use: "inspect"}
	cmd.SetOut(&bytes.Buffer{})
	err := finish(cmd, r, nil)
	if err == nil || errors.Is(err, ErrPartial) || r.Report.State != "complete" {
		t.Fatalf("incorrect HTML failure: %v, state %s", err, r.Report.State)
	}
	if _, err := os.Stat(filepath.Join(r.Dir, exporter.ContractName)); err != nil {
		t.Fatal("HTML failure removed contract")
	}
}
