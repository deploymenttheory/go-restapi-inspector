package cli

import (
	"bytes"
	"os"
	"testing"
)

func TestLiveLab(t *testing.T) {
	if os.Getenv("INSPECTOR_LIVE") != "1" {
		t.Skip("opt-in live lab test")
	}
	file := os.Getenv("INSPECTOR_LIVE_CONFIG")
	if file == "" {
		t.Fatal("INSPECTOR_LIVE_CONFIG is required")
	}
	cmd := New()
	cmd.SetArgs([]string{"inspect", "--config", file, "--output", t.TempDir()})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("live inspection: %v\n%s", err, out.String())
	}
	t.Log(out.String())
}
