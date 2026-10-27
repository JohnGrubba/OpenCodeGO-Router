package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonas/opencode-go-router/internal/auth"
	"github.com/jonas/opencode-go-router/internal/config"
	"github.com/jonas/opencode-go-router/internal/models"
	"github.com/jonas/opencode-go-router/internal/store"
	"github.com/jonas/opencode-go-router/internal/usage"
)

type Proxy struct {
	cfg     config.Config
	store   *store.Store
	tracker *usage.Tracker
	models  *models.Service
	log     *slog.Logger
	client  *http.Client

	touchMu sync.Mutex
	touched map[string]int64
}

func New(cfg config.Config, st *store.Store, tracker *usage.Tracker, ms *models.Service, log *slog.Logger) *Proxy {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		ExpectContinueTimeout: 1 * time.Second,
		WriteBufferSize:       32 << 10,
		ReadBufferSize:        32 << 10,
	}
	return &Proxy{
		cfg:     cfg,
		store:   st,
		tracker: tracker,
		models:  ms,
		log:     log,
		client:  &http.Client{Transport: transport},
		touched: make(map[string]int64),
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	canonical, ok := CanonicalPath(r.URL.Path)
	if !ok {
		writeError(w, "chat", http.StatusNotFound, "Unknown API path", "not_found")
		return
	}
	if p.cfg.APIKey == "" {
		writeError(w, familyForPath(canonical), http.StatusServiceUnavailable, "Upstream API key is not configured", "configuration_error")
		return
	}

	secret := tokenFromRequest(r)
	if secret == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="opencode-go-router"`)
		writeError(w, familyForPath(canonical), http.StatusUnauthorized, "Missing API key. Use Authorization: Bearer ocgr_... or x-api-key.", "authentication_error")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	key, err := p.store.GetKeyByHash(ctx, auth.HashSecret(secret))
	cancel()
	if errors.Is(err, store.ErrNotFound) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="opencode-go-router"`)
		writeError(w, familyForPath(canonical), http.StatusUnauthorized, "Invalid API key.", "authentication_error")
		return
	}
	if err != nil {
		p.log.Error("key lookup failed", "error", err)
		writeError(w, familyForPath(canonical), http.StatusInternalServerError, "Internal error.", "internal_error")
		return
	}
	now := time.Now().Unix()
	if !key.Enabled {
		writeError(w, familyForPath(canonical), http.StatusForbidden, "This API key is disabled.", "permission_error")
		return
	}
	if key.ExpiresAt > 0 && now >= key.ExpiresAt {
		writeError(w, familyForPath(canonical), http.StatusForbidden, "This API key has expired.", "permission_error")
		return
	}

	// Model catalog routes are served locally so a key only sees the models it
	// is allowed to use.
	if isModelsList(canonical, r.Method) {
		p.serveModels(w, r, key)
		return
	}

	family := familyForPath(canonical)
	body, err := p.readRequestBody(r)
	if err != nil {
		writeError(w, family, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	model, modelErr := extractModel(canonical, body)
	if modelErr != nil {
		writeError(w, family, http.StatusBadRequest, modelErr.Error(), "invalid_request_error")
		return
	}
	if model != "" && !ModelAllowed(key.AllowedModels, model) {
		writeError(w, family, http.StatusForbidden, fmt.Sprintf("Model %q is not allowed for this API key.", model), "permission_error")
		return
	}
	if model == "" {
		fields := []any{
			"endpoint", canonical,
			"method", r.Method,
			"body_bytes", len(body),
			"content_type", r.Header.Get("Content-Type"),
			"content_encoding", r.Header.Get("Content-Encoding"),
			"session", r.Header.Get("X-Opencode-Session"),
		}
		if len(body) > 0 {
			var payload map[string]json.RawMessage
			if json.Unmarshal(body, &payload) == nil {
				keys := make([]string, 0, len(payload))
				for k := range payload {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				fields = append(fields, "json_keys", keys)
			} else {
				fields = append(fields, "json", "invalid")
			}
		}
		p.log.Warn("proxied request has no model", fields...)
	}
	if model == "" {
		writeError(w, family, http.StatusBadRequest, "Missing model in request body. Use the final https:// base URL in opencode.jsonc: an HTTP-to-HTTPS redirect turns POST into GET and drops the body.", "invalid_request_error")
		return
	}

	statuses, blocked := p.tracker.Check(key.ID, key.Limits, now)
	if blocked {
		var hit *usage.LimitStatus
		for i := range statuses {
			if statuses[i].Used+1e-9 >= statuses[i].Max {
				hit = &statuses[i]
				break
			}
		}
		retry := int64(60)
		if hit != nil && hit.ResetAt > now {
			retry = hit.ResetAt - now
		}
		w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
		if hit != nil {
			w.Header().Set("X-Go-Router-Limit-Metric", hit.Metric)
			w.Header().Set("X-Go-Router-Limit-Window", strconv.FormatInt(hit.WindowSeconds, 10))
			w.Header().Set("X-Go-Router-Limit-Max", strconv.FormatFloat(hit.Max, 'f', 4, 64))
			w.Header().Set("X-Go-Router-Limit-Used", strconv.FormatFloat(hit.Used, 'f', 4, 64))
		}
		p.log.Info("request blocked by limit", "key", key.Prefix, "model", model, "limit", hit)
		msg := "Usage limit exceeded for this API key."
		if hit != nil {
			msg = fmt.Sprintf("Usage limit exceeded: %s per %s. Try again after %s.",
				formatLimitPhrase(hit.Metric, hit.Used, hit.Max), formatWindow(hit.WindowSeconds), time.Unix(hit.ResetAt, 0).UTC().Format(time.RFC3339))
		}
		writeError(w, family, http.StatusTooManyRequests, msg, "usage_limit_exceeded")
		return
	}

	requestID := r.Header.Get("X-Request-Id")
	if requestID == "" {
		requestID = auth.NewRequestID()
	}
	w.Header().Set("X-Go-Router-Request-Id", requestID)
	session := sessionFromRequest(r, body, key.ID)

	start := time.Now()
	upstream, err := p.forward(r, canonical, body, requestID, key.ID, session)
	if err != nil {
		p.log.Warn("upstream request failed", "request_id", requestID, "key", key.Prefix, "model", model, "error", err)
		writeError(w, family, http.StatusBadGateway, "Upstream request failed: "+err.Error(), "upstream_error")
		p.record(r, key, canonical, family, model, session, http.StatusBadGateway, nil, err.Error(), start, false)
		return
	}
	defer upstream.Body.Close()

	encoding := strings.ToLower(strings.TrimSpace(upstream.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		msg := "Upstream returned an unsupported content encoding: " + encoding
		writeError(w, family, http.StatusBadGateway, msg, "upstream_error")
		p.record(r, key, canonical, family, model, session, http.StatusBadGateway, nil, msg, start, false)
		return
	}

	stream := strings.Contains(strings.ToLower(upstream.Header.Get("Content-Type")), "text/event-stream")
	copyResponseHeaders(w.Header(), upstream.Header)
	if stream {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(upstream.StatusCode)

	bodyReader := io.Reader(upstream.Body)
	switch encoding {
	case "", "identity":
	case "gzip":
		zr, zerr := gzip.NewReader(upstream.Body)
		if zerr != nil {
			p.log.Warn("could not decode gzip upstream response", "request_id", requestID, "error", zerr)
		} else {
			defer zr.Close()
			bodyReader = zr
		}
	}

	var captured usage.Usage
	var copyErr error
	if stream {
		captured, copyErr = p.copySSE(w, bodyReader, family)
	} else {
		captured, copyErr = p.copyJSON(w, bodyReader, family, upstream.StatusCode)
	}
	if copyErr != nil {
		p.log.Debug("response copy interrupted", "request_id", requestID, "error", copyErr)
	}

	meta, _ := p.models.Lookup(model)
	captured.FinalizeAt(family, meta, start)
	if upstream.StatusCode >= 400 {
		captured.Cost = 0
		captured.Allowance = 0
	}
	elapsed := time.Since(start)
	p.record(r, key, canonical, family, model, session, upstream.StatusCode, &captured, "", start, stream)
	p.log.Info("proxied request",
		"request_id", requestID,
		"key", key.Prefix,
		"endpoint", canonical,
		"method", r.Method,
		"body_bytes", len(body),
		"model", model,
		"family", family,
		"status", upstream.StatusCode,
		"stream", stream,
		"duration_ms", elapsed.Milliseconds(),
		"cost", captured.Cost,
	)
}

func (p *Proxy) forward(r *http.Request, canonical string, body []byte, requestID, keyID, session string) (*http.Response, error) {
	body = ensureStreamUsage(body, canonical)
	q := r.URL.Query()
	q.Del("key")
	target := p.cfg.UpstreamBaseURL + canonical
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyRequestHeaders(req.Header, r.Header)
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	req.Header.Set("X-Api-Key", p.cfg.APIKey)
	req.Header.Set("X-Goog-Api-Key", p.cfg.APIKey)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("X-Request-Id", requestID)
	req.Header.Del("Cookie")
	req.Header.Del("Content-Encoding")

	if session != "" {
		req.Header.Set("X-Opencode-Session", session)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "opencode-go-router/"+p.cfg.Version)
	}
	return p.client.Do(req)
}

func (p *Proxy) copyJSON(w http.ResponseWriter, body io.Reader, family string, status int) (usage.Usage, error) {
	data, err := io.ReadAll(io.LimitReader(body, p.cfg.MaxBodyBytes+1))
	if err != nil {
		return usage.Usage{}, err
	}
	parseData := data
	if int64(len(data)) > p.cfg.MaxBodyBytes {
		parseData = data[:p.cfg.MaxBodyBytes]
	}
	if len(data) > 0 {
		if _, werr := w.Write(data); werr != nil {
			return usage.Extract(parseData, family), werr
		}
	}
	if int64(len(data)) > p.cfg.MaxBodyBytes {
		if _, werr := io.Copy(w, body); werr != nil {
			return usage.Extract(parseData, family), werr
		}
	}
	u := usage.Extract(parseData, family)
	if status >= 400 {
		u.Found = false
	}
	return u, nil
}

func (p *Proxy) copySSE(w http.ResponseWriter, body io.Reader, family string) (usage.Usage, error) {
	collector := usage.NewCollector(family)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			collector.Feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return collector.Usage(), werr
			}
			_ = rc.Flush()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return collector.Usage(), nil
			}
			return collector.Usage(), err
		}
	}
}

func (p *Proxy) readRequestBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, p.cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read request body")
	}
	if int64(len(data)) > p.cfg.MaxBodyBytes {
		return nil, fmt.Errorf("request body too large (limit %d MB)", p.cfg.MaxBodyBytes>>20)
	}
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	switch enc {
	case "", "identity":
		return data, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip request body")
		}
		defer zr.Close()
		out, err := io.ReadAll(io.LimitReader(zr, p.cfg.MaxBodyBytes))
		if err != nil {
			return nil, fmt.Errorf("invalid gzip request body")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", enc)
	}
}

func (p *Proxy) record(r *http.Request, key store.Key, endpoint, family, model, session string, status int, u *usage.Usage, errMsg string, start time.Time, stream bool) {
	now := time.Now()
	req := store.Request{
		KeyID:      key.ID,
		KeyName:    key.Name,
		Model:      model,
		Endpoint:   endpoint,
		APIFamily:  family,
		SessionID:  session,
		IP:         ClientIP(r, p.cfg.TrustProxy),
		UserAgent:  truncate(r.UserAgent(), 300),
		Error:      truncate(errMsg, 500),
		Status:     status,
		Stream:     stream,
		DurationMS: time.Since(start).Milliseconds(),
		CreatedAt:  now.Unix(),
	}
	var tokens int64
	if u != nil {
		req.Input = u.Input
		req.Output = u.Output
		req.Reasoning = u.Reasoning
		req.CacheRead = u.CacheRead
		req.CacheWrite = u.CacheWrite
		req.Cost = u.Cost
		req.Allowance = u.Allowance
		req.ListCost = u.ListCost
		req.Estimated = u.Estimated
		tokens = u.Tokens()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.store.InsertRequest(ctx, req); err != nil {
		p.log.Error("failed to record request", "error", err)
	}
	if status > 0 {
		p.tracker.Add(key.ID, now.Unix(), req.Cost, tokens)
	}
	if status > 0 {
		p.touchKey(ctx, key.ID, now.Unix())
	}
}

func (p *Proxy) touchKey(ctx context.Context, id string, now int64) {
	p.touchMu.Lock()
	last := p.touched[id]
	if now-last >= 60 {
		p.touched[id] = now
	}
	p.touchMu.Unlock()
	if now-last >= 60 {
		p.store.TouchKey(ctx, id, now)
	}
}

func (p *Proxy) serveModels(w http.ResponseWriter, r *http.Request, key store.Key) {
	list := p.models.List()
	type entry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []entry `json:"data"`
	}{Object: "list", Data: []entry{}}
	now := time.Now().Unix()
	for _, m := range list {
		if m.Deprecated || !ModelAllowed(key.AllowedModels, m.ID) {
			continue
		}
		out.Data = append(out.Data, entry{ID: m.ID, Object: "model", Created: now, OwnedBy: "opencode-go-router"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

func CanonicalPath(p string) (string, bool) {
	switch {
	case p == "/go":
		return "/go/", true
	case strings.HasPrefix(p, "/go/"):
		return p, true
	case p == "/v1/models":
		return "/go/v1/models", true
	case p == "/v1/messages" || strings.HasPrefix(p, "/v1/messages/"):
		return "/go/anthropic" + p, true
	case strings.HasPrefix(p, "/v1/"):
		return "/go/openai" + p, true
	case strings.HasPrefix(p, "/v1beta/"):
		return "/go/google" + p, true
	case strings.HasPrefix(p, "/openai/"):
		return "/go" + p, true
	case strings.HasPrefix(p, "/anthropic/"):
		return "/go" + p, true
	case strings.HasPrefix(p, "/google/"):
		return "/go" + p, true
	default:
		return "", false
	}
}

func familyForPath(p string) string {
	switch {
	case strings.Contains(p, "/messages"):
		return "messages"
	case strings.Contains(p, "/responses"):
		return "responses"
	case strings.Contains(p, "/models/") || strings.Contains(p, ":generateContent") || strings.Contains(p, ":streamGenerateContent"):
		return "google"
	default:
		return "chat"
	}
}

func isModelsList(p, method string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	p = strings.TrimSuffix(p, "/")
	if p == "/go/v1/models" {
		return true
	}
	return strings.HasSuffix(p, "/models") && !strings.Contains(p, "/models/")
}

func extractModel(p string, body []byte) (string, error) {
	if strings.Contains(p, "/models/") {
		part := p[strings.Index(p, "/models/")+len("/models/"):]
		if i := strings.IndexByte(part, ':'); i >= 0 {
			part = part[:i]
		}
		if part != "" {
			if m, err := url.PathUnescape(part); err == nil {
				return m, nil
			}
			return part, nil
		}
	}
	if len(body) == 0 {
		return "", nil
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("invalid JSON request body")
	}
	if payload.Model == "" {
		return "", nil
	}
	return payload.Model, nil
}

func ModelAllowed(allowed []string, model string) bool {
	if model == "" {
		return true
	}
	for _, rule := range allowed {
		if rule == "*" || rule == model {
			return true
		}
		if strings.ContainsAny(rule, "*?") {
			if ok, err := path.Match(rule, model); err == nil && ok {
				return true
			}
		}
	}
	return false
}

func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	if h := strings.TrimSpace(r.Header.Get("X-Api-Key")); h != "" {
		return h
	}
	if h := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); h != "" {
		return h
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}

func copyRequestHeaders(dst, src http.Header) {
	for k, vals := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Host", "Content-Length",
			"Cookie", "Accept-Encoding", "Authorization", "X-Api-Key", "X-Goog-Api-Key",
			"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded":
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vals := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length", "Content-Encoding", "Set-Cookie":
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func sessionFromRequest(r *http.Request, body []byte, keyID string) string {
	for _, h := range []string{"X-Opencode-Session", "X-Opencode-Session-Id", "X-Session-Affinity", "X-Session-Id"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	if len(body) == 0 {
		return ""
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, key := range []string{"prompt_cache_key", "conversation_id"} {
		if raw, ok := payload[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return "ses_" + shortHash(s)
			}
		}
	}
	if raw, ok := payload["metadata"]; ok {
		var md map[string]any
		if json.Unmarshal(raw, &md) == nil {
			for _, key := range []string{"session_id", "conversation_id"} {
				if s, ok := md[key].(string); ok && s != "" {
					return "ses_" + shortHash(s)
				}
			}
		}
	}
	var firstUser any
	if raw, ok := payload["messages"]; ok {
		var msgs []map[string]any
		if json.Unmarshal(raw, &msgs) == nil {
			for _, m := range msgs {
				if role, _ := m["role"].(string); role == "user" {
					firstUser = m["content"]
					break
				}
			}
		}
	}
	if firstUser == nil {
		if raw, ok := payload["input"]; ok {
			firstUser = raw
		}
	}
	if firstUser == nil {
		return ""
	}
	b, _ := json.Marshal(firstUser)
	return "ses_" + shortHash(keyID+"\x00"+string(b))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}

func ensureStreamUsage(body []byte, canonical string) []byte {
	if len(body) == 0 || !strings.Contains(canonical, "/chat/completions") {
		return body
	}
	if !bytes.Contains(body, []byte(`"stream":true`)) && !bytes.Contains(body, []byte(`"stream": true`)) {
		return body
	}
	if bytes.Contains(body, []byte(`"include_usage":true`)) || bytes.Contains(body, []byte(`"include_usage": true`)) {
		return body
	}
	if bytes.Contains(body, []byte(`"stream_options"`)) {
		var payload map[string]json.RawMessage
		if json.Unmarshal(body, &payload) != nil {
			return body
		}
		var opts map[string]json.RawMessage
		if raw, ok := payload["stream_options"]; ok {
			_ = json.Unmarshal(raw, &opts)
		}
		if opts == nil {
			opts = map[string]json.RawMessage{}
		}
		opts["include_usage"] = json.RawMessage("true")
		rawOpts, err := json.Marshal(opts)
		if err != nil {
			return body
		}
		payload["stream_options"] = rawOpts
		out, err := json.Marshal(payload)
		if err != nil {
			return body
		}
		return out
	}
	idx := bytes.LastIndexByte(body, '}')
	if idx < 0 {
		return body
	}
	prefix := bytes.TrimSpace(body[:idx])
	insert := []byte(`,"stream_options":{"include_usage":true}`)
	if bytes.Equal(prefix, []byte("{")) {
		insert = []byte(`"stream_options":{"include_usage":true}`)
	}
	out := make([]byte, 0, len(body)+len(insert))
	out = append(out, body[:idx]...)
	out = append(out, insert...)
	out = append(out, body[idx:]...)
	return out
}

func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				xff = xff[:i]
			}
			if s := strings.TrimSpace(xff); s != "" {
				return s
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-Ip")); xr != "" {
			return xr
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeError(w http.ResponseWriter, family string, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var payload any
	switch family {
	case "messages":
		errType := code
		if code == "" {
			errType = "api_error"
		}
		payload = map[string]any{"type": "error", "error": map[string]any{"type": errType, "message": message}}
	case "google":
		gs := "INVALID_ARGUMENT"
		switch status {
		case http.StatusUnauthorized:
			gs = "UNAUTHENTICATED"
		case http.StatusForbidden:
			gs = "PERMISSION_DENIED"
		case http.StatusTooManyRequests:
			gs = "RESOURCE_EXHAUSTED"
		case http.StatusServiceUnavailable, http.StatusBadGateway:
			gs = "UNAVAILABLE"
		case http.StatusInternalServerError:
			gs = "INTERNAL"
		}
		payload = map[string]any{"error": map[string]any{"code": status, "message": message, "status": gs}}
	default:
		errType := "invalid_request_error"
		switch status {
		case http.StatusUnauthorized:
			errType = "authentication_error"
		case http.StatusForbidden:
			errType = "permission_error"
		case http.StatusTooManyRequests:
			errType = "rate_limit_error"
		case http.StatusServiceUnavailable, http.StatusBadGateway:
			errType = "api_error"
		case http.StatusInternalServerError:
			errType = "api_error"
		}
		payload = map[string]any{"error": map[string]any{"message": message, "type": errType, "code": code}}
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func formatWindow(sec int64) string {
	switch sec {
	case 5 * 3600:
		return "5h"
	case 86400:
		return "24h"
	case 7 * 86400:
		return "7d"
	case 30 * 86400:
		return "30d"
	}
	if sec%86400 == 0 {
		return fmt.Sprintf("%dd", sec/86400)
	}
	if sec%3600 == 0 {
		return fmt.Sprintf("%dh", sec/3600)
	}
	return fmt.Sprintf("%ds", sec)
}

func formatLimitPhrase(metric string, used, max float64) string {
	switch metric {
	case "requests":
		return fmt.Sprintf("%.0f of %.0f requests", used, max)
	case "tokens":
		return fmt.Sprintf("%.0f of %.0f tokens", used, max)
	default:
		return fmt.Sprintf("$%.4f of $%.4f spend", used, max)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
