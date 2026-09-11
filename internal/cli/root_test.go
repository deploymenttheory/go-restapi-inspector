package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/spf13/cobra"
)

func TestConfigurationPrecedenceAndEnvironmentOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte("spec: from-file.yaml\nbase-url: https://example.invalid\nwait-between-requests: 3s\nvalidation-trials: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RESTAPI_INSPECTOR_SPEC", "from-env.yaml")
	t.Setenv("RESTAPI_INSPECTOR_WAIT_BETWEEN_REQUESTS", "2s")
	root := New()
	var got config.Config
	root.AddCommand(&cobra.Command{Use: "test-config", RunE: func(cmd *cobra.Command, _ []string) error { var err error; got, err = resolved(cmd, nil); return err }})
	root.SetArgs([]string{"--config", file, "--wait-between-requests", "1s", "test-config"})
	root.SetOut(&bytes.Buffer{})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.Spec != "from-env.yaml" || got.Wait != time.Second || got.ValidationTrials != 2 || got.InteractionOrder != 3 || got.Cleanup.Timeout != 2*time.Minute {
		t.Fatalf("incorrect configuration: %+v", got)
	}
}
func TestResumeRetainsRecordedDefaults(t *testing.T) {
	base := config.Defaults()
	base.Wait = 8 * time.Second
	base.MaxRequests = 32
	root := New()
	var got config.Config
	root.AddCommand(&cobra.Command{Use: "test-config", RunE: func(cmd *cobra.Command, _ []string) error { var err error; got, err = resolved(cmd, &base); return err }})
	root.SetArgs([]string{"test-config"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.Wait != base.Wait || got.MaxRequests != 32 {
		t.Fatalf("resume replaced stored defaults: %+v", got)
	}
}

func TestOAuthSettingsFromConfigurationAndFlags(t *testing.T) {
	file := filepath.Join(t.TempDir(), "auth.yaml")
	if err := os.WriteFile(file, []byte("auth:\n  client-auth-method: client_secret_post\n  token-refresh-buffer: 300s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, override := range []bool{false, true} {
		root := New()
		var got config.Config
		root.AddCommand(&cobra.Command{Use: "test-config", RunE: func(cmd *cobra.Command, _ []string) error { var err error; got, err = resolved(cmd, nil); return err }})
		args := []string{"--config", file, "test-config"}
		want := 300 * time.Second
		if override {
			args = append(args, "--auth-token-refresh-buffer", "90s")
			want = 90 * time.Second
		}
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if got.Auth.ClientAuthMethod != "client_secret_post" || got.Auth.TokenRefreshBuffer != want {
			t.Fatalf("incorrect OAuth configuration: %+v", got.Auth)
		}
	}
}

func TestConfigurationPreservesAPINames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("hints:\n  createWidget:\n    values:\n      body:/displayName: Widget\n    auth: LabBearer\nauth-profiles:\n  LabBearer:\n    type: bearer\n    token-env: LAB_TOKEN\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	root := New()
	var got config.Config
	root.AddCommand(&cobra.Command{Use: "test-config", RunE: func(cmd *cobra.Command, _ []string) error { var err error; got, err = resolved(cmd, nil); return err }})
	root.SetArgs([]string{"--config", file, "test-config"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.Hints["createWidget"].Values["body:/displayName"] != "Widget" || got.AuthProfiles["LabBearer"].TokenEnv != "LAB_TOKEN" {
		t.Fatalf("case-sensitive API identifiers were lost: %+v", got)
	}
}
