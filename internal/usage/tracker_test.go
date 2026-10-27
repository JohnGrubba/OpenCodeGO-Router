package usage

import (
	"testing"
	"time"

	"github.com/jonas/opencode-go-router/internal/store"
)

func TestTrackerLimits(t *testing.T) {
	now := time.Now().Unix()
	tr := NewTracker(30 * 86400)
	tr.Add("k1", now-7200, 0.20, 100) // outside 1h, inside 24h
	tr.Add("k1", now-100, 0.60, 200)
	tr.Add("k1", now-10, 0.50, 300)

	limits := []store.Limit{
		{Metric: "cost", WindowSeconds: 3600, Max: 1.0},
		{Metric: "requests", WindowSeconds: 86400, Max: 10},
	}
	statuses, blocked := tr.Check("k1", limits, now)
	if !blocked {
		t.Fatal("expected blocked by cost")
	}
	if statuses[0].Used < 1.09 || statuses[0].Used > 1.11 {
		t.Fatalf("used = %v, want 1.10", statuses[0].Used)
	}
	if statuses[1].Used != 3 {
		t.Fatalf("requests used = %v, want 3", statuses[1].Used)
	}
	if statuses[0].ResetAt != now-100+3600 {
		t.Fatalf("reset = %d, want %d", statuses[0].ResetAt, now-100+3600)
	}
}

func TestTrackerPrune(t *testing.T) {
	now := time.Now().Unix()
	tr := NewTracker(30 * 86400)
	tr.Add("k1", now-40*86400, 1, 1)
	tr.Add("k1", now-10, 1, 1)
	tr.Prune(now)
	_, blocked := tr.Check("k1", []store.Limit{{Metric: "cost", WindowSeconds: 30 * 86400, Max: 0.5}}, now)
	if !blocked {
		t.Fatal("recent event should remain")
	}
}
