package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

func quotaTestStore(t *testing.T) *quota.Store {
	t.Helper()
	store := quota.NewStore(filepath.Join(t.TempDir(), "quota.json"))
	now := time.Now()
	store.Set(quota.Snapshot{
		Provider:  quota.ProviderCodex,
		Session:   &quota.Window{UsedPercent: 2, WindowMinutes: 300, ResetsAt: now.Add(4 * time.Hour)},
		Plan:      "plus",
		UpdatedAt: now,
	})
	store.Set(quota.Snapshot{
		Provider:  quota.ProviderClaude,
		Session:   &quota.Window{UsedPercent: 41, WindowMinutes: 300, ResetsAt: now.Add(2 * time.Hour)},
		Weekly:    &quota.Window{UsedPercent: 44, WindowMinutes: 10080},
		UpdatedAt: now,
	})
	return store
}

func decodeQuotaProviders(t *testing.T, body []byte) []quota.Snapshot {
	t.Helper()
	var resp struct {
		Providers []quota.Snapshot `json:"providers"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("invalid JSON: %v; body: %s", err, body)
	}
	return resp.Providers
}

func TestHandleQuota_OK(t *testing.T) {
	s := &Server{cfg: Config{}, quotaStore: quotaTestStore(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	w := httptest.NewRecorder()

	s.handleQuota(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	providers := decodeQuotaProviders(t, w.Body.Bytes())
	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2: %+v", len(providers), providers)
	}
	// Fixed order, so a client never has to sort and the UI never reshuffles.
	if providers[0].Provider != quota.ProviderClaude || providers[1].Provider != quota.ProviderCodex {
		t.Fatalf("providers out of order: %+v", providers)
	}
	if providers[0].Session == nil || providers[0].Session.UsedPercent != 41 {
		t.Fatalf("claude session window lost in transit: %+v", providers[0])
	}
	if providers[1].Plan != "plus" {
		t.Fatalf("plan lost in transit: %+v", providers[1])
	}
}

// A server that has not polled yet must still answer 200 with an empty list:
// clients need to tell "no data yet" from "this server is too old to know the
// endpoint" (404).
func TestHandleQuota_EmptyStoreIsOK(t *testing.T) {
	for name, s := range map[string]*Server{
		"no store":    {cfg: Config{}},
		"empty store": {cfg: Config{}, quotaStore: quota.NewStore(filepath.Join(t.TempDir(), "quota.json"))},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
		w := httptest.NewRecorder()

		s.handleQuota(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body: %s", name, w.Code, w.Body.String())
		}
		if providers := decodeQuotaProviders(t, w.Body.Bytes()); len(providers) != 0 {
			t.Fatalf("%s: got %d providers, want none", name, len(providers))
		}
	}
}

func TestHandleQuota_MethodNotAllowed(t *testing.T) {
	s := &Server{cfg: Config{}, quotaStore: quotaTestStore(t)}
	req := httptest.NewRequest(http.MethodPost, "/api/quota", nil)
	w := httptest.NewRecorder()

	s.handleQuota(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleQuota_Unauthorized(t *testing.T) {
	s := &Server{cfg: Config{Token: "secret"}, quotaStore: quotaTestStore(t)}
	req := httptest.NewRequest(http.MethodGet, "/api/quota", nil)
	w := httptest.NewRecorder()

	s.handleQuota(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestSetQuotaStore(t *testing.T) {
	s := &Server{cfg: Config{}}
	store := quotaTestStore(t)
	s.SetQuotaStore(store)
	if s.quotaStore != store {
		t.Fatal("SetQuotaStore did not install the store")
	}
}
