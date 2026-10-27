package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonas/opencode-go-router/internal/auth"
	"github.com/jonas/opencode-go-router/internal/config"
	"github.com/jonas/opencode-go-router/internal/models"
	"github.com/jonas/opencode-go-router/internal/proxy"
	"github.com/jonas/opencode-go-router/internal/store"
	"github.com/jonas/opencode-go-router/internal/usage"
	"github.com/jonas/opencode-go-router/internal/web"
)

const (
	sessionCookie = "ocr_session"
	csrfHeader    = "X-Requested-With"
	csrfValue     = "OpenCodeRouter"
)

type ctxKey int

const sessionContextKey ctxKey = 1

type Server struct {
	cfg     config.Config
	store   *store.Store
	tracker *usage.Tracker
	models  *models.Service
	proxy   *proxy.Proxy
	log     *slog.Logger
	started time.Time

	mux    *http.ServeMux
	assets fs.FS
	login  *loginLimiter
}

func New(cfg config.Config, st *store.Store, tracker *usage.Tracker, ms *models.Service, p *proxy.Proxy, log *slog.Logger) (*Server, error) {
	sub, err := fs.Sub(web.FS(), "static")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:     cfg,
		store:   st,
		tracker: tracker,
		models:  ms,
		proxy:   p,
		log:     log,
		started: time.Now(),
		mux:     http.NewServeMux(),
		assets:  sub,
		login:   newLoginLimiter(6, 10*time.Minute),
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler {
	return s.recoverer(s.securityHeaders(s.requestLogger(s.mux)))
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.health)
	m.HandleFunc("GET /readyz", s.ready)
	m.HandleFunc("GET /api/session", s.sessionInfo)
	m.HandleFunc("POST /api/login", s.loginHandler)
	m.HandleFunc("POST /api/logout", s.logoutHandler)

	admin := http.NewServeMux()
	admin.HandleFunc("GET /overview", s.overview)
	admin.HandleFunc("GET /keys", s.listKeys)
	admin.HandleFunc("POST /keys", s.createKey)
	admin.HandleFunc("GET /keys/{id}", s.getKey)
	admin.HandleFunc("PATCH /keys/{id}", s.updateKey)
	admin.HandleFunc("DELETE /keys/{id}", s.deleteKey)
	admin.HandleFunc("POST /keys/{id}/rotate", s.rotateKey)
	admin.HandleFunc("POST /keys/{id}/enabled", s.setKeyEnabled)
	admin.HandleFunc("GET /requests", s.listRequests)
	admin.HandleFunc("DELETE /requests", s.clearRequests)
	admin.HandleFunc("DELETE /sessions/expired", s.clearExpiredSessions)
	admin.HandleFunc("GET /models", s.listModels)
	admin.HandleFunc("POST /models/refresh", s.refreshModels)
	admin.HandleFunc("GET /plan", s.planUsage)
	admin.HandleFunc("GET /setup", s.setupConfig)
	admin.HandleFunc("GET /settings", s.getSettings)
	admin.HandleFunc("PATCH /settings", s.updateSettings)
	admin.HandleFunc("POST /password", s.changePassword)
	admin.HandleFunc("GET /export.csv", s.exportCSV)
	m.Handle("/api/", http.StripPrefix("/api", s.requireAdmin(admin)))

	for _, prefix := range []string{"/go/", "/v1/", "/v1beta/", "/openai/", "/anthropic/", "/google/"} {
		m.Handle(prefix, s.proxy)
	}
	m.HandleFunc("/", s.serveStatic)
}

// ---- middleware ----

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic while serving request", "path", r.URL.Path, "panic", rec)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if s.isSecure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = 200
		}
		isProxy := false
		for _, p := range []string{"/go/", "/v1/", "/v1beta/", "/openai/", "/anthropic/", "/google/"} {
			if strings.HasPrefix(r.URL.Path, p) {
				isProxy = true
				break
			}
		}
		if isProxy {
			return
		}
		lvl := slog.LevelDebug
		if sw.status >= 400 || r.URL.Path == "/api/login" {
			lvl = slog.LevelInfo
		}
		s.log.Log(r.Context(), lvl, "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"ip", proxy.ClientIP(r, s.cfg.TrustProxy),
		)
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.checkCSRF(r) {
			writeErr(w, http.StatusForbidden, "Request origin could not be verified.")
			return
		}
		if !isReadMethod(r.Method) && !isJSON(r) {
			writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json.")
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeErr(w, http.StatusUnauthorized, "Not signed in.")
			return
		}
		now := time.Now().Unix()
		sess, err := s.store.GetSession(r.Context(), auth.HashSecret(c.Value), now)
		if err != nil {
			s.clearSessionCookie(w, r)
			writeErr(w, http.StatusUnauthorized, "Session expired. Sign in again.")
			return
		}
		if now-sess.LastSeen > 300 {
			_ = s.store.TouchSession(r.Context(), sess.Hash, now)
		}
		ctx := context.WithValue(r.Context(), sessionContextKey, sess)
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) checkCSRF(r *http.Request) bool {
	if isReadMethod(r.Method) {
		return true
	}
	if r.Header.Get(csrfHeader) != csrfValue {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		allowed := map[string]bool{strings.ToLower(r.Host): true}
		if s.cfg.PublicURL != "" {
			if pu, err := url.Parse(s.cfg.PublicURL); err == nil {
				allowed[strings.ToLower(pu.Host)] = true
			}
		}
		if s.cfg.TrustProxy {
			if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
				allowed[strings.ToLower(strings.TrimSpace(strings.Split(xfh, ",")[0]))] = true
			}
		}
		if !allowed[strings.ToLower(u.Host)] {
			return false
		}
	}
	return true
}

func isReadMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func isJSON(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return r.ContentLength <= 0
	}
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && (mt == "application/json" || strings.HasSuffix(mt, "+json"))
}

func (s *Server) isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.cfg.TrustProxy {
		xfp := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
		return strings.EqualFold(xfp, "https")
	}
	return false
}

// ---- session helpers ----

func sessionFrom(ctx context.Context) (store.Session, bool) {
	v, ok := ctx.Value(sessionContextKey).(store.Session)
	return v, ok
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
		Expires:  time.Now().Add(s.cfg.SessionTTL),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.isSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

// ---- auth handlers ----

func (s *Server) loginHandler(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		writeErr(w, http.StatusForbidden, "Request origin could not be verified.")
		return
	}
	if !isJSON(r) {
		writeErr(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json.")
		return
	}
	ip := proxy.ClientIP(r, s.cfg.TrustProxy)
	if !s.login.allow(ip) {
		writeErr(w, http.StatusTooManyRequests, "Too many sign-in attempts. Try again later.")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, ok, err := s.store.AdminHash(r.Context())
	if err != nil || !ok {
		writeErr(w, http.StatusInternalServerError, "Admin account is not initialized.")
		return
	}
	if !auth.CheckPassword(hash, in.Password) {
		s.login.fail(ip)
		time.Sleep(250 * time.Millisecond)
		s.log.Warn("failed admin sign-in", "ip", ip)
		writeErr(w, http.StatusUnauthorized, "Incorrect password.")
		return
	}
	token, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not create session.")
		return
	}
	now := time.Now().Unix()
	if err := s.store.CreateSession(r.Context(), store.Session{
		Hash: tokenHash, IP: ip, UserAgent: truncate(r.UserAgent(), 300),
		CreatedAt: now, ExpiresAt: now + int64(s.cfg.SessionTTL.Seconds()), LastSeen: now,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not create session.")
		return
	}
	s.login.reset(ip)
	s.setSessionCookie(w, r, token)
	s.log.Info("admin signed in", "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		writeErr(w, http.StatusForbidden, "Request origin could not be verified.")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(r.Context(), auth.HashSecret(c.Value))
	}
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) sessionInfo(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"authenticated": false,
		"name":          s.instanceName(r.Context()),
		"version":       s.cfg.Version,
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if _, err := s.store.GetSession(r.Context(), auth.HashSecret(c.Value), time.Now().Unix()); err == nil {
			resp["authenticated"] = true
			resp["plan"] = s.plan(r.Context())
			resp["upstream"] = s.cfg.APIKey != ""
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r.Context())
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, ok, err := s.store.AdminHash(r.Context())
	if err != nil || !ok {
		writeErr(w, http.StatusInternalServerError, "Admin account is not initialized.")
		return
	}
	if !auth.CheckPassword(hash, in.Current) {
		time.Sleep(250 * time.Millisecond)
		writeErr(w, http.StatusForbidden, "Current password is incorrect.")
		return
	}
	newHash, err := auth.HashPassword(in.New)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.SetAdminHash(r.Context(), newHash); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not update password.")
		return
	}
	_ = s.store.DeleteOtherSessions(r.Context(), sess.Hash)
	s.log.Info("admin password changed", "ip", proxy.ClientIP(r, s.cfg.TrustProxy))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- settings helpers ----

func (s *Server) settingOr(ctx context.Context, key, def string) string {
	v, ok, err := s.store.Setting(ctx, key)
	if err != nil || !ok || v == "" {
		return def
	}
	return v
}

func (s *Server) instanceName(ctx context.Context) string {
	return s.settingOr(ctx, "instance_name", s.cfg.InstanceName)
}

func (s *Server) plan(ctx context.Context) string {
	p := s.settingOr(ctx, "plan", s.cfg.Plan)
	if p != "go" && p != "go-plus" {
		return "go"
	}
	return p
}

func (s *Server) retentionDays(ctx context.Context) int {
	v := s.settingOr(ctx, "retention_days", strconv.Itoa(s.cfg.RetentionDays))
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return s.cfg.RetentionDays
	}
	return n
}

func (s *Server) publicBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		if xfp := r.Header.Get("X-Forwarded-Proto"); xfp != "" {
			scheme = strings.ToLower(strings.TrimSpace(strings.Split(xfp, ",")[0]))
		}
		if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
			host = strings.TrimSpace(strings.Split(xfh, ",")[0])
		}
	}
	if scheme != "https" {
		scheme = "http"
	}
	return s.cfg.PublicBase(scheme, host)
}

// ---- health ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Health(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "database": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "uptime_seconds": int(time.Since(s.started).Seconds())})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Health(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "database": "unavailable"})
		return
	}
	if s.cfg.APIKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "error", "upstream": "missing OPENCODE_API_KEY"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": len(s.models.List())})
}

// ---- static ----

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	f, err := s.assets.Open(name)
	if err != nil {
		// SPA fallback
		s.serveIndex(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		s.serveIndex(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFileFS(w, r, s.assets, name)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	f, err := s.assets.Open("index.html")
	if err != nil {
		http.Error(w, "dashboard assets missing", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, s.assets, "index.html")
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("request body is empty")
		}
		return fmt.Errorf("invalid JSON request body")
	}
	if dec.More() {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- login rate limiting ----

type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, attempts: make(map[string][]time.Time)}
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cut := now.Add(-l.window)
	old := l.attempts[ip]
	kept := old[:0]
	for _, t := range old {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	l.attempts[ip] = kept
	return len(kept) < l.max
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[ip] = append(l.attempts[ip], time.Now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}
