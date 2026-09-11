package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/engine"
	"github.com/deploymenttheory/go-restapi-inspector/internal/exporter"
	"github.com/deploymenttheory/go-restapi-inspector/internal/generate"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/learn"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/report"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var Version = "dev"
var ErrPartial = errors.New("inspection is partial; see report.json for coverage gaps and remaining resources")

func New() *cobra.Command {
	root := &cobra.Command{Use: "restapi-inspector", Short: "Discover evidence-backed REST API contracts from OpenAPI", Version: Version, SilenceUsage: true, SilenceErrors: true}
	flags := root.PersistentFlags()
	flags.String("config", "", "YAML, JSON or TOML configuration file")
	flags.String("spec", "", "OpenAPI 3.0/3.1 file or URL")
	flags.String("baseline-run", "", "Verified prior run to reuse with plan or inspect; preserves its selection scope")
	flags.String("spec-release", "", "Release label recorded alongside the spec version and hashes")
	flags.String("evidence-context", "", "Principal/tenant configuration revision; change to invalidate previous evidence")
	flags.String("base-url", "", "Explicit target API base URL (use a disposable lab)")
	flags.String("output", "inspector-runs", "Directory for run artifacts")
	flags.Bool("html-report", true, "Generate an offline HTML report with contract artifacts")
	flags.StringSlice("operation", nil, "Operation ID or METHOD /path (repeatable)")
	flags.String("mode", "discovery", "discovery or exhaustive over finite configured domains")
	flags.String("strategy", "active", "active, covering, or unary")
	flags.Int("interaction-order", 3, "Maximum automatically generated rule arity and covering strength")
	flags.Int("validation-trials", 3, "Fresh repetitions required to support a correction")
	flags.Duration("wait-between-requests", config.Defaults().Wait, "Wait from response completion to the next request")
	flags.Duration("request-timeout", config.Defaults().Timeout, "Timeout per request or credential command")
	flags.Int("max-requests", 0, "Optional request limit; cleanup has its own allowance (0 is unlimited)")
	flags.Duration("max-duration", 0, "Optional inspection duration (0 is unlimited)")
	flags.Int("max-planning-tuples", 1_000_000, "Memory guard for candidate, covering and SAT planning")
	flags.Int("boundary-search-steps", config.Defaults().BoundarySteps, "Maximum exponential steps per numeric boundary search (0 disables)")
	flags.Int("confirmation-request-reserve", 0, "Requests reserved for confirmation: 0 estimates, -1 disables")
	flags.StringSlice("sensitive-field", nil, "Additional field names to redact from artifacts")
	flags.Int("cleanup-attempts", 3, "Cleanup attempts per resource")
	flags.Duration("cleanup-backoff", config.Defaults().Cleanup.Backoff, "Initial exponential cleanup backoff")
	flags.Duration("cleanup-timeout", config.Defaults().Cleanup.Timeout, "Independent cleanup deadline")
	flags.String("auth-type", "none", "none, bearer, basic, api-key, oauth2-client-credentials, or exec")
	flags.String("auth-token-env", "", "Environment variable containing bearer token/API key")
	flags.String("auth-username-env", "", "Basic authentication username environment variable")
	flags.String("auth-password-env", "", "Basic authentication password environment variable")
	flags.String("auth-name", "", "API-key header/query/cookie name")
	flags.String("auth-in", "", "API-key location: header, query, cookie")
	flags.String("auth-token-url", "", "OAuth2 token endpoint")
	flags.String("auth-client-id-env", "", "OAuth2 client ID environment variable")
	flags.String("auth-client-secret-env", "", "OAuth2 client secret environment variable")
	flags.String("auth-client-auth-method", "client_secret_basic", "OAuth2 client_secret_basic or client_secret_post")
	flags.Duration("auth-token-refresh-buffer", 30*time.Second, "Refresh cached tokens this long before expiry")
	flags.StringSlice("auth-scope", nil, "OAuth2 scope (repeatable)")
	flags.StringArray("auth-command", nil, "Executable and arguments, repeated in order; no shell")
	flags.String("tls-ca-file", "", "Additional trusted PEM CA file")
	flags.String("tls-cert-file", "", "mTLS client certificate PEM file")
	flags.String("tls-key-file", "", "mTLS private key PEM file")
	flags.String("run", "", "Existing run directory for resume/export/report/explain/cleanup")
	reportCmd := &cobra.Command{Use: "report", Short: "Render an offline HTML report from a saved run", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := runDir(cmd)
		if err != nil {
			return err
		}
		baseline, _ := cmd.Flags().GetString("compare-run")
		path, err := report.Generate(dir, baseline)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), path)
		return nil
	}}
	reportCmd.Flags().String("compare-run", "", "Baseline run directory to compare against; writes comparison.html in --run")
	root.AddCommand(reportCmd)
	root.AddCommand(&cobra.Command{Use: "plan", Short: "Resolve operations and dependencies without probing the API", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := inspectionConfig(cmd)
		if err != nil {
			return err
		}
		if err = c.Validate(false); err != nil {
			return err
		}
		d, err := spec.Load(cmd.Context(), c.Spec)
		if err != nil {
			return err
		}
		incremental, err := engine.PrepareIncremental(c, d)
		if err != nil {
			return err
		}
		c, p := incremental.Config, incremental.Plan
		var inventories []any
		for _, key := range p.Order {
			op, _ := p.Find(key)
			base := generate.Baselines(op, c)[0]
			domains := generate.Domains(op, base, c, true)
			rules, err := learn.Candidates(op, domains, c)
			if err != nil {
				return err
			}
			inventories = append(inventories, map[string]any{"operation": op.Key, "fields": len(op.Fields), "candidates": len(rules), "finiteCombinations": generate.Cardinality(domains), "warnings": op.Warnings})
		}
		return write(cmd, map[string]any{"version": model.Version, "specIdentity": d.Identity(c.SpecRelease), "baseline": incremental.Lineage, "plan": p, "inventory": inventories, "warnings": []string{"Declared schemas guide probes; they are not treated as validation truth.", "Inspect performs real writes for create/update operations and their prerequisites."}})
	}})
	root.AddCommand(&cobra.Command{Use: "inspect", Short: "Probe the API and export its observed contract", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := inspectionConfig(cmd)
		if err != nil {
			return err
		}
		result, runErr := engine.Inspect(cmd.Context(), c, func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), s) })
		return finish(cmd, result, runErr)
	}})
	for _, name := range []string{"resume", "cleanup"} {
		name := name
		root.AddCommand(&cobra.Command{Use: name, Short: map[string]string{"resume": "Reconcile resources and continue incomplete operations", "cleanup": "Retry cleanup of resources recorded in a run"}[name], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := runDir(cmd)
			if err != nil {
				return err
			}
			saved, err := engine.LoadConfig(dir)
			if err != nil {
				return err
			}
			c, err := resolved(cmd, &saved)
			if err != nil {
				return err
			}
			result, runErr := engine.Resume(cmd.Context(), dir, &c, name == "cleanup", func(s string) { fmt.Fprintln(cmd.ErrOrStderr(), s) })
			return finish(cmd, result, runErr)
		}})
	}
	root.AddCommand(&cobra.Command{Use: "export", Short: "Regenerate contract artifacts from a recorded run without API requests", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		dir, err := runDir(cmd)
		if err != nil {
			return err
		}
		saved, observations, _, err := engine.Load(dir)
		if err != nil {
			return err
		}
		c, err := resolved(cmd, &saved.Config)
		if err != nil {
			return err
		}
		saved.Report.HTMLReport = ""
		if c.ReportEnabled() {
			saved.Report.HTMLReport = report.Filename
		}
		d, err := spec.FromMap(saved.Document)
		if err != nil {
			return err
		}
		redactor := journal.NewRedactor(saved.Config.SensitiveFields)
		if saved.Recovered {
			j, err := journal.Open(dir, redactor)
			if err != nil {
				return err
			}
			err = j.Append("document-recovered", map[string]any{"document": saved.Document, "sourceSpecHash": saved.Report.SpecHash})
			_ = j.Close()
			if err != nil {
				return err
			}
		}
		if err = exporter.Save(dir, d, &saved.Report, observations, redactor); err != nil {
			return err
		}
		if err = persistExport(dir, saved.Report, redactor); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), dir+"/"+exporter.ContractName)
		if c.ReportEnabled() {
			return renderReport(cmd, dir)
		}
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "explain [rule-id or operation]", Short: "Show recorded rules and their experimental evidence", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := runDir(cmd)
		if err != nil {
			return err
		}
		saved, observations, _, err := engine.Load(dir)
		if err != nil {
			return err
		}
		id := ""
		if len(args) > 0 {
			id = args[0]
		}
		var rules []model.Rule
		evidence := map[string]bool{}
		for _, rule := range saved.Report.Rules {
			if id == "" || rule.ID == id || rule.Operation == id {
				rules = append(rules, rule)
				for _, eid := range append(append([]string{}, rule.Accepted...), rule.Rejected...) {
					evidence[eid] = true
				}
			}
		}
		if id != "" && len(rules) == 0 {
			return fmt.Errorf("no recorded rule matches %q", id)
		}
		var obs []model.Observation
		for _, o := range observations {
			if evidence[o.ID] {
				obs = append(obs, o)
			}
		}
		return write(cmd, map[string]any{"rules": rules, "evidence": obs, "coverage": saved.Report.Coverage})
	}})
	return root
}

func finish(cmd *cobra.Command, result *engine.Result, runErr error) error {
	if result == nil {
		return runErr
	}
	result.Report.HTMLReport = ""
	if result.HTMLReport {
		result.Report.HTMLReport = report.Filename
	}
	exportErr := exporter.Save(result.Dir, result.Document, &result.Report, result.Observations, result.Redactor)
	if exportErr == nil {
		exportErr = persistExport(result.Dir, result.Report, result.Redactor)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\nState: %s\nContract: %s/%s\n", result.Dir, result.Report.State, result.Dir, exporter.ContractName)
	var reportErr error
	if exportErr == nil && result.HTMLReport {
		reportErr = renderReport(cmd, result.Dir)
	}
	if result.Report.State != "complete" && cmd.Name() != "cleanup" {
		runErr = errors.Join(runErr, ErrPartial)
	}
	return errors.Join(runErr, exportErr, reportErr)
}

func renderReport(cmd *cobra.Command, dir string) error {
	path, err := report.Generate(dir, "")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "HTML report: "+path)
	return nil
}

func persistExport(dir string, report model.Report, redactor *journal.Redactor) error {
	j, err := journal.Open(dir, redactor)
	if err != nil {
		return err
	}
	defer j.Close()
	return j.Append("report", report)
}
func write(cmd *cobra.Command, v any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}
func runDir(cmd *cobra.Command) (string, error) {
	dir, _ := cmd.Flags().GetString("run")
	if dir == "" {
		return "", fmt.Errorf("--run must name an existing run directory")
	}
	return dir, nil
}

func resolved(cmd *cobra.Command, base *config.Config) (config.Config, error) {
	v := viper.New()
	v.SetEnvPrefix("RESTAPI_INSPECTOR")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	defaults := config.Defaults()
	if base != nil {
		defaults = *base
	}
	var values map[string]any
	b, _ := json.Marshal(defaults)
	_ = json.Unmarshal(b, &values)
	var setDefaults func(string, map[string]any)
	setDefaults = func(prefix string, m map[string]any) {
		for key, value := range m {
			full := prefix + key
			if child, ok := value.(map[string]any); ok {
				setDefaults(full+".", child)
			} else {
				v.SetDefault(full, value)
				_ = v.BindEnv(full)
			}
		}
	}
	setDefaults("", values)
	mapping := map[string]string{"operation": "operations", "sensitive-field": "sensitive-fields", "auth-scope": "auth.scopes", "auth-command": "auth.command"}
	cmd.Root().PersistentFlags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "config" || f.Name == "run" {
			return
		}
		key := f.Name
		if mapped, ok := mapping[key]; ok {
			key = mapped
		} else {
			for _, prefix := range []string{"auth-", "cleanup-", "tls-"} {
				if strings.HasPrefix(key, prefix) {
					key = strings.TrimSuffix(prefix, "-") + "." + strings.TrimPrefix(key, prefix)
					break
				}
			}
		}
		_ = v.BindPFlag(key, f)
		_ = v.BindEnv(key)
	})
	file, _ := cmd.Flags().GetString("config")
	if file != "" {
		v.SetConfigFile(file)
		if err := v.ReadInConfig(); err != nil {
			return config.Config{}, err
		}
	}
	var c config.Config
	if err := v.Unmarshal(&c); err != nil {
		return c, fmt.Errorf("configuration: %w", err)
	}
	if err := dynamicConfig(&c, base, file); err != nil {
		return c, err
	}
	_, envSpec := os.LookupEnv("RESTAPI_INSPECTOR_SPEC")
	c.SpecExplicit = cmd.Flags().Changed("spec") || v.InConfig("spec") || envSpec
	return c, nil
}

func inspectionConfig(cmd *cobra.Command) (config.Config, error) {
	c, err := resolved(cmd, nil)
	if err != nil || c.BaselineRun == "" {
		return c, err
	}
	if !c.SpecExplicit {
		return c, fmt.Errorf("incremental inspection requires an explicit --spec, environment value or config entry")
	}
	saved, err := engine.LoadConfig(c.BaselineRun)
	if err != nil {
		return c, err
	}
	// A new revision receives a fresh label; the old release must not leak in.
	saved.SpecRelease = ""
	return resolved(cmd, &saved)
}
