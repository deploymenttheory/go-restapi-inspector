// Package httpclient owns authentication, serialization, pacing, and HTTP
// classification. It never retries an ambiguous mutation automatically.
package httpclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
	"github.com/deploymenttheory/go-restapi-inspector/internal/spec"
)

var ErrBudget = errors.New("configured request limit reached")

type Credentials struct {
	Headers   map[string]string `json:"headers"`
	Query     map[string]string `json:"query"`
	Cookies   map[string]string `json:"cookies"`
	ExpiresAt time.Time         `json:"expiresAt"`
}
type Client struct {
	Config      config.Config
	HTTP        *http.Client
	Journal     *journal.Journal
	Redactor    *journal.Redactor
	mu          sync.Mutex
	next        time.Time
	counts      map[string]int
	credentials map[string]Credentials
	sequence    int
}

func New(c config.Config, j *journal.Journal, r *journal.Redactor) (*Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.TLS.CAFile != "" {
		b, e := os.ReadFile(c.TLS.CAFile)
		if e != nil {
			return nil, e
		}
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("ca-file contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if c.TLS.CertFile != "" || c.TLS.KeyFile != "" {
		cert, e := tls.LoadX509KeyPair(c.TLS.CertFile, c.TLS.KeyFile)
		if e != nil {
			return nil, e
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	h := &http.Client{Transport: transport, Timeout: c.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{Config: c, HTTP: h, Journal: j, Redactor: r, counts: map[string]int{}, credentials: map[string]Credentials{}}, nil
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }
func (c *Client) Counts() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.Clone(c.counts)
}
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.credentials = map[string]Credentials{}
}
func (c *Client) dispatch(ctx context.Context, req *http.Request, phase string) (*http.Response, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for k, n := range c.counts {
		if k != "cleanup" {
			total += n
		}
	}
	if phase != "cleanup" && c.Config.MaxRequests > 0 && total >= c.Config.MaxRequests {
		return nil, false, ErrBudget
	}
	if e := Sleep(ctx, time.Until(c.next)); e != nil {
		return nil, false, e
	}
	c.counts[phase]++
	if c.Journal != nil {
		if err := c.Journal.Append("request", map[string]any{"phase": phase, "method": req.Method, "host": req.URL.Host, "requestId": model.RequestID(ctx), "experimentId": model.ExperimentID(ctx)}); err != nil {
			return nil, false, err
		}
	}
	res, e := c.HTTP.Do(req)
	if res != nil && e == nil {
		body, readErr := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
		_ = res.Body.Close()
		res.Body = io.NopCloser(bytes.NewReader(body))
		if readErr != nil {
			e = readErr
		}
	}
	c.next = time.Now().Add(c.Config.Wait)
	if res != nil {
		if retry := RetryAfter(res.Header.Get("Retry-After"), time.Now()); retry.After(c.next) {
			c.next = retry
		}
	}
	return res, true, e
}

func (c *Client) RestoreCounts(counts map[string]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = model.Clone(counts)
	if c.counts == nil {
		c.counts = map[string]int{}
	}
}
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func RetryAfter(s string, now time.Time) time.Time {
	if n, e := strconv.Atoi(s); e == nil && n >= 0 {
		return now.Add(time.Duration(n) * time.Second)
	}
	t, _ := http.ParseTime(s)
	return t
}

func (c *Client) Do(ctx context.Context, op model.Operation, in model.Input, phase, control, contextID string) (model.Observation, error) {
	c.mu.Lock()
	c.sequence++
	sequence := c.sequence
	c.mu.Unlock()
	obs := model.Observation{ID: fmt.Sprintf("e-%d-%06d", time.Now().UnixNano(), sequence), Operation: op.Key, Phase: phase, Input: model.Clone(in), Started: time.Now().UTC(), Control: control, Context: contextID, Outcome: "inconclusive"}
	obs.ExperimentID = model.ExperimentID(ctx)
	req, e := c.Request(ctx, op, in)
	if e != nil {
		return obs, e
	}
	if e = c.applyAuth(ctx, req, op, phase); e != nil {
		obs.Reason = "authentication: " + e.Error()
		return obs, c.record(obs)
	}
	if c.Journal != nil {
		intent := map[string]any{"id": obs.ID, "operation": op.Key, "method": op.Method, "input": in, "phase": phase, "context": contextID, "experimentId": obs.ExperimentID}
		if e = c.Journal.Append("intent", intent); e != nil {
			return obs, e
		}
	}
	res, sent, e := c.dispatch(model.WithRequest(ctx, obs.ID), req, phase)
	obs.Sent = sent
	obs.Duration = time.Since(obs.Started)
	if e != nil {
		obs.Reason = e.Error()
		recordErr := c.record(obs)
		return obs, errors.Join(e, recordErr)
	}
	defer res.Body.Close()
	obs.Status = res.StatusCode
	obs.Headers = res.Header.Clone()
	b, e := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if e != nil {
		obs.Reason = "read response: " + e.Error()
	} else if len(b) > 8<<20 {
		obs.Reason = "response exceeds 8 MiB; outcome cannot be established"
	} else {
		if len(bytes.TrimSpace(b)) > 0 {
			if e = journal.Decode(b, &obs.Response); e != nil {
				obs.Response = string(b)
			}
		}
		oracle := c.Config.Oracle
		if h := c.Config.Hint(op); h.Oracle != nil {
			oracle = *h.Oracle
		}
		obs.Outcome, obs.Reason = Classify(res.StatusCode, obs.Response, oracle)
	}
	if obs.Status == 401 {
		c.Invalidate()
	}
	return obs, c.record(obs)
}
func (c *Client) record(obs model.Observation) error {
	if c.Journal != nil {
		return c.Journal.Append("observation", obs)
	}
	return nil
}

func Classify(status int, body any, o config.Oracle) (string, string) {
	// Environmental statuses never become field evidence, even if a broad body
	// predicate happens to match their error envelope.
	if status == 401 || status == 403 || status == 404 || status == 409 || status == 412 || status == 429 || status >= 500 {
		return "inconclusive", fmt.Sprintf("HTTP %d requires environmental/state interpretation", status)
	}
	if status == 202 {
		return "inconclusive", "asynchronous result requires completion polling"
	}
	for _, m := range o.Reject {
		if v, ok := model.Get(body, m.Pointer); ok && model.Equal(v, m.Equals) {
			return "input-rejected", "configured response predicate"
		}
	}
	for _, s := range o.RejectStatuses {
		if status == s {
			return "input-rejected", fmt.Sprintf("configured input-rejection status %d", status)
		}
	}
	if status >= 200 && status < 300 {
		if len(o.Accept) > 0 {
			for _, m := range o.Accept {
				if v, ok := model.Get(body, m.Pointer); ok && model.Equal(v, m.Equals) {
					return "accepted", "configured acceptance predicate"
				}
			}
			return "inconclusive", "response did not satisfy configured acceptance predicates"
		}
		if b := spec.Map(body); b != nil {
			if success, ok := b["success"].(bool); ok && !success {
				return "inconclusive", "application reports success=false; configure a rejection predicate"
			}
			if v, ok := b["error"]; ok && v != nil && v != "" && v != false {
				return "inconclusive", "application error envelope needs an explicit oracle"
			}
		}
		return "accepted", "successful HTTP response"
	}
	return "inconclusive", fmt.Sprintf("unclassified HTTP %d", status)
}

func (c *Client) Request(ctx context.Context, op model.Operation, in model.Input) (*http.Request, error) {
	base, e := url.Parse(c.Config.BaseURL)
	if e != nil {
		return nil, e
	}
	path := op.Path
	q := url.Values{}
	headers := http.Header{}
	for _, f := range op.Fields {
		if f.In == "body" {
			continue
		}
		v, ok := in.Get(f.ID)
		if !ok {
			if f.In == "path" {
				return nil, fmt.Errorf("unbound path parameter %s", f.ID)
			}
			continue
		}
		switch f.In {
		case "path":
			s, e := pathValue(f, v)
			if e != nil {
				return nil, e
			}
			path = strings.ReplaceAll(path, "{"+f.Name+"}", s)
		case "query":
			if e = queryValue(q, f, v); e != nil {
				return nil, e
			}
		case "header":
			headers.Set(f.Name, simple(v, f.Explode, ","))
		case "cookie":
			headers.Add("Cookie", f.Name+"="+url.QueryEscape(scalar(v)))
		default:
			return nil, fmt.Errorf("unsupported parameter location %q", f.In)
		}
	}
	if strings.Contains(path, "{") {
		return nil, fmt.Errorf("unbound path in %s", op.Key)
	}
	// Keep an explicitly supplied base path and encoded path values intact.
	address := strings.TrimRight(base.String(), "/") + "/" + strings.TrimLeft(path, "/")
	if len(q) > 0 {
		address += "?" + q.Encode()
	}
	var body io.Reader
	if in.HasBody {
		if !spec.JSONMedia(op.MediaType) {
			return nil, fmt.Errorf("unsupported body media type %q", op.MediaType)
		}
		b, e := json.Marshal(in.Body)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
		headers.Set("Content-Type", op.MediaType)
	}
	req, e := http.NewRequestWithContext(ctx, op.Method, address, body)
	if e != nil {
		return nil, e
	}
	req.Header = headers
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "go-restapi-inspector/0.1")
	return req, nil
}

func scalar(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func simple(v any, explode bool, delimiter string) string {
	var parts []string
	switch x := v.(type) {
	case []any:
		for _, v := range x {
			parts = append(parts, scalar(v))
		}
	case map[string]any:
		for _, k := range spec.Keys(x) {
			if explode {
				parts = append(parts, k+"="+scalar(x[k]))
			} else {
				parts = append(parts, k, scalar(x[k]))
			}
		}
	default:
		return scalar(v)
	}
	return strings.Join(parts, delimiter)
}
func pathValue(f model.Field, v any) (string, error) {
	delimiter := ","
	prefix := ""
	switch f.Style {
	case "simple", "":
	case "label":
		prefix = "."
		if f.Explode {
			delimiter = "."
		}
	case "matrix":
		prefix = ";" + f.Name + "="
	default:
		return "", fmt.Errorf("unsupported path style %s", f.Style)
	}
	// Escape values individually so separators remain serialization syntax.
	if f.Style == "matrix" && f.Explode {
		switch x := v.(type) {
		case []any:
			var b strings.Builder
			for _, v := range x {
				b.WriteString(";" + f.Name + "=" + url.PathEscape(scalar(v)))
			}
			return b.String(), nil
		case map[string]any:
			var b strings.Builder
			for _, k := range spec.Keys(x) {
				b.WriteString(";" + url.PathEscape(k) + "=" + url.PathEscape(scalar(x[k])))
			}
			return b.String(), nil
		}
	}
	var parts []string
	switch x := v.(type) {
	case []any:
		for _, v := range x {
			parts = append(parts, url.PathEscape(scalar(v)))
		}
	case map[string]any:
		for _, k := range spec.Keys(x) {
			if f.Explode {
				parts = append(parts, url.PathEscape(k)+"="+url.PathEscape(scalar(x[k])))
			} else {
				parts = append(parts, url.PathEscape(k), url.PathEscape(scalar(x[k])))
			}
		}
	default:
		return prefix + url.PathEscape(scalar(v)), nil
	}
	return prefix + strings.Join(parts, delimiter), nil
}
func queryValue(q url.Values, f model.Field, v any) error {
	switch f.Style {
	case "deepObject":
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("deepObject %s needs an object", f.ID)
		}
		for _, k := range spec.Keys(m) {
			q.Add(f.Name+"["+k+"]", scalar(m[k]))
		}
	case "form", "":
		switch x := v.(type) {
		case []any:
			if f.Explode {
				for _, v := range x {
					q.Add(f.Name, scalar(v))
				}
			} else {
				q.Add(f.Name, simple(v, false, ","))
			}
		case map[string]any:
			if f.Explode {
				for _, k := range spec.Keys(x) {
					q.Add(k, scalar(x[k]))
				}
			} else {
				q.Add(f.Name, simple(v, false, ","))
			}
		default:
			q.Add(f.Name, scalar(v))
		}
	case "spaceDelimited":
		q.Add(f.Name, simple(v, false, " "))
	case "pipeDelimited":
		q.Add(f.Name, simple(v, false, "|"))
	default:
		return fmt.Errorf("unsupported query style %s", f.Style)
	}
	return nil
}

func (c *Client) applyAuth(ctx context.Context, req *http.Request, op model.Operation, phase string) error {
	profiles := []config.Auth{c.Config.Auth}
	h := c.Config.Hint(op)
	if h.Auth != "" {
		a, ok := c.Config.AuthProfiles[h.Auth]
		if !ok {
			return fmt.Errorf("unknown auth profile %s", h.Auth)
		}
		profiles = []config.Auth{a}
	} else if len(c.Config.Security) > 0 {
		matched := false
		for _, alternative := range spec.Slice(op.Raw["security"]) {
			var group []config.Auth
			all := true
			for _, scheme := range spec.Keys(spec.Map(alternative)) {
				name, ok := c.Config.Security[scheme]
				a, exists := c.Config.AuthProfiles[name]
				if !ok || !exists {
					all = false
					break
				}
				group = append(group, a)
			}
			if all {
				profiles = group
				matched = true
				break
			}
		}
		if !matched && len(spec.Slice(op.Raw["security"])) > 0 {
			return fmt.Errorf("no configured security alternative satisfies operation")
		}
	}
	for _, profile := range profiles {
		cred, e := c.resolveAuth(ctx, profile, op, phase)
		if e != nil {
			return e
		}
		for k, v := range cred.Headers {
			req.Header.Set(k, v)
		}
		q := req.URL.Query()
		for k, v := range cred.Query {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
		for k, v := range cred.Cookies {
			req.AddCookie(&http.Cookie{Name: k, Value: v})
		}
	}
	return nil
}
func env(name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return "", fmt.Errorf("credential environment variable %s is empty or unset", name)
	}
	return v, nil
}
func (c *Client) resolveAuth(ctx context.Context, a config.Auth, op model.Operation, phase string) (Credentials, error) {
	key := model.ID(a)
	if a.Type == "exec" {
		key = model.ID(a, op.Key, c.Config.BaseURL)
	}
	c.mu.Lock()
	cached, ok := c.credentials[key]
	c.mu.Unlock()
	buffer := a.TokenRefreshBuffer
	if buffer == 0 {
		buffer = 30 * time.Second
	}
	if ok && (cached.ExpiresAt.IsZero() || time.Until(cached.ExpiresAt) > buffer) {
		return cached, nil
	}
	cred := Credentials{Headers: map[string]string{}, Query: map[string]string{}, Cookies: map[string]string{}}
	get := func(name string) (string, error) {
		v, e := env(name)
		if e == nil {
			c.Redactor.Secret(v)
		}
		return v, e
	}
	switch a.Type {
	case "", "none":
		return cred, nil
	case "bearer", "api-key":
		v, e := get(a.TokenEnv)
		if e != nil {
			return cred, e
		}
		if a.Type == "bearer" {
			cred.Headers["Authorization"] = "Bearer " + v
		} else {
			switch a.In {
			case "header":
				cred.Headers[a.Name] = v
			case "query":
				cred.Query[a.Name] = v
			case "cookie":
				cred.Cookies[a.Name] = v
			}
		}
	case "basic":
		u, e := get(a.UsernameEnv)
		if e != nil {
			return cred, e
		}
		p, e := get(a.PasswordEnv)
		if e != nil {
			return cred, e
		}
		cred.Headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(u+":"+p))
	case "oauth2-client-credentials":
		id, e := get(a.ClientIDEnv)
		if e != nil {
			return cred, e
		}
		secret, e := get(a.ClientSecretEnv)
		if e != nil {
			return cred, e
		}
		form := url.Values{"grant_type": {"client_credentials"}}
		if a.ClientAuthMethod == "client_secret_post" {
			form.Set("client_id", id)
			form.Set("client_secret", secret)
		}
		if len(a.Scopes) > 0 {
			form.Set("scope", strings.Join(a.Scopes, " "))
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
		if e != nil {
			return cred, e
		}
		if a.ClientAuthMethod != "client_secret_post" {
			req.SetBasicAuth(id, secret)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		authPhase := "auth"
		if phase == "cleanup" {
			authPhase = "cleanup"
		}
		res, _, e := c.dispatch(ctx, req, authPhase)
		if e != nil {
			return cred, e
		}
		defer res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return cred, fmt.Errorf("token endpoint returned HTTP %d", res.StatusCode)
		}
		var result struct {
			Token   string      `json:"access_token"`
			Expires json.Number `json:"expires_in"`
			Type    string      `json:"token_type"`
		}
		d := json.NewDecoder(io.LimitReader(res.Body, 1<<20))
		d.UseNumber()
		if e = d.Decode(&result); e != nil {
			return cred, fmt.Errorf("invalid token response: %w", e)
		}
		if result.Token == "" {
			return cred, fmt.Errorf("token endpoint returned no access_token")
		}
		if result.Type != "" && !strings.EqualFold(result.Type, "bearer") {
			return cred, fmt.Errorf("unsupported token_type %s", result.Type)
		}
		c.Redactor.Secret(result.Token)
		cred.Headers["Authorization"] = "Bearer " + result.Token
		secs, _ := strconv.ParseFloat(result.Expires.String(), 64)
		if secs <= 0 {
			secs = 60
		}
		cred.ExpiresAt = time.Now().Add(time.Duration(secs * float64(time.Second)))
	case "exec":
		child, cancel := context.WithTimeout(ctx, c.Config.Timeout)
		defer cancel()
		cmd := exec.CommandContext(child, a.Command[0], a.Command[1:]...)
		data, _ := json.Marshal(map[string]any{"version": 1, "operation": op.Key, "baseUrl": c.Config.BaseURL, "scopes": a.Scopes})
		cmd.Stdin = bytes.NewReader(data)
		var output limitedBuffer
		cmd.Stdout = &output
		if e := cmd.Run(); e != nil {
			return cred, fmt.Errorf("credential provider failed: %w", e)
		}
		if e := journal.Decode(output.Bytes(), &cred); e != nil {
			return cred, fmt.Errorf("invalid credential-provider JSON: %w", e)
		}
		if cred.ExpiresAt.IsZero() {
			cred.ExpiresAt = time.Now().Add(time.Minute)
		}
	default:
		return cred, fmt.Errorf("unsupported auth type %s", a.Type)
	}
	for _, v := range cred.Headers {
		c.Redactor.Secret(v)
	}
	for _, v := range cred.Query {
		c.Redactor.Secret(v)
	}
	for _, v := range cred.Cookies {
		c.Redactor.Secret(v)
	}
	c.mu.Lock()
	c.credentials[key] = cred
	c.mu.Unlock()
	return cred, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("credential-provider output exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}
