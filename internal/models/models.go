// Package models merges the pinned catalog with the live OpenCode Go model
// list and exposes a cheap, atomically-readable view for the proxy and API.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonas/opencode-go-router/internal/catalog"
	"github.com/jonas/opencode-go-router/internal/config"
)

type Service struct {
	cfg    config.Config
	log    *slog.Logger
	client *http.Client

	mu        sync.Mutex
	lastFetch time.Time
	lastErr   error

	all  atomic.Pointer[[]catalog.Model]
	byID atomic.Pointer[map[string]catalog.Model]
}

func New(cfg config.Config, log *slog.Logger) *Service {
	s := &Service{
		cfg: cfg,
		log: log,
		client: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          10,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		},
	}
	s.apply(catalog.Pinned())
	return s
}

func (s *Service) apply(list []catalog.Model) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Deprecated != list[j].Deprecated {
			return !list[i].Deprecated
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	idx := make(map[string]catalog.Model, len(list))
	for _, m := range list {
		idx[m.ID] = m
	}
	s.all.Store(&list)
	s.byID.Store(&idx)
}

// List returns the effective catalog.
func (s *Service) List() []catalog.Model {
	return append([]catalog.Model(nil), (*s.all.Load())...)
}

// Lookup returns metadata for a model.
func (s *Service) Lookup(id string) (catalog.Model, bool) {
	m, ok := (*s.byID.Load())[id]
	return m, ok
}

// Refresh fetches the live list and merges it over the pinned snapshot.
func (s *Service) Refresh(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	url := s.cfg.UpstreamBaseURL + "/go/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opencode-go-router/"+s.cfg.Version)
	if s.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.APIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.lastErr = err
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.lastErr = fmt.Errorf("upstream models endpoint returned %s", resp.Status)
		return s.lastErr
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		s.lastErr = err
		return err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		s.lastErr = err
		return err
	}

	pinned := make(map[string]catalog.Model)
	for _, m := range catalog.Pinned() {
		pinned[m.ID] = m
	}
	seen := make(map[string]bool, len(payload.Data))
	merged := make([]catalog.Model, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if m, ok := pinned[id]; ok {
			m.Live = true
			merged = append(merged, m)
			continue
		}
		m := catalog.Model{
			ID:          id,
			Name:        id,
			Package:     "@opencode/ai/providers/openai-compatible",
			APIFamily:   "chat",
			ToolCall:    true,
			Reasoning:   true,
			Context:     262144,
			OutputLimit: 131072,
			Live:        true,
		}
		// The live list does not expose an API family. Keep a small override
		// table for models that are newer than the pinned models.dev snapshot.
		switch id {
		case "grok-4.6":
			m.Package = "@opencode/ai/providers/openai"
			m.APIFamily = "responses"
		}
		merged = append(merged, m)
	}
	if len(merged) == 0 {
		s.lastErr = fmt.Errorf("upstream model list was empty; keeping snapshot")
		return s.lastErr
	}
	s.apply(merged)
	s.lastFetch = time.Now()
	s.lastErr = nil
	s.log.Info("model catalog refreshed", "count", len(merged))
	return nil
}

// RefreshLoop refreshes periodically until ctx is cancelled.
func (s *Service) RefreshLoop(ctx context.Context) {
	_ = s.Refresh(ctx)
	t := time.NewTicker(s.cfg.ModelsRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_ = s.Refresh(rctx)
			cancel()
		}
	}
}

func (s *Service) Status() (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastFetch, s.lastErr
}
