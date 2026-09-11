package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

type Config struct {
	HTMLReport          *bool                    `mapstructure:"html-report" json:"html-report,omitempty"`
	Spec                string                   `mapstructure:"spec" json:"spec"`
	BaseURL             string                   `mapstructure:"base-url" json:"base-url"`
	Output              string                   `mapstructure:"output" json:"output"`
	Operations          []string                 `mapstructure:"operations" json:"operations,omitempty"`
	Mode                string                   `mapstructure:"mode" json:"mode"`
	Strategy            string                   `mapstructure:"strategy" json:"strategy"`
	InteractionOrder    int                      `mapstructure:"interaction-order" json:"interaction-order"`
	ValidationTrials    int                      `mapstructure:"validation-trials" json:"validation-trials"`
	Wait                time.Duration            `mapstructure:"wait-between-requests" json:"wait-between-requests"`
	Timeout             time.Duration            `mapstructure:"request-timeout" json:"request-timeout"`
	MaxRequests         int                      `mapstructure:"max-requests" json:"max-requests"`
	MaxDuration         time.Duration            `mapstructure:"max-duration" json:"max-duration"`
	MaxPlanningTuples   int                      `mapstructure:"max-planning-tuples" json:"max-planning-tuples"`
	BoundarySteps       int                      `mapstructure:"boundary-search-steps" json:"boundary-search-steps"`
	ConfirmationReserve int                      `mapstructure:"confirmation-request-reserve" json:"confirmation-request-reserve"`
	Auth                Auth                     `mapstructure:"auth" json:"auth"`
	AuthProfiles        map[string]Auth          `mapstructure:"auth-profiles" json:"auth-profiles,omitempty"`
	Security            map[string]string        `mapstructure:"security" json:"security,omitempty"`
	Hints               map[string]Hint          `mapstructure:"hints" json:"hints,omitempty"`
	Rules               []model.Rule             `mapstructure:"rules" json:"rules,omitempty"`
	Domains             map[string][]model.State `mapstructure:"domains" json:"domains,omitempty"`
	SensitiveFields     []string                 `mapstructure:"sensitive-fields" json:"sensitive-fields,omitempty"`
	Cleanup             Cleanup                  `mapstructure:"cleanup" json:"cleanup"`
	Oracle              Oracle                   `mapstructure:"oracle" json:"oracle"`
	TLS                 TLS                      `mapstructure:"tls" json:"tls"`
}

type Auth struct {
	Type               string        `mapstructure:"type" json:"type"`
	TokenEnv           string        `mapstructure:"token-env" json:"token-env,omitempty"`
	UsernameEnv        string        `mapstructure:"username-env" json:"username-env,omitempty"`
	PasswordEnv        string        `mapstructure:"password-env" json:"password-env,omitempty"`
	Name               string        `mapstructure:"name" json:"name,omitempty"`
	In                 string        `mapstructure:"in" json:"in,omitempty"`
	TokenURL           string        `mapstructure:"token-url" json:"token-url,omitempty"`
	ClientIDEnv        string        `mapstructure:"client-id-env" json:"client-id-env,omitempty"`
	ClientSecretEnv    string        `mapstructure:"client-secret-env" json:"client-secret-env,omitempty"`
	ClientAuthMethod   string        `mapstructure:"client-auth-method" json:"client-auth-method,omitempty"`
	TokenRefreshBuffer time.Duration `mapstructure:"token-refresh-buffer" json:"token-refresh-buffer,omitempty"`
	Scopes             []string      `mapstructure:"scopes" json:"scopes,omitempty"`
	Command            []string      `mapstructure:"command" json:"command,omitempty"`
}

type Binding struct {
	Operation string `mapstructure:"operation" json:"operation"`
	Pointer   string `mapstructure:"pointer" json:"pointer"`
	Source    string `mapstructure:"source" json:"source,omitempty"`
}

type Hint struct {
	ValueEnvs       map[string]string  `mapstructure:"value-envs" json:"value-envs,omitempty"`
	Role            string             `mapstructure:"role" json:"role,omitempty"`
	Read            string             `mapstructure:"read" json:"read,omitempty"`
	Delete          string             `mapstructure:"delete" json:"delete,omitempty"`
	Auth            string             `mapstructure:"auth" json:"auth,omitempty"`
	Values          map[string]any     `mapstructure:"values" json:"values,omitempty"`
	Bindings        map[string]Binding `mapstructure:"bindings" json:"bindings,omitempty"`
	Oracle          *Oracle            `mapstructure:"oracle" json:"oracle,omitempty"`
	Poll            *Poll              `mapstructure:"poll" json:"poll,omitempty"`
	Observe         map[string]string  `mapstructure:"observe" json:"observe,omitempty"`
	MonotonicFields []string           `mapstructure:"monotonic-fields" json:"monotonic-fields,omitempty"`
}

type Poll struct {
	Operation    string            `mapstructure:"operation" json:"operation"`
	Bindings     map[string]string `mapstructure:"bindings" json:"bindings"`
	StatePointer string            `mapstructure:"state-pointer" json:"state-pointer"`
	Success      []any             `mapstructure:"success" json:"success"`
	Failure      []any             `mapstructure:"failure" json:"failure"`
	Timeout      time.Duration     `mapstructure:"timeout" json:"timeout"`
}

type Cleanup struct {
	Attempts int           `mapstructure:"attempts" json:"attempts"`
	Backoff  time.Duration `mapstructure:"backoff" json:"backoff"`
	Timeout  time.Duration `mapstructure:"timeout" json:"timeout"`
}

type Match struct {
	Pointer string `mapstructure:"pointer" json:"pointer"`
	Equals  any    `mapstructure:"equals" json:"equals"`
}

type Oracle struct {
	Accept         []Match `mapstructure:"accept" json:"accept,omitempty"`
	Reject         []Match `mapstructure:"reject" json:"reject,omitempty"`
	RejectStatuses []int   `mapstructure:"reject-statuses" json:"reject-statuses,omitempty"`
}

type TLS struct {
	CAFile   string `mapstructure:"ca-file" json:"ca-file,omitempty"`
	CertFile string `mapstructure:"cert-file" json:"cert-file,omitempty"`
	KeyFile  string `mapstructure:"key-file" json:"key-file,omitempty"`
}

func Defaults() Config {
	return Config{
		Output: "inspector-runs", Mode: "discovery", Strategy: "active", InteractionOrder: 3, ValidationTrials: 3,
		Wait: time.Second, Timeout: 30 * time.Second, MaxPlanningTuples: 1_000_000, BoundarySteps: 16,
		Cleanup: Cleanup{Attempts: 3, Backoff: time.Second, Timeout: 2 * time.Minute},
		Oracle:  Oracle{RejectStatuses: []int{400, 422}},
	}
}

func (c Config) ReportEnabled() bool { return c.HTMLReport == nil || *c.HTMLReport }

func (c Config) Hint(op model.Operation) Hint {
	h, ok := c.Hints[op.Key]
	if !ok {
		h = c.Hints[op.ID]
	}
	h.Values = model.Clone(h.Values)
	if h.Values == nil {
		h.Values = map[string]any{}
	}
	for field, name := range h.ValueEnvs {
		if value, ok := os.LookupEnv(name); ok {
			h.Values[field] = value
		}
	}
	return h
}
func (c Config) Validate(live bool) error {
	if c.Spec == "" {
		return fmt.Errorf("spec must be supplied through a flag, environment variable, or config file")
	}
	if live {
		u, e := url.Parse(c.BaseURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("base-url must be an explicit HTTP(S) lab URL without credentials, query, or fragment")
		}
	}
	if c.Mode != "discovery" && c.Mode != "exhaustive" {
		return fmt.Errorf("mode must be discovery or exhaustive")
	}
	if c.Strategy != "active" && c.Strategy != "covering" && c.Strategy != "unary" {
		return fmt.Errorf("strategy must be active, covering, or unary")
	}
	if c.InteractionOrder < 1 || c.ValidationTrials < 1 || c.Timeout <= 0 || c.Wait < 0 || c.MaxRequests < 0 || c.MaxDuration < 0 || c.MaxPlanningTuples < 1 {
		return fmt.Errorf("invalid discovery or transport limits")
	}
	if c.Cleanup.Attempts < 1 || c.Cleanup.Backoff < 0 || c.Cleanup.Timeout <= 0 {
		return fmt.Errorf("cleanup needs positive attempts and timeout, and nonnegative backoff")
	}
	if c.BoundarySteps < 0 || c.BoundarySteps > 30 {
		return fmt.Errorf("boundary-search-steps must be between 0 and 30")
	}
	if c.ConfirmationReserve < -1 {
		return fmt.Errorf("confirmation-request-reserve must be -1, zero, or positive")
	}
	if e := c.Auth.Validate(); e != nil {
		return e
	}
	for name, a := range c.AuthProfiles {
		if e := a.Validate(); e != nil {
			return fmt.Errorf("auth profile %s: %w", name, e)
		}
	}
	for scheme, name := range c.Security {
		if _, ok := c.AuthProfiles[name]; !ok {
			return fmt.Errorf("security scheme %s references unknown auth profile %s", scheme, name)
		}
	}
	for name, h := range c.Hints {
		switch h.Role {
		case "", "create", "update", "delete", "read", "action":
		default:
			return fmt.Errorf("hint %s has unsupported role %q", name, h.Role)
		}
		if h.Auth != "" {
			if _, ok := c.AuthProfiles[h.Auth]; !ok {
				return fmt.Errorf("hint %s references unknown auth profile %s", name, h.Auth)
			}
		}
		if h.Poll != nil && (h.Poll.Operation == "" || len(h.Poll.Success) == 0 || h.Poll.Timeout < 0) {
			return fmt.Errorf("hint %s polling needs an operation, success states and a nonnegative timeout", name)
		}
		if live {
			for field, env := range h.ValueEnvs {
				if _, ok := os.LookupEnv(env); !ok {
					return fmt.Errorf("hint %s %s requires environment variable %s", name, field, env)
				}
			}
			for field, value := range h.Values {
				if s, ok := value.(string); ok && strings.Contains(s, "[REDACTED]") {
					return fmt.Errorf("hint %s %s was redacted; supply its value through the original config or value-envs when resuming", name, field)
				}
			}
		}
	}
	return nil
}
func (a Auth) Validate() error {
	if a.TokenRefreshBuffer < 0 {
		return fmt.Errorf("token-refresh-buffer must be nonnegative")
	}
	if a.ClientAuthMethod != "" && a.ClientAuthMethod != "client_secret_basic" && a.ClientAuthMethod != "client_secret_post" {
		return fmt.Errorf("client-auth-method must be client_secret_basic or client_secret_post")
	}
	switch a.Type {
	case "", "none":
	case "bearer":
		if a.TokenEnv == "" {
			return fmt.Errorf("bearer auth requires token-env")
		}
	case "basic":
		if a.UsernameEnv == "" || a.PasswordEnv == "" {
			return fmt.Errorf("basic auth requires username-env and password-env")
		}
	case "api-key":
		if a.TokenEnv == "" || a.Name == "" || (a.In != "header" && a.In != "query" && a.In != "cookie") {
			return fmt.Errorf("api-key auth requires token-env, name, and in: header/query/cookie")
		}
	case "oauth2-client-credentials":
		if a.TokenURL == "" || a.ClientIDEnv == "" || a.ClientSecretEnv == "" {
			return fmt.Errorf("OAuth2 requires token-url, client-id-env, and client-secret-env")
		}
		u, err := url.Parse(a.TokenURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return fmt.Errorf("token-url must be an HTTP(S) URL without embedded credentials")
		}
	case "exec":
		if len(a.Command) == 0 {
			return fmt.Errorf("exec auth requires a command argument list")
		}
	default:
		return fmt.Errorf("unsupported auth type %q", a.Type)
	}
	return nil
}
