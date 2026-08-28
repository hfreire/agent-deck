package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A provider that stops being signed in must vanish from the bar rather than
// keep painting the numbers cached from the last session.
func TestPollerDropsProviderWhenCredentialsDisappear(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "quota.json"))
	store.Set(Snapshot{
		Provider:  ProviderClaude,
		Session:   &Window{UsedPercent: 42, WindowMinutes: SessionWindowMinutes},
		UpdatedAt: time.Now().Add(-time.Hour),
	})

	p := &Poller{Store: store, Fetchers: []Fetcher{&fakeFetcher{provider: ProviderClaude, err: ErrNoCredentials}}}
	p.Refresh(context.Background())

	if _, ok := store.Get(ProviderClaude); ok {
		t.Fatal("signed-out provider still has a cached snapshot")
	}
}

func TestStoreDelete(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "quota.json"))
	store.Set(Snapshot{Provider: ProviderCodex, Session: &Window{UsedPercent: 1}})
	store.Delete(ProviderCodex)
	if _, ok := store.Get(ProviderCodex); ok {
		t.Fatal("Delete left the snapshot in place")
	}
	store.Delete(ProviderCodex) // deleting an absent provider is a no-op
}

// The keychain lookup shells out to `security`; repeating it every tick is
// wasted work while the token is still valid.
func TestClaudeFetcherCachesTokenUntilExpiry(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":10}}`))
	}))
	defer srv.Close()

	f := &ClaudeFetcher{
		UsageURL:   srv.URL,
		HTTPClient: srv.Client(),
		readCredential: func() (claudeCredential, error) {
			reads++
			return claudeCredential{token: "tok", expiresAt: time.Now().Add(time.Hour)}, nil
		},
	}
	for i := 0; i < 3; i++ {
		if _, err := f.Fetch(context.Background()); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if reads != 1 {
		t.Fatalf("credential read %d times, want 1", reads)
	}
}

func TestClaudeFetcherRereadsExpiredToken(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":10}}`))
	}))
	defer srv.Close()

	f := &ClaudeFetcher{
		UsageURL:   srv.URL,
		HTTPClient: srv.Client(),
		readCredential: func() (claudeCredential, error) {
			reads++
			return claudeCredential{token: "tok", expiresAt: time.Now().Add(-time.Minute)}, nil
		},
	}
	for i := 0; i < 2; i++ {
		if _, err := f.Fetch(context.Background()); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if reads != 2 {
		t.Fatalf("credential read %d times, want 2", reads)
	}
}

func TestClaudeCredentialFromJSONReadsExpiry(t *testing.T) {
	expires := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	data := []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d}}`, expires.UnixMilli()))
	cred, err := claudeCredentialFromJSON(data)
	if err != nil {
		t.Fatalf("claudeCredentialFromJSON: %v", err)
	}
	if cred.token != "tok" {
		t.Fatalf("token = %q", cred.token)
	}
	if !cred.expiresAt.Equal(expires) {
		t.Fatalf("expiresAt = %v, want %v", cred.expiresAt, expires)
	}
}

// loadCodeAssist and the token exchange are per-account constants; repeating
// them on every tick triples the request count for one number.
func TestGeminiFetcherReusesProjectAndToken(t *testing.T) {
	var loads, tokens, quotas int
	mux := http.NewServeMux()
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		loads++
		_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-1"}`))
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		tokens++
		_, _ = w.Write([]byte(`{"access_token":"fresh","expires_in":3600}`))
	})
	mux.HandleFunc("/quota", func(w http.ResponseWriter, r *http.Request) {
		quotas++
		_, _ = w.Write([]byte(`[{"quotaId":"gemini-3-pro","usedPercent":12}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	creds, _ := json.Marshal(map[string]any{
		"access_token":  "stale",
		"refresh_token": "refresh",
		"expiry_date":   time.Now().Add(-time.Hour).UnixMilli(),
	})
	if err := os.WriteFile(filepath.Join(dir, geminiCredentialsFile), creds, 0o600); err != nil {
		t.Fatal(err)
	}

	f := &GeminiFetcher{
		ConfigDir:  dir,
		QuotaURL:   srv.URL + "/quota",
		LoadURL:    srv.URL + "/load",
		TokenURL:   srv.URL + "/token",
		HTTPClient: srv.Client(),
	}
	for i := 0; i < 3; i++ {
		if _, err := f.Fetch(context.Background()); err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
	}
	if quotas != 3 {
		t.Fatalf("quota requests = %d, want 3", quotas)
	}
	if loads != 1 {
		t.Fatalf("loadCodeAssist requests = %d, want 1", loads)
	}
	if tokens != 1 {
		t.Fatalf("token refreshes = %d, want 1", tokens)
	}
}
