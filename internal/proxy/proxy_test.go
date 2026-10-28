package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonas/opencode-go-router/internal/auth"
	"github.com/jonas/opencode-go-router/internal/config"
	"github.com/jonas/opencode-go-router/internal/models"
	"github.com/jonas/opencode-go-router/internal/store"
	"github.com/jonas/opencode-go-router/internal/usage"
)

type fakeUpstream struct {
	mu       sync.Mutex
	lastAuth string
	lastKey  string
	lastGoog string
	lastSess string
	lastBody string
	status   int
}

func (f *fakeUpstream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/go/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"kimi-k3"},{"id":"glm-5.3"},{"id":"gpt-6-luna"}]}`)
	})
	mux.HandleFunc("/go/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.lastKey = r.Header.Get("X-Api-Key")
		f.lastGoog = r.Header.Get("X-Goog-Api-Key")
		f.lastSess = r.Header.Get("X-Opencode-Session")
		f.lastBody = string(b)
		status := f.status
		f.mu.Unlock()
		if status != 0 && status != 200 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"upstream says no","type":"invalid_request_error"}}`)
			return
		}
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			fmt.Fprint(w, "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"opencode\":{\"allowance\":0.01,\"tokens\":{\"input\":10,\"output\":5}}}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2},"opencode":{"allowance":0.01,"list_cost":0.01,"billing_source":"go","tokens":{"input":8,"output":5,"cache_read":2}}}}`)
	})
	return mux
}

func testProxy(t *testing.T) (*Proxy, *store.Store, *fakeUpstream) {
	t.Helper()
	up := &fakeUpstream{}
	upstream := httptest.NewServer(up.handler())
	t.Cleanup(upstream.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Config{
		APIKey:          "master-key",
		UpstreamBaseURL: upstream.URL,
		MaxBodyBytes:    1 << 20,
		Version:         "test",
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr := usage.NewTracker(30 * 86400)
	ms := models.New(cfg, log)
	p := New(cfg, st, tr, ms, log)
	return p, st, up
}

func makeKey(t *testing.T, st *store.Store, allowed []string, limits []store.Limit) string {
	t.Helper()
	secret, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateKey(context.Background(), store.Key{
		ID: "key_" + prefix, Name: "Tester", Prefix: prefix, Hash: hash,
		Enabled: true, AllowedModels: allowed, CreatedAt: time.Now().Unix(), Limits: limits,
	}); err != nil {
		t.Fatal(err)
	}
	return secret
}

func do(p http.Handler, method, path, secret string, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		r.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}

func TestProxyChatAndAccounting(t *testing.T) {
	p, st, up := testProxy(t)
	secret := makeKey(t, st, []string{"*"}, nil)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret,
		`{"model":"kimi-k3","messages":[{"role":"user","content":"hello there"}]}`,
		map[string]string{"X-Opencode-Session": "ses_test123"})
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"hi"`) {
		t.Fatalf("unexpected body %s", w.Body.String())
	}
	if up.lastAuth != "Bearer master-key" || up.lastKey != "master-key" || up.lastGoog != "master-key" {
		t.Fatalf("upstream auth headers wrong: %q %q %q", up.lastAuth, up.lastKey, up.lastGoog)
	}
	if up.lastSess != "ses_test123" {
		t.Fatalf("session header = %q", up.lastSess)
	}
	rows, err := st.Requests(context.Background(), store.RequestFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.Allowance != 0.01 || r.Input != 8 || r.Output != 5 || r.CacheRead != 2 || r.Status != 200 {
		t.Fatalf("unexpected recorded request: %+v", r)
	}
}

func TestProxyDerivesSession(t *testing.T) {
	p, st, up := testProxy(t)
	secret := makeKey(t, st, []string{"*"}, nil)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret,
		`{"model":"kimi-k3","messages":[{"role":"system","content":"sys"},{"role":"user","content":"first question"}]}`, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.HasPrefix(up.lastSess, "ses_") || len(up.lastSess) != 28 {
		t.Fatalf("derived session = %q", up.lastSess)
	}
}

func TestProxyModelAllowlist(t *testing.T) {
	p, st, _ := testProxy(t)
	secret := makeKey(t, st, []string{"glm-5.3"}, nil)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret,
		`{"model":"kimi-k3","messages":[]}`, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	w = do(p, http.MethodGet, "/go/v1/models", secret, "", nil)
	if w.Code != 200 {
		t.Fatalf("models status = %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "kimi-k3") || !strings.Contains(w.Body.String(), "glm-5.3") {
		t.Fatalf("models not filtered: %s", w.Body.String())
	}
}

func TestFormatMoneyKeepsSubCentPrecision(t *testing.T) {
	cases := map[float64]string{
		0:                    "0.00",
		0.00014:              "0.00014",
		0.0000014:            "0.0000014",
		0.005:                "0.005",
		0.1 + 0.2:            "0.30",
		1:                    "1.00",
		12.3456789012345678:  "12.345678901235",
		123.4567890123456789: "123.456789012346",
	}
	for in, want := range cases {
		if got := formatMoney(in); got != want {
			t.Errorf("formatMoney(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatLimitPhraseKeepsSubCentPrecision(t *testing.T) {
	got := formatLimitPhrase("cost", 0.00014, 0.005)
	want := "$0.00014 of $0.005 spend"
	if got != want {
		t.Fatalf("formatLimitPhrase = %q, want %q", got, want)
	}
}

func TestProxyCostLimit(t *testing.T) {
	p, st, _ := testProxy(t)
	secret := makeKey(t, st, []string{"*"}, []store.Limit{{Metric: "cost", WindowSeconds: 3600, Max: 0.005}})
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret,
		`{"model":"kimi-k3","messages":[]}`, nil)
	if w.Code != 200 {
		t.Fatalf("first status = %d", w.Code)
	}
	w = do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret,
		`{"model":"kimi-k3","messages":[]}`, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, body = %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	var payload map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &payload)
	if payload["error"] == nil {
		t.Fatalf("missing error payload: %s", w.Body.String())
	}
}

func TestProxyStreaming(t *testing.T) {
	p, st, up := testProxy(t)
	secret := makeKey(t, st, []string{"*"}, nil)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret,
		`{"model":"kimi-k3","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "data: [DONE]") {
		t.Fatalf("stream not passed through: %s", w.Body.String())
	}
	if !strings.Contains(up.lastBody, `"stream_options":{"include_usage":true}`) {
		t.Fatalf("include_usage was not added: %s", up.lastBody)
	}
	rows, _ := st.Requests(context.Background(), store.RequestFilter{Limit: 10})
	if len(rows) != 1 || rows[0].Allowance != 0.01 || rows[0].Input != 10 || rows[0].Output != 5 {
		t.Fatalf("stream accounting wrong: %+v", rows)
	}
}

func TestProxyInvalidKey(t *testing.T) {
	p, _, _ := testProxy(t)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", "ocgr_wrong", `{"model":"kimi-k3","messages":[]}`, nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestProxyUpstreamError(t *testing.T) {
	p, st, up := testProxy(t)
	up.status = http.StatusBadRequest
	secret := makeKey(t, st, []string{"*"}, nil)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret, `{"model":"kimi-k3","messages":[]}`, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "upstream says no") {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	rows, _ := st.Requests(context.Background(), store.RequestFilter{Limit: 10})
	if len(rows) != 1 || rows[0].Status != http.StatusBadRequest {
		t.Fatalf("error request not recorded: %+v", rows)
	}
}

func TestProxyMissingModel(t *testing.T) {
	p, st, _ := testProxy(t)
	secret := makeKey(t, st, []string{"*"}, nil)
	w := do(p, http.MethodPost, "/go/openai/v1/chat/completions", secret, `{"messages":[]}`, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Missing model") {
		t.Fatalf("missing model error not returned: %s", w.Body.String())
	}
}
