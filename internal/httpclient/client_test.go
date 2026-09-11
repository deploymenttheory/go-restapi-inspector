package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/deploymenttheory/go-restapi-inspector/internal/config"
	"github.com/deploymenttheory/go-restapi-inspector/internal/journal"
	"github.com/deploymenttheory/go-restapi-inspector/internal/model"
)

func TestOracleDistinguishesEnvironmentAndApplicationErrors(t *testing.T) {
	oracle := config.Defaults().Oracle
	oracle.Reject = []config.Match{{Pointer: "/success", Equals: false}}
	for _, status := range []int{401, 403, 404, 409, 412, 429, 500, 503, 202} {
		outcome, _ := Classify(status, map[string]any{"success": false}, oracle)
		if outcome != "inconclusive" {
			t.Errorf("HTTP %d became %s", status, outcome)
		}
	}
	if outcome, _ := Classify(200, map[string]any{"success": false}, oracle); outcome != "input-rejected" {
		t.Fatal("HTTP 200 application rejection was ignored")
	}
	if outcome, _ := Classify(200, map[string]any{"error": "bad input"}, config.Defaults().Oracle); outcome != "inconclusive" {
		t.Fatal("unknown application error was accepted")
	}
}
func TestPacingStartsAfterBodyCompletion(t *testing.T) {
	var mu sync.Mutex
	var completed time.Time
	var gap time.Duration
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 2 {
			gap = time.Since(completed)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
		completed = time.Now()
	}))
	defer server.Close()
	c := config.Defaults()
	c.BaseURL = server.URL
	c.Wait = 40 * time.Millisecond
	client, err := New(c, nil, journal.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	op := model.Operation{Key: "GET /", Method: "GET", Path: "/"}
	for range 2 {
		if _, err := client.Do(context.Background(), op, model.Input{}, "probe", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if gap < 35*time.Millisecond {
		t.Fatalf("pacing started before response completion: %v", gap)
	}
}
func TestOAuthRefreshAndHeaderQuerySerialization(t *testing.T) {
	var mu sync.Mutex
	tokens := 0
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			tokens++
			_, _ = fmt.Fprintf(w, `{"access_token":"fixture-token-%d","expires_in":3600}`, tokens)
			return
		}
		auth = r.Header.Get("Authorization")
		if r.URL.Query().Get("filter[name]") != "hello world" {
			t.Error("incorrect deepObject serialization")
		}
		if tokens == 1 {
			w.WriteHeader(401)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	t.Setenv("FIXTURE_ID", "client-id")
	t.Setenv("FIXTURE_SECRET", "client-secret-value")
	c := config.Defaults()
	c.BaseURL = server.URL
	c.Wait = 0
	c.Auth = config.Auth{Type: "oauth2-client-credentials", TokenURL: server.URL + "/token", ClientIDEnv: "FIXTURE_ID", ClientSecretEnv: "FIXTURE_SECRET"}
	client, err := New(c, nil, journal.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	op := model.Operation{Key: "GET /data", Method: "GET", Path: "/data", Fields: []model.Field{{ID: "query:filter", In: "query", Name: "filter", Style: "deepObject"}}}
	in := model.Input{Parameters: map[string]any{"query:filter": map[string]any{"name": "hello world"}}}
	first, err := client.Do(context.Background(), op, in, "probe", "", "")
	if err != nil || first.Status != 401 {
		t.Fatalf("unexpected first request %v %v", first, err)
	}
	second, err := client.Do(context.Background(), op, in, "probe", "", "")
	if err != nil || second.Outcome != "accepted" {
		t.Fatalf("refresh failed: %v %v", second, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if tokens != 2 || auth != "Bearer fixture-token-2" {
		t.Fatalf("token was not refreshed: %d %s", tokens, auth)
	}
}
func TestOAuthClientAuthenticationAndRefreshBuffer(t *testing.T) {
	for _, method := range []string{"client_secret_basic", "client_secret_post"} {
		t.Run(method, func(t *testing.T) {
			t.Setenv("FIXTURE_ID", "fixture-id")
			t.Setenv("FIXTURE_SECRET", "fixture-secret")
			tokens := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Method != http.MethodPost || r.PostForm.Get("grant_type") != "client_credentials" {
					t.Error("incorrect token request")
				}
				if method == "client_secret_post" {
					if r.Header.Get("Authorization") != "" || r.PostForm.Get("client_id") != "fixture-id" || r.PostForm.Get("client_secret") != "fixture-secret" {
						t.Error("incorrect form client authentication")
					}
				} else {
					id, secret, ok := r.BasicAuth()
					if !ok || id != "fixture-id" || secret != "fixture-secret" || r.PostForm.Has("client_secret") || r.PostForm.Has("client_id") {
						t.Error("incorrect Basic client authentication")
					}
				}
				tokens++
				_, _ = fmt.Fprintf(w, `{"access_token":"fixture-%d","expires_in":3600,"token_type":"Bearer"}`, tokens)
			}))
			defer server.Close()
			c := config.Defaults()
			c.Wait = 0
			c.Auth = config.Auth{Type: "oauth2-client-credentials", TokenURL: server.URL, ClientIDEnv: "FIXTURE_ID", ClientSecretEnv: "FIXTURE_SECRET", ClientAuthMethod: method, TokenRefreshBuffer: 300 * time.Second}
			client, err := New(c, nil, journal.NewRedactor(nil))
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			op := model.Operation{Key: "GET /data"}
			cred, err := client.resolveAuth(context.Background(), c.Auth, op, "probe")
			if err != nil {
				t.Fatal(err)
			}
			cred.ExpiresAt = time.Now().Add(310 * time.Second)
			client.credentials[model.ID(c.Auth)] = cred
			if _, err = client.resolveAuth(context.Background(), c.Auth, op, "probe"); err != nil {
				t.Fatal(err)
			}
			if client.Counts()["auth"] != 1 {
				t.Fatal("token refreshed outside the buffer")
			}
			cred.ExpiresAt = time.Now().Add(290 * time.Second)
			client.credentials[model.ID(c.Auth)] = cred
			if _, err = client.resolveAuth(context.Background(), c.Auth, op, "probe"); err != nil {
				t.Fatal(err)
			}
			if client.Counts()["auth"] != 2 {
				t.Fatal("token not refreshed inside the buffer")
			}
		})
	}
}

func TestRequestBudgetLeavesCleanupAllowance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	c := config.Defaults()
	c.BaseURL = server.URL
	c.Wait = 0
	c.MaxRequests = 1
	client, err := New(c, nil, journal.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	op := model.Operation{Key: "DELETE /x", Method: "DELETE", Path: "/x"}
	if _, err := client.Do(context.Background(), op, model.Input{}, "probe", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(context.Background(), op, model.Input{}, "probe", "", ""); !errors.Is(err, ErrBudget) {
		t.Fatalf("missing request limit: %v", err)
	}
	if _, err := client.Do(context.Background(), op, model.Input{}, "cleanup", "", ""); err != nil {
		t.Fatalf("cleanup lost its allowance: %v", err)
	}
}
