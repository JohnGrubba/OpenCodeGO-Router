// Package catalog contains a pinned snapshot of the OpenCode Go model catalog.
// The snapshot supplies names, API families, context limits and pricing. The
// live model list from the upstream is merged on top of it at runtime.
package catalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

//go:embed catalog.json
var embedded []byte

type Cost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// Model describes one upstream model.
type Model struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Family        string  `json:"family"`
	Package       string  `json:"package"`
	APIFamily     string  `json:"api_family"`
	Reasoning     bool    `json:"reasoning"`
	ToolCall      bool    `json:"tool_call"`
	Context       int64   `json:"context"`
	InputLimit    int64   `json:"input_limit"`
	OutputLimit   int64   `json:"output_limit"`
	Cost          Cost    `json:"cost"`
	Deprecated    bool    `json:"deprecated"`
	GoMonthly     float64 `json:"go_monthly"`
	GoPlusMonthly float64 `json:"go_plus_monthly"`
	Live          bool    `json:"live,omitempty"`
}

var (
	all  atomic.Pointer[[]Model]
	byID atomic.Pointer[map[string]Model]
)

func init() {
	var models []Model
	if err := json.Unmarshal(embedded, &models); err != nil {
		panic(fmt.Sprintf("invalid embedded model catalog: %v", err))
	}
	set(models)
}

func set(models []Model) {
	idx := make(map[string]Model, len(models))
	for _, m := range models {
		idx[m.ID] = m
	}
	all.Store(&models)
	byID.Store(&idx)
}

// All returns the pinned catalog snapshot.
func All() []Model {
	return append([]Model(nil), (*all.Load())...)
}

// Get looks up a catalog model by ID.
func Get(id string) (Model, bool) {
	m, ok := (*byID.Load())[id]
	return m, ok
}

// Replace swaps the effective catalog (snapshot + live upstream entries).
func Replace(models []Model) { set(models) }

// Pinned returns the original embedded snapshot.
func Pinned() []Model {
	var models []Model
	_ = json.Unmarshal(embedded, &models)
	return models
}
