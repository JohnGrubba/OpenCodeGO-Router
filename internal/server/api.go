package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonas/opencode-go-router/internal/auth"
	"github.com/jonas/opencode-go-router/internal/catalog"
	"github.com/jonas/opencode-go-router/internal/proxy"
	"github.com/jonas/opencode-go-router/internal/store"
)

type limitView struct {
	ID            int64   `json:"id"`
	Metric        string  `json:"metric"`
	WindowSeconds int64   `json:"window_seconds"`
	Max           float64 `json:"max"`
	Used          float64 `json:"used"`
	ResetAt       int64   `json:"reset_at"`
}

type keyView struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Note          string         `json:"note"`
	Prefix        string         `json:"prefix"`
	Enabled       bool           `json:"enabled"`
	AllowedModels []string       `json:"allowed_models"`
	ExpiresAt     int64          `json:"expires_at"`
	CreatedAt     int64          `json:"created_at"`
	LastUsedAt    int64          `json:"last_used_at"`
	Status        string         `json:"status"`
	Limits        []limitView    `json:"limits"`
	Usage         store.KeyUsage `json:"usage"`
	Blocked       bool           `json:"blocked"`
}

type limitInput struct {
	Metric        string  `json:"metric"`
	WindowSeconds int64   `json:"window_seconds"`
	Max           float64 `json:"max"`
}

type keyCreateInput struct {
	Name          string       `json:"name"`
	Note          string       `json:"note"`
	ExpiresAt     int64        `json:"expires_at"`
	AllowedModels []string     `json:"allowed_models"`
	Limits        []limitInput `json:"limits"`
}

type keyUpdateInput struct {
	Name          *string       `json:"name"`
	Note          *string       `json:"note"`
	Enabled       *bool         `json:"enabled"`
	ExpiresAt     *int64        `json:"expires_at"`
	AllowedModels *[]string     `json:"allowed_models"`
	Limits        *[]limitInput `json:"limits"`
}

func (s *Server) keyView(ctx context.Context, k store.Key, now int64, u store.KeyUsage) keyView {
	statuses, blocked := s.tracker.Check(k.ID, k.Limits, now)
	lv := make([]limitView, 0, len(statuses))
	for _, st := range statuses {
		lv = append(lv, limitView{ID: st.ID, Metric: st.Metric, WindowSeconds: st.WindowSeconds, Max: st.Max, Used: st.Used, ResetAt: st.ResetAt})
	}
	st := "active"
	switch {
	case !k.Enabled:
		st = "disabled"
	case k.ExpiresAt > 0 && now >= k.ExpiresAt:
		st = "expired"
	case blocked:
		st = "limited"
	}
	return keyView{
		ID: k.ID, Name: k.Name, Note: k.Note, Prefix: k.Prefix, Enabled: k.Enabled,
		AllowedModels: k.AllowedModels, ExpiresAt: k.ExpiresAt, CreatedAt: k.CreatedAt,
		LastUsedAt: k.LastUsedAt, Status: st, Limits: lv, Usage: u, Blocked: blocked,
	}
}

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	keys, err := s.store.ListKeys(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load keys.")
		return
	}
	now := time.Now().Unix()
	usageMap, err := s.store.KeyUsageAll(ctx, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load usage.")
		return
	}
	out := make([]keyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.keyView(ctx, k, now, usageMap[k.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out, "server_time": now})
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	var in keyCreateInput
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limits, err := validateLimits(in.Limits)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name, note, allowed, exp, err := validateKeyFields(in.Name, in.Note, in.AllowedModels, in.ExpiresAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	secret, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not generate key.")
		return
	}
	now := time.Now().Unix()
	key := store.Key{
		ID: auth.NewID("key_"), Name: name, Note: note, Prefix: prefix, Hash: hash,
		Enabled: true, AllowedModels: allowed, ExpiresAt: exp, CreatedAt: now, Limits: limits,
	}
	if err := s.store.CreateKey(r.Context(), key); err != nil {
		s.log.Error("create key failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "Could not save key.")
		return
	}
	var window int64
	for _, l := range limits {
		if l.WindowSeconds > window {
			window = l.WindowSeconds
		}
	}
	s.tracker.EnsureWindow(window)
	s.log.Info("api key created", "key_id", key.ID, "name", key.Name, "limits", len(limits), "models", len(allowed))
	cfg, cfgText, _ := s.buildClientConfig(s.publicBase(r), &key, secret)
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":                    s.keyView(r.Context(), key, now, store.KeyUsage{}),
		"secret":                 secret,
		"client_config":          cfg,
		"client_config_text":     cfgText,
		"client_config_filename": "opencode.jsonc",
	})
}

func (s *Server) getKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	k, err := s.store.GetKey(ctx, id)
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "Key not found.")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load key.")
		return
	}
	now := time.Now().Unix()
	usageMap, _ := s.store.KeyUsageAll(ctx, now)
	series, _ := s.store.KeySeries(ctx, id, now-7*86400, now+1, 3*3600)
	totals, _ := s.store.KeyTotals(ctx, id, now-30*86400, now+1)
	recent, _ := s.store.Requests(ctx, store.RequestFilter{KeyID: id, Limit: 25})
	writeJSON(w, http.StatusOK, map[string]any{
		"key":         s.keyView(ctx, k, now, usageMap[id]),
		"series":      fillSeries(series, now-7*86400, now, 3*3600),
		"totals_30d":  totals,
		"recent":      recent,
		"server_time": now,
	})
}

func (s *Server) updateKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	k, err := s.store.GetKey(ctx, id)
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "Key not found.")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load key.")
		return
	}
	var in keyUpdateInput
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name, note := k.Name, k.Note
	allowed, exp := k.AllowedModels, k.ExpiresAt
	if in.Name != nil {
		name = *in.Name
	}
	if in.Note != nil {
		note = *in.Note
	}
	if in.AllowedModels != nil {
		allowed = *in.AllowedModels
	}
	if in.ExpiresAt != nil {
		exp = *in.ExpiresAt
	}
	name, note, allowed, exp, err = validateKeyFields(name, note, allowed, exp)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	limits := k.Limits
	if in.Limits != nil {
		raw := make([]limitInput, 0, len(*in.Limits))
		for _, l := range *in.Limits {
			raw = append(raw, limitInput{Metric: l.Metric, WindowSeconds: l.WindowSeconds, Max: l.Max})
		}
		limits, err = validateLimits(raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	enabled := k.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	k.Name, k.Note, k.AllowedModels, k.ExpiresAt, k.Enabled, k.Limits = name, note, allowed, exp, enabled, limits
	if err := s.store.UpdateKey(ctx, k); err != nil {
		s.log.Error("update key failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "Could not update key.")
		return
	}
	var window int64
	for _, l := range limits {
		if l.WindowSeconds > window {
			window = l.WindowSeconds
		}
	}
	s.tracker.EnsureWindow(window)
	now := time.Now().Unix()
	usageMap, _ := s.store.KeyUsageAll(ctx, now)
	writeJSON(w, http.StatusOK, map[string]any{"key": s.keyView(ctx, k, now, usageMap[id])})
}

func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteKey(r.Context(), id); err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "Key not found.")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not delete key.")
		return
	}
	s.log.Info("api key deleted", "key_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	k, err := s.store.GetKey(r.Context(), id)
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "Key not found.")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load key.")
		return
	}
	secret, prefix, hash, err := auth.NewAPIKey()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not generate key.")
		return
	}
	if err := s.store.RotateKey(r.Context(), id, prefix, hash); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not rotate key.")
		return
	}
	s.log.Info("api key rotated", "key_id", id, "name", k.Name)
	cfg, cfgText, _ := s.buildClientConfig(s.publicBase(r), &k, secret)
	writeJSON(w, http.StatusOK, map[string]any{
		"secret":                 secret,
		"prefix":                 prefix,
		"client_config":          cfg,
		"client_config_text":     cfgText,
		"client_config_filename": "opencode.jsonc",
	})
}

func (s *Server) setKeyEnabled(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(w, r, &in); err != nil || in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "enabled is required")
		return
	}
	if err := s.store.SetKeyEnabled(r.Context(), id, *in.Enabled); err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "Key not found.")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not update key.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *in.Enabled})
}

func validateKeyFields(name, note string, allowed []string, exp int64) (string, string, []string, int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return "", "", nil, 0, fmt.Errorf("name must be between 1 and 80 characters")
	}
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return "", "", nil, 0, fmt.Errorf("note must be at most 500 characters")
	}
	if exp < 0 {
		return "", "", nil, 0, fmt.Errorf("invalid expiry")
	}
	if exp > 0 && exp <= time.Now().Unix() {
		return "", "", nil, 0, fmt.Errorf("expiry must be in the future")
	}
	if exp > time.Now().AddDate(10, 0, 0).Unix() {
		return "", "", nil, 0, fmt.Errorf("expiry is too far in the future")
	}
	clean := make([]string, 0, len(allowed))
	seen := map[string]bool{}
	for _, m := range allowed {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > 120 || seen[m] {
			continue
		}
		seen[m] = true
		clean = append(clean, m)
	}
	if seen["*"] {
		clean = []string{"*"}
	}
	if len(clean) > 200 {
		return "", "", nil, 0, fmt.Errorf("too many model rules")
	}
	return name, note, clean, exp, nil
}

func validateLimits(in []limitInput) ([]store.Limit, error) {
	if len(in) > 10 {
		return nil, fmt.Errorf("at most 10 limits are allowed")
	}
	out := make([]store.Limit, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		switch l.Metric {
		case "cost", "requests", "tokens":
		default:
			return nil, fmt.Errorf("invalid metric %q", l.Metric)
		}
		if l.WindowSeconds < 60 || l.WindowSeconds > 90*86400 {
			return nil, fmt.Errorf("limit window must be between 1 minute and 90 days")
		}
		if l.WindowSeconds%60 != 0 {
			return nil, fmt.Errorf("limit window must be a whole number of minutes")
		}
		if l.Max <= 0 || math.IsNaN(l.Max) || math.IsInf(l.Max, 0) || l.Max > 1e9 {
			return nil, fmt.Errorf("limit amount must be greater than zero")
		}
		key := l.Metric + ":" + strconv.FormatInt(l.WindowSeconds, 10)
		if seen[key] {
			return nil, fmt.Errorf("duplicate limit for %s per window", l.Metric)
		}
		seen[key] = true
		out = append(out, store.Limit{Metric: l.Metric, WindowSeconds: l.WindowSeconds, Max: l.Max})
	}
	return out, nil
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().Unix()
	from, to, bucket, label := parseRange(r.URL.Query().Get("range"), now)
	ov, err := s.store.Overview(ctx, from, to, bucket)
	if err != nil {
		s.log.Error("overview query failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "Could not load usage.")
		return
	}
	ov.Series = fillSeries(ov.Series, from, to, bucket)

	keys, _ := s.store.ListKeys(ctx)
	active, expiring, limited := 0, 0, 0
	for _, k := range keys {
		if k.Enabled && (k.ExpiresAt == 0 || k.ExpiresAt > now) {
			active++
		}
		if k.ExpiresAt > 0 && k.ExpiresAt <= now+7*86400 && k.ExpiresAt > now {
			expiring++
		}
		if k.Enabled && (k.ExpiresAt == 0 || k.ExpiresAt > now) {
			if _, blocked := s.tracker.Check(k.ID, k.Limits, now); blocked {
				limited++
			}
		}
	}

	modelNames := make(map[string]string)
	for _, m := range s.models.List() {
		modelNames[m.ID] = m.Name
	}
	type modelRow struct {
		store.ModelUsage
		Name string `json:"name"`
	}
	models := make([]modelRow, 0, len(ov.Models))
	for _, m := range ov.Models {
		name := modelNames[m.Model]
		if name == "" {
			name = m.Model
		}
		models = append(models, modelRow{ModelUsage: m, Name: name})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"range":       label,
		"from":        from,
		"to":          to,
		"totals":      ov.Totals,
		"series":      ov.Series,
		"models":      models,
		"keys":        ov.Keys,
		"counts":      map[string]int{"keys": len(keys), "active": active, "expiring": expiring, "limited": limited},
		"server_time": now,
	})
}

func parseRange(v string, now int64) (from, to, bucket int64, label string) {
	label = "24h"
	dur := int64(86400)
	bucket = 900
	switch v {
	case "7d":
		dur, bucket, label = 7*86400, 3*3600, "7d"
	case "30d":
		dur, bucket, label = 30*86400, 12*3600, "30d"
	case "90d":
		dur, bucket, label = 90*86400, 86400, "90d"
	}
	return now - dur, now + 1, bucket, label
}

func fillSeries(points []store.SeriesPoint, from, to, bucket int64) []store.SeriesPoint {
	if bucket < 1 {
		bucket = 3600
	}
	byT := make(map[int64]store.SeriesPoint, len(points))
	for _, p := range points {
		byT[p.T] = p
	}
	start := (from / bucket) * bucket
	out := make([]store.SeriesPoint, 0, (to-start)/bucket+1)
	for t := start; t < to; t += bucket {
		if p, ok := byT[t]; ok {
			out = append(out, p)
		} else {
			out = append(out, store.SeriesPoint{T: t})
		}
	}
	return out
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.RequestFilter{
		KeyID:  q.Get("key"),
		Model:  q.Get("model"),
		Status: q.Get("status"),
	}
	f.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if f.Limit == 0 {
		f.Limit = 50
	}
	rows, err := s.store.Requests(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load activity.")
		return
	}
	var next int64
	if len(rows) == f.Limit && len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows, "next_before": next, "server_time": time.Now().Unix()})
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	fetched, lastErr := s.models.Status()
	plan := s.plan(r.Context())
	out := s.models.List()
	var fetchedAt int64
	if !fetched.IsZero() {
		fetchedAt = fetched.Unix()
	}
	resp := map[string]any{
		"models":     out,
		"fetched_at": fetchedAt,
		"plan":       plan,
	}
	if lastErr != nil {
		resp["error"] = lastErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) refreshModels(w http.ResponseWriter, r *http.Request) {
	if err := s.models.Refresh(r.Context()); err != nil {
		writeErr(w, http.StatusBadGateway, "Could not refresh models: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(s.models.List())})
}

func (s *Server) planUsage(w http.ResponseWriter, r *http.Request) {
	plan := r.URL.Query().Get("plan")
	if plan != "go" && plan != "go-plus" {
		plan = s.plan(r.Context())
	}
	now := time.Now().Unix()
	usageMap, err := s.store.PlanUsage(r.Context(), now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not load plan usage.")
		return
	}
	type usageBar struct {
		Used  float64 `json:"used"`
		Limit float64 `json:"limit"`
	}
	type planRow struct {
		ID         string   `json:"id"`
		Name       string   `json:"name"`
		Monthly    float64  `json:"monthly_limit"`
		H5         usageBar `json:"h5"`
		D7         usageBar `json:"d7"`
		D30        usageBar `json:"d30"`
		Deprecated bool     `json:"deprecated,omitempty"`
	}
	rows := []planRow{}
	var totalUsed, totalLimit float64
	for _, m := range s.models.List() {
		monthly := m.GoMonthly
		if plan == "go-plus" {
			monthly = m.GoPlusMonthly
		}
		if monthly <= 0 {
			continue
		}
		u := usageMap[m.ID]
		row := planRow{
			ID: m.ID, Name: m.Name, Monthly: monthly,
			H5:  usageBar{Used: u.H5.Cost, Limit: monthly * 0.2},
			D7:  usageBar{Used: u.D7.Cost, Limit: monthly * 0.5},
			D30: usageBar{Used: u.D30.Cost, Limit: monthly},
		}
		totalUsed += u.D30.Cost
		totalLimit += monthly
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := 0.0, 0.0
		if rows[i].D30.Limit > 0 {
			ri = rows[i].D30.Used / rows[i].D30.Limit
		}
		if rows[j].D30.Limit > 0 {
			rj = rows[j].D30.Used / rows[j].D30.Limit
		}
		return ri > rj
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":        plan,
		"models":      rows,
		"totals":      map[string]float64{"used": totalUsed, "limit": totalLimit},
		"server_time": now,
	})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	fetched, lastErr := s.models.Status()
	var fetchedAt int64
	if !fetched.IsZero() {
		fetchedAt = fetched.Unix()
	}
	resp := map[string]any{
		"name":              s.instanceName(r.Context()),
		"plan":              s.plan(r.Context()),
		"retention_days":    s.retentionDays(r.Context()),
		"version":           s.cfg.Version,
		"public_url":        s.publicBase(r),
		"trust_proxy":       s.cfg.TrustProxy,
		"session_ttl_hours": int(s.cfg.SessionTTL.Hours()),
		"upstream": map[string]any{
			"configured": s.cfg.APIKey != "",
			"base_url":   s.cfg.UpstreamBaseURL,
			"fetched_at": fetchedAt,
		},
	}
	if lastErr != nil {
		resp["upstream"].(map[string]any)["error"] = lastErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          *string `json:"name"`
		Plan          *string `json:"plan"`
		RetentionDays *int    `json:"retention_days"`
	}
	if err := readJSON(w, r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 60 {
			writeErr(w, http.StatusBadRequest, "name must be between 1 and 60 characters")
			return
		}
		if err := s.store.SetSetting(r.Context(), "instance_name", n); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not save settings.")
			return
		}
	}
	if in.Plan != nil {
		if *in.Plan != "go" && *in.Plan != "go-plus" {
			writeErr(w, http.StatusBadRequest, "plan must be go or go-plus")
			return
		}
		if err := s.store.SetSetting(r.Context(), "plan", *in.Plan); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not save settings.")
			return
		}
	}
	if in.RetentionDays != nil {
		if *in.RetentionDays < 1 || *in.RetentionDays > 3650 {
			writeErr(w, http.StatusBadRequest, "retention_days must be between 1 and 3650")
			return
		}
		if err := s.store.SetSetting(r.Context(), "retention_days", strconv.Itoa(*in.RetentionDays)); err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not save settings.")
			return
		}
	}
	s.getSettings(w, r)
}

func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	now := time.Now().Unix()
	from, to, _, _ := parseRange(r.URL.Query().Get("range"), now)
	rows, err := s.store.ExportRequests(r.Context(), from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not export activity.")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="opencode-go-router-usage.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"timestamp", "key_id", "key_name", "model", "endpoint", "status", "stream", "input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "allowance_usd", "list_cost_usd", "estimated", "duration_ms", "session_id", "ip", "error"})
	for _, row := range rows {
		_ = cw.Write([]string{
			time.Unix(row.CreatedAt, 0).UTC().Format(time.RFC3339),
			csvSafe(row.KeyID), csvSafe(row.KeyName), csvSafe(row.Model), csvSafe(row.Endpoint),
			strconv.Itoa(row.Status), strconv.FormatBool(row.Stream),
			strconv.FormatInt(row.Input, 10), strconv.FormatInt(row.Output, 10), strconv.FormatInt(row.Reasoning, 10),
			strconv.FormatInt(row.CacheRead, 10), strconv.FormatInt(row.CacheWrite, 10),
			strconv.FormatFloat(row.Cost, 'f', 6, 64),
			strconv.FormatFloat(row.Allowance, 'f', 6, 64), strconv.FormatFloat(row.ListCost, 'f', 6, 64),
			strconv.FormatBool(row.Estimated), strconv.FormatInt(row.DurationMS, 10),
			csvSafe(row.SessionID), csvSafe(row.IP), csvSafe(row.Error),
		})
	}
	cw.Flush()
}

func chooseSmallModel(available []catalog.Model) string {
	preferred := []string{"glm-5.3-flash", "deepseek-v4.1-flash", "minimax-m3.1-flash", "claude-haiku-5-5", "gpt-6-luna", "mimo-v2.6-flash", "glm-5.2"}
	for _, id := range preferred {
		for _, m := range available {
			if m.ID == id {
				return id
			}
		}
	}
	if len(available) > 0 {
		return available[0].ID
	}
	return ""
}

func csvSafe(s string) string {
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s[:1], "=+-@") {
		return "'" + s
	}
	return s
}

func (s *Server) setupConfig(w http.ResponseWriter, r *http.Request) {
	base := s.publicBase(r)
	var filter *store.Key
	if id := r.URL.Query().Get("key"); id != "" {
		k, err := s.store.GetKey(r.Context(), id)
		if err == store.ErrNotFound {
			writeErr(w, http.StatusNotFound, "Key not found.")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "Could not load key.")
			return
		}
		filter = &k
	}
	cfg, text, ids := s.buildClientConfig(base, filter, "")
	resp := map[string]any{
		"public_url":       base,
		"ready":            false,
		"note":             "This is a template. Create or rotate a key to download a finished opencode.jsonc with the key embedded.",
		"opencode_json":    cfg,
		"opencode_jsonc":   text,
		"model_count":      len(ids),
		"install_command":  "curl -fsSL https://opencode.ai/v2/install | bash",
		"example_command":  "opencode run --model go/kimi-k3 \"hello\"",
		"config_path_hint": "~/.config/opencode/opencode.jsonc",
	}
	if filter != nil {
		resp["key_id"] = filter.ID
		resp["key_name"] = filter.Name
		resp["key_prefix"] = filter.Prefix
	}
	writeJSON(w, http.StatusOK, resp)
}

// buildClientConfig creates a ready-to-use opencode v2 config. When apiKey is
// provided the key is embedded in provider settings, so no environment
// variables are needed on the friend's machine.
func (s *Server) buildClientConfig(base string, filter *store.Key, apiKey string) (map[string]any, string, []string) {
	modelsCfg := map[string]any{}
	modelIDs := []string{}
	available := make([]catalog.Model, 0, len(s.models.List()))
	for _, m := range s.models.List() {
		if m.Deprecated {
			continue
		}
		if filter != nil && !proxy.ModelAllowed(filter.AllowedModels, m.ID) {
			continue
		}
		available = append(available, m)
		entry := map[string]any{}
		switch m.APIFamily {
		case "responses":
			entry["package"] = "@opencode/ai/providers/openai"
			entry["settings"] = map[string]any{"baseURL": base + "/go/openai/v1", "transport": "http"}
		case "messages":
			entry["package"] = "@opencode/ai/providers/anthropic"
			entry["settings"] = map[string]any{"baseURL": base + "/go/anthropic/v1"}
		case "google":
			entry["package"] = "@opencode/ai/providers/google"
			entry["settings"] = map[string]any{"baseURL": base + "/go/google/v1beta"}
		}
		modelsCfg[m.ID] = entry
		modelIDs = append(modelIDs, m.ID)
	}

	settings := map[string]any{"baseURL": base + "/go/openai/v1"}
	provider := map[string]any{
		"name":      "OpenCode Go (Router)",
		"canonical": "opencode-go",
		"package":   "@opencode/ai/providers/openai-compatible",
		"settings":  settings,
		"models":    modelsCfg,
	}
	if apiKey != "" {
		// Belt and suspenders: newer OpenCode builds read settings.apiKey,
		// while provider headers work across client versions and API families.
		settings["apiKey"] = apiKey
		provider["headers"] = map[string]string{
			"Authorization":  "Bearer " + apiKey,
			"X-Api-Key":      apiKey,
			"X-Goog-Api-Key": apiKey,
		}
	} else {
		settings["apiKey"] = "ocgr_REPLACE_ME"
	}
	cfg := map[string]any{
		"$schema":   "https://opencode.ai/config.json",
		"providers": map[string]any{"go": provider},
	}
	// OpenCode uses a small model for titles and other auxiliary work. If it
	// picks a model that is outside the key's allow-list those requests fail,
	// so pin the small model to something this key can actually reach.
	if small := chooseSmallModel(available); small != "" {
		cfg["small_model"] = "go/" + small
	}
	pretty, _ := json.MarshalIndent(cfg, "", "  ")
	return cfg, string(pretty), modelIDs
}
