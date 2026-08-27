package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCodexUsage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := parseCodexUsage(raw)
	if err != nil {
		t.Fatalf("parseCodexUsage: %v", err)
	}
	if snap.Plan != "plus" {
		t.Fatalf("plan = %q, want plus", snap.Plan)
	}
	if snap.Session == nil || snap.Session.UsedPercent != 0 || snap.Session.WindowMinutes != 300 {
		t.Fatalf("session = %+v, want 0%% over 300m", snap.Session)
	}
	if !snap.Session.ResetsAt.Equal(time.Unix(1787852906, 0)) {
		t.Fatalf("session reset = %v", snap.Session.ResetsAt)
	}
	if snap.Weekly == nil || snap.Weekly.UsedPercent != 6 || snap.Weekly.WindowMinutes != 10080 {
		t.Fatalf("weekly = %+v, want 6%% over 10080m", snap.Weekly)
	}
}

// The two windows are identified by their duration, not their position, so a
// payload that swaps them must still classify correctly.
func TestParseCodexUsageSwappedWindows(t *testing.T) {
	snap, err := parseCodexUsage([]byte(`{
		"plan_type": "pro",
		"rate_limit": {
			"primary_window":   {"used_percent": 6,  "limit_window_seconds": 604800, "reset_at": 1788340344},
			"secondary_window": {"used_percent": 11, "limit_window_seconds": 18000,  "reset_at": 1787852906}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == nil || snap.Session.UsedPercent != 11 {
		t.Fatalf("session = %+v, want 11%%", snap.Session)
	}
	if snap.Weekly == nil || snap.Weekly.UsedPercent != 6 {
		t.Fatalf("weekly = %+v, want 6%%", snap.Weekly)
	}
}

// A window with no recognisable duration falls back to its position: primary
// is the short window, secondary the long one.
func TestParseCodexUsageUnknownDurations(t *testing.T) {
	snap, err := parseCodexUsage([]byte(`{
		"plan_type": "plus",
		"rate_limit": {
			"primary_window":   {"used_percent": 3},
			"secondary_window": {"used_percent": 9}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == nil || snap.Session.UsedPercent != 3 || snap.Session.WindowMinutes != SessionWindowMinutes {
		t.Fatalf("session = %+v", snap.Session)
	}
	if snap.Weekly == nil || snap.Weekly.UsedPercent != 9 || snap.Weekly.WindowMinutes != WeeklyWindowMinutes {
		t.Fatalf("weekly = %+v", snap.Weekly)
	}
}

func TestCodexFetchSendsAccountHeaders(t *testing.T) {
	var gotAuth, gotAccount, gotOriginator string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		gotOriginator = r.Header.Get("originator")
		raw, _ := os.ReadFile(filepath.Join("testdata", "codex_usage.json"))
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	home := t.TempDir()
	auth := `{"tokens":{"access_token":"codex-tok","account_id":"acct-9"}}`
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}

	f := &CodexFetcher{CodexHome: home, UsageURL: srv.URL, HTTPClient: srv.Client()}
	snap, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer codex-tok" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acct-9" {
		t.Fatalf("ChatGPT-Account-Id = %q", gotAccount)
	}
	if gotOriginator != codexOriginator {
		t.Fatalf("originator = %q, want %q", gotOriginator, codexOriginator)
	}
	if snap.Provider != ProviderCodex {
		t.Fatalf("provider = %q", snap.Provider)
	}
}

func TestCodexFetchWithoutAuthFile(t *testing.T) {
	f := &CodexFetcher{CodexHome: t.TempDir()}
	_, err := f.Fetch(context.Background())
	if !errorIsMissingCredentials(err) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
}
