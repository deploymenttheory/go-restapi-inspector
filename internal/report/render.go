package report

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"path/filepath"

	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
)

//go:embed templates/*.gohtml assets/*
var assets embed.FS

// Generate writes an offline report without changing either journal or contract.
func Generate(dir, baselineDir string) (string, error) {
	current, cfg, err := Load(dir)
	if err != nil {
		return "", fmt.Errorf("load report: %w", err)
	}
	v := View{Version: 1, Current: current}
	name := Filename
	if baselineDir != "" {
		baseline, previousConfig, err := Load(baselineDir)
		if err != nil {
			return "", fmt.Errorf("load comparison baseline: %w", err)
		}
		comparison := Compare(current, baseline, cfg, previousConfig)
		v.Baseline, v.Comparison = &baseline, &comparison
		name = ComparisonFilename
	}
	b, err := Render(v)
	if err != nil {
		return "", err
	}
	if err = journal.WriteFile(dir, name, b); err != nil {
		return "", fmt.Errorf("write HTML report: %w", err)
	}
	return filepath.Join(dir, name), nil
}

// Render embeds a versioned view and shared components in one portable document.
func Render(v View) ([]byte, error) {
	t, err := template.ParseFS(assets, "templates/*.gohtml")
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	css, err := assets.ReadFile("assets/report.css")
	if err != nil {
		return nil, err
	}
	js, err := assets.ReadFile("assets/report.js")
	if err != nil {
		return nil, err
	}
	// Only compiled assets are trusted code. Recorded values travel as base64
	// data and are inserted by the browser through text nodes, never HTML.
	page := struct {
		View View
		Data string
		CSS  template.CSS
		JS   template.JS
	}{v, base64.StdEncoding.EncodeToString(payload), template.CSS(css), template.JS(js)}
	var output bytes.Buffer
	if err = t.ExecuteTemplate(&output, "page", page); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
