package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestUsageAggregations(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if err := st.CreateKey(ctx, Key{ID: "k1", Name: "Friend", Prefix: "ocgr_x", Hash: "h", Enabled: true, AllowedModels: []string{"*"}, CreatedAt: now - 100}); err != nil {
		t.Fatal(err)
	}
	rows := []Request{
		{KeyID: "k1", KeyName: "Friend", Model: "kimi-k3", Status: 200, Cost: 0.1, Allowance: 0.1, Input: 50, Output: 50, CreatedAt: now - 10},
		{KeyID: "k1", KeyName: "Friend", Model: "kimi-k3", Status: 200, Cost: 0.2, Allowance: 0.2, Input: 100, Output: 100, CreatedAt: now - 2*3600},
		{KeyID: "k1", KeyName: "Friend", Model: "glm-5.3", Status: 200, Cost: 0.3, Allowance: 0.3, Input: 150, Output: 150, CreatedAt: now - 8*86400},
		{KeyID: "k1", KeyName: "Friend", Model: "glm-5.3", Status: 200, Cost: 0.4, Allowance: 0.4, Input: 200, Output: 200, CreatedAt: now - 40*86400},
	}
	for _, r := range rows {
		if err := st.InsertRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	usage, err := st.KeyUsageAll(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	u := usage["k1"]
	if !almost(u.H5.Cost, 0.3) || u.H5.Requests != 2 || u.H5.Tokens != 300 {
		t.Fatalf("h5 = %+v", u.H5)
	}
	if !almost(u.D1.Cost, 0.3) {
		t.Fatalf("d1 = %+v", u.D1)
	}
	if !almost(u.D7.Cost, 0.3) {
		t.Fatalf("d7 = %+v", u.D7)
	}
	if !almost(u.D30.Cost, 0.6) || u.D30.Requests != 3 || u.D30.Tokens != 600 {
		t.Fatalf("d30 = %+v", u.D30)
	}

	ov, err := st.Overview(ctx, now-30*86400, now+1, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if !almost(ov.Totals.Cost, 0.6) || ov.Totals.Requests != 3 {
		t.Fatalf("overview totals = %+v", ov.Totals)
	}
	if len(ov.Series) != 3 || len(ov.Models) != 2 || len(ov.Keys) != 1 {
		t.Fatalf("overview shapes: series=%d models=%d keys=%d", len(ov.Series), len(ov.Models), len(ov.Keys))
	}

	plan, err := st.PlanUsage(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if !almost(plan["kimi-k3"].H5.Cost, 0.3) || !almost(plan["glm-5.3"].D30.Cost, 0.3) {
		t.Fatalf("plan usage = %+v", plan)
	}

	reqs, err := st.Requests(ctx, RequestFilter{KeyID: "k1", Limit: 2})
	if err != nil || len(reqs) != 2 {
		t.Fatalf("requests = %d, %v", len(reqs), err)
	}
}

func TestSettingsAndKeyLifecycle(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "plan", "go-plus"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := st.Setting(ctx, "plan"); err != nil || !ok || v != "go-plus" {
		t.Fatalf("setting = %q %v %v", v, ok, err)
	}
	now := time.Now().Unix()
	k := Key{ID: "k1", Name: "A", Prefix: "p", Hash: "h", Enabled: true, AllowedModels: []string{}, CreatedAt: now, Limits: []Limit{{Metric: "cost", WindowSeconds: 3600, Max: 1}}}
	if err := st.CreateKey(ctx, k); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetKeyByHash(ctx, "h")
	if err != nil || len(got.Limits) != 1 || got.Limits[0].Max != 1 {
		t.Fatalf("get key = %+v %v", got, err)
	}
	if len(got.AllowedModels) != 0 {
		t.Fatalf("empty allow-list should stay empty: %+v", got.AllowedModels)
	}
	got.Limits = nil
	if err := st.UpdateKey(ctx, got); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetKey(ctx, "k1")
	if len(got.Limits) != 0 {
		t.Fatalf("limits not cleared: %+v", got.Limits)
	}
	if err := st.DeleteKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetKey(ctx, "k1"); err != ErrNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	now := time.Now().Unix()
	if err := st.CreateSession(ctx, Session{Hash: "s", CreatedAt: now, ExpiresAt: now + 60, LastSeen: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, "s", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, "s", now+61); err != ErrNotFound {
		t.Fatalf("expired session = %v", err)
	}
	if err := st.Cleanup(ctx, now+61, 30); err != nil {
		t.Fatal(err)
	}
}

func almost(a, b float64) bool {
	d := a - b
	return d < 1e-9 && d > -1e-9
}
