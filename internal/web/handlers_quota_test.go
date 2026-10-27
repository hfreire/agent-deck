package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func seedQuotaCache(t *testing.T, snapshots ...quota.Snapshot) *quota.Store {
	t.Helper()
	profile, err := session.ResolveProfileForStorage("")
	if err != nil {
		t.Fatalf("resolving profile: %v", err)
	}
	store, err := quota.NewStore(profile)
	if err != nil {
		t.Fatalf("opening quota store: %v", err)
	}
	// The package shares one isolated HOME across tests, so each case starts
	// from a cache holding exactly what it seeded.
	if err := os.RemoveAll(store.Dir()); err != nil {
		t.Fatalf("clearing quota cache: %v", err)
	}
	for _, snapshot := range snapshots {
		if err := store.Save(snapshot); err != nil {
			t.Fatalf("saving %s snapshot: %v", snapshot.ID, err)
		}
	}
	return store
}

func getQuota(t *testing.T, s *Server) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	w := httptest.NewRecorder()
	s.handleQuota(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v; body: %s", err, w.Body.String())
	}
	return resp
}

// TestHandleQuota_NoCacheIsAnEmptyList pins 200 with an empty list rather than
// a 404 for a host that has never ingested anything: a client has to be able
// to tell "no data yet" from "this server is too old to have the endpoint".
func TestHandleQuota_NoCacheIsAnEmptyList(t *testing.T) {
	seedQuotaCache(t)
	resp := getQuota(t, &Server{cfg: Config{}})
	providers, ok := resp["providers"].([]any)
	if !ok {
		t.Fatalf("providers missing or not a list: %#v", resp)
	}
	if len(providers) != 0 {
		t.Fatalf("providers = %#v, want empty", providers)
	}
}

// TestHandleQuota_WireShape pins the exact field names the phone app decodes
// (AgentDeckKit QuotaSnapshot/QuotaWindow). The cache holds kind/label windows;
// this endpoint speaks session/weekly/extra, and a rename on either side has to
// fail here rather than silently blank the app's usage card.
func TestHandleQuota_WireShape(t *testing.T) {
	resetsAt := time.Now().Add(2 * time.Hour).Unix()
	seedQuotaCache(t,
		quota.Snapshot{
			ID:    quota.ProviderClaude,
			Label: "Claude",
			Plan:  "max",
			Windows: []quota.Window{
				{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 42, ResetsAt: &resetsAt},
				{Kind: quota.WindowSevenDay, Label: "7d", UsedPercentage: 7},
				{Kind: quota.WindowSpendLimit, Label: "spend", UsedPercentage: 110},
			},
			UpdatedAt: time.Now().Unix(),
		},
		quota.Snapshot{
			ID:        quota.ProviderZai,
			Label:     "Z.ai",
			Error:     "signed out",
			UpdatedAt: time.Now().Unix(),
		},
	)

	resp := getQuota(t, &Server{cfg: Config{}})
	raw, _ := json.Marshal(resp["providers"])
	var providers []struct {
		Provider string `json:"provider"`
		Session  *struct {
			UsedPercent   float64 `json:"used_percent"`
			WindowMinutes int     `json:"window_minutes"`
			ResetsAt      string  `json:"resets_at"`
		} `json:"session"`
		Weekly *struct {
			UsedPercent   float64 `json:"used_percent"`
			WindowMinutes int     `json:"window_minutes"`
			ResetsAt      string  `json:"resets_at"`
		} `json:"weekly"`
		Extra []struct {
			Name        string  `json:"name"`
			UsedPercent float64 `json:"used_percent"`
		} `json:"extra"`
		Plan string `json:"plan"`
		Err  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &providers); err != nil {
		t.Fatalf("decoding providers as the app does: %v; body: %s", err, raw)
	}
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2: %s", len(providers), raw)
	}

	// Claude first: the order is fixed so the app's list never reshuffles.
	claude := providers[0]
	if claude.Provider != quota.ProviderClaude {
		t.Fatalf("first provider = %q, want claude", claude.Provider)
	}
	if claude.Plan != "max" {
		t.Errorf("plan = %q, want max", claude.Plan)
	}
	if claude.Session == nil || claude.Session.UsedPercent != 42 {
		t.Fatalf("session = %#v, want 42%%", claude.Session)
	}
	if claude.Session.WindowMinutes != 300 {
		t.Errorf("session window_minutes = %d, want 300", claude.Session.WindowMinutes)
	}
	if claude.Session.ResetsAt == "" {
		t.Error("session resets_at missing, want the reported reset")
	}
	if claude.Weekly == nil || claude.Weekly.UsedPercent != 7 {
		t.Fatalf("weekly = %#v, want 7%%", claude.Weekly)
	}
	if claude.Weekly.WindowMinutes != 10080 {
		t.Errorf("weekly window_minutes = %d, want 10080", claude.Weekly.WindowMinutes)
	}
	// A window with no provider-reported reset must be omitted, not sent as a
	// 1970 or year-1 date.
	if claude.Weekly.ResetsAt != "" {
		t.Errorf("weekly resets_at = %q, want omitted", claude.Weekly.ResetsAt)
	}
	if len(claude.Extra) != 1 || claude.Extra[0].Name != "spend" || claude.Extra[0].UsedPercent != 110 {
		t.Errorf("extra = %#v, want the uncapped spend window", claude.Extra)
	}

	// A failed provider still appears, carrying its own error.
	if providers[1].Provider != quota.ProviderZai || providers[1].Err != "signed out" {
		t.Errorf("second provider = %#v, want zai carrying its error", providers[1])
	}
}

// TestHandleQuota_RefreshesStaleCodex pins that a Codex snapshot older than the
// store's TTL is re-read from Codex's own transcripts on request. Nothing
// polls Codex, so without this the phone shows whatever `agent-deck usage`
// last happened to write.
func TestHandleQuota_RefreshesStaleCodex(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexHome, "sessions", "2026", "10", "27")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating rollout dir: %v", err)
	}
	record := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":55.0,"window_minutes":300,"resets_in_seconds":600},"secondary":{"used_percent":9.0,"window_minutes":10080,"resets_in_seconds":60000}}}}`
	if err := os.WriteFile(filepath.Join(dir, "rollout-a.jsonl"), []byte(record+"\n"), 0o644); err != nil {
		t.Fatalf("writing rollout: %v", err)
	}

	store := seedQuotaCache(t, quota.Snapshot{
		ID:        quota.ProviderCodex,
		Label:     "Codex",
		Windows:   []quota.Window{{Kind: quota.WindowFiveHour, Label: "5h", UsedPercentage: 1}},
		UpdatedAt: time.Now().Add(-2 * time.Hour).Unix(),
	})

	resp := getQuota(t, &Server{cfg: Config{}})
	raw, _ := json.Marshal(resp["providers"])
	var providers []struct {
		Provider string `json:"provider"`
		Session  *struct {
			UsedPercent float64 `json:"used_percent"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &providers); err != nil {
		t.Fatalf("decoding providers: %v", err)
	}
	if len(providers) != 1 || providers[0].Provider != quota.ProviderCodex {
		t.Fatalf("providers = %s, want one codex entry", raw)
	}
	if providers[0].Session == nil || providers[0].Session.UsedPercent != 55 {
		t.Fatalf("session = %#v, want the 55%% reading from the transcript", providers[0].Session)
	}

	// The refreshed reading is written back, so the next reader (the TUI, the
	// accounts header) sees it too rather than re-scanning the transcripts.
	cached, err := store.Load()
	if err != nil {
		t.Fatalf("reloading cache: %v", err)
	}
	if len(cached) != 1 || len(cached[0].Windows) == 0 || cached[0].Windows[0].UsedPercentage != 55 {
		t.Fatalf("cached snapshot = %#v, want the refreshed 55%% reading", cached)
	}
}

func TestHandleQuota_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/quota", nil)
	w := httptest.NewRecorder()
	(&Server{cfg: Config{}}).handleQuota(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleQuota_Unauthorized(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	w := httptest.NewRecorder()
	(&Server{cfg: Config{Token: "secret"}}).handleQuota(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}
