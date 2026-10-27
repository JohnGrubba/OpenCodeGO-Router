package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonas/opencode-go-router/internal/auth"
	"github.com/jonas/opencode-go-router/internal/config"
	"github.com/jonas/opencode-go-router/internal/models"
	"github.com/jonas/opencode-go-router/internal/proxy"
	"github.com/jonas/opencode-go-router/internal/store"
	"github.com/jonas/opencode-go-router/internal/usage"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, err := auth.HashPassword("test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureAdmin(context.Background(), hash); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{
		APIKey:          "master",
		UpstreamBaseURL: "http://127.0.0.1:1",
		MaxBodyBytes:    1 << 20,
		Version:         "test",
		SessionTTL:      24 * time.Hour,
		RetentionDays:   30,
		Plan:            "go",
		InstanceName:    "Test Router",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr := usage.NewTracker(90 * 86400)
	ms := models.New(cfg, log)
	p := proxy.New(cfg, st, tr, ms, log)
	srv, err := New(cfg, st, tr, ms, p, log)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func client(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func req(t *testing.T, c *http.Client, method, url, body string, csrf bool) *http.Response {
	t.Helper()
	var r *http.Request
	if body == "" {
		r, _ = http.NewRequest(method, url, nil)
	} else {
		r, _ = http.NewRequest(method, url, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		r.Header.Set("X-Requested-With", csrfValue)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func TestLoginFlowAndSecurity(t *testing.T) {
	ts, _ := newTestServer(t)
	c := client(t)

	resp := req(t, c, http.MethodPost, ts.URL+"/api/login", `{"password":"test-password-123"}`, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without csrf = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = req(t, c, http.MethodPost, ts.URL+"/api/login", `{"password":"wrong"}`, true)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = req(t, c, http.MethodPost, ts.URL+"/api/login", `{"password":"test-password-123"}`, true)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login = %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	resp = req(t, c, http.MethodGet, ts.URL+"/api/keys", "", false)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("keys = %d", resp.StatusCode)
	}
	var keys struct {
		Keys []keyView `json:"keys"`
	}
	decode(t, resp, &keys)
	if len(keys.Keys) != 0 {
		t.Fatalf("expected no keys")
	}

	// Missing CSRF on a mutation is rejected.
	resp = req(t, c, http.MethodPost, ts.URL+"/api/keys", `{"name":"x","limits":[]}`, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create without csrf = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = req(t, c, http.MethodPost, ts.URL+"/api/keys", `{"name":"Friend","allowed_models":["kimi-k3"],"limits":[{"metric":"cost","window_seconds":86400,"max":2}]}`, true)
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create = %d %s", resp.StatusCode, b)
	}
	var created struct {
		Secret string  `json:"secret"`
		Key    keyView `json:"key"`
	}
	decode(t, resp, &created)
	if !strings.HasPrefix(created.Secret, auth.KeyPrefix) || created.Key.Name != "Friend" {
		t.Fatalf("bad create response: %+v", created)
	}
	if len(created.Key.Limits) != 1 || created.Key.Limits[0].Max != 2 {
		t.Fatalf("limits not saved: %+v", created.Key.Limits)
	}

	// The secret is not exposed by list/detail endpoints.
	resp = req(t, c, http.MethodGet, ts.URL+"/api/keys", "", false)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), created.Secret) || strings.Contains(string(b), auth.HashSecret(created.Secret)) {
		t.Fatalf("secret leaked in list response")
	}

	// Security headers.
	resp = req(t, c, http.MethodGet, ts.URL+"/", "", false)
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing security headers")
	}
	resp.Body.Close()
}

func TestUnauthenticatedAPI(t *testing.T) {
	ts, _ := newTestServer(t)
	c := client(t)
	resp := req(t, c, http.MethodGet, ts.URL+"/api/keys", "", false)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestOriginCheck(t *testing.T) {
	ts, _ := newTestServer(t)
	c := client(t)
	resp := req(t, c, http.MethodPost, ts.URL+"/api/login", `{"password":"test-password-123"}`, true)
	resp.Body.Close()

	r, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/keys", strings.NewReader(`{"name":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", csrfValue)
	r.Header.Set("Origin", "https://evil.example")
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin = %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPerKeySetupAndModelList(t *testing.T) {
	ts, _ := newTestServer(t)
	c := client(t)
	resp := req(t, c, http.MethodPost, ts.URL+"/api/login", `{"password":"test-password-123"}`, true)
	resp.Body.Close()

	resp = req(t, c, http.MethodPost, ts.URL+"/api/keys", `{"name":"Kimi","allowed_models":["kimi-k3"],"limits":[]}`, true)
	var created struct {
		Secret string  `json:"secret"`
		Key    keyView `json:"key"`
	}
	decode(t, resp, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create failed")
	}

	resp = req(t, c, http.MethodGet, ts.URL+"/api/setup?key="+created.Key.ID, "", false)
	var setup struct {
		Config map[string]any `json:"opencode_json"`
	}
	decode(t, resp, &setup)
	provider := setup.Config["providers"].(map[string]any)["go"].(map[string]any)
	models := provider["models"].(map[string]any)
	if len(models) != 1 {
		t.Fatalf("models = %v", models)
	}
	if _, ok := models["kimi-k3"]; !ok {
		t.Fatalf("kimi missing: %v", models)
	}
	if setup.Config["small_model"] != "go/kimi-k3" {
		t.Fatalf("small_model = %v", setup.Config["small_model"])
	}

	// The key sees only its own models.
	r, _ := http.NewRequest(http.MethodGet, ts.URL+"/go/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+created.Secret)
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "kimi-k3") || strings.Contains(string(body), "glm-5.3") {
		t.Fatalf("model list not filtered: %s", body)
	}

	// An explicitly empty allow-list denies every model.
	resp = req(t, c, http.MethodPost, ts.URL+"/api/keys", `{"name":"Nothing","allowed_models":[],"limits":[]}`, true)
	var empty struct {
		Secret string  `json:"secret"`
		Key    keyView `json:"key"`
	}
	decode(t, resp, &empty)
	r, _ = http.NewRequest(http.MethodPost, ts.URL+"/go/openai/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3","messages":[]}`))
	r.Header.Set("Authorization", "Bearer "+empty.Secret)
	r.Header.Set("Content-Type", "application/json")
	resp, err = c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("empty allow-list status = %d", resp.StatusCode)
	}
	resp.Body.Close()
}
