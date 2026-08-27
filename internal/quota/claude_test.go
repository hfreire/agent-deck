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

func TestParseClaudeUsage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "claude_usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := parseClaudeUsage(raw)
	if err != nil {
		t.Fatalf("parseClaudeUsage: %v", err)
	}
	if snap.Session == nil || snap.Session.UsedPercent != 17 {
		t.Fatalf("session = %+v, want 17%%", snap.Session)
	}
	if snap.Session.WindowMinutes != SessionWindowMinutes {
		t.Fatalf("session window = %d, want %d", snap.Session.WindowMinutes, SessionWindowMinutes)
	}
	wantReset := time.Date(2026, 8, 27, 16, 29, 59, 729146000, time.UTC)
	if !snap.Session.ResetsAt.Equal(wantReset) {
		t.Fatalf("session reset = %v, want %v", snap.Session.ResetsAt, wantReset)
	}
	if snap.Weekly == nil || snap.Weekly.UsedPercent != 43 {
		t.Fatalf("weekly = %+v, want 43%%", snap.Weekly)
	}
	if len(snap.Extra) != 1 || snap.Extra[0].Name != "Fable" || snap.Extra[0].UsedPercent != 0 {
		t.Fatalf("extra = %+v, want one Fable window at 0%%", snap.Extra)
	}
}

// The scoped weekly caps arrive only in limits[]; a payload carrying just the
// legacy top-level windows must still map.
func TestParseClaudeUsageWithoutLimitsArray(t *testing.T) {
	snap, err := parseClaudeUsage([]byte(`{
		"five_hour": {"utilization": 12.0, "resets_at": "2026-08-27T16:00:00Z"},
		"seven_day": {"used_percentage": 42.0, "resets_at": null}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == nil || snap.Session.UsedPercent != 12 {
		t.Fatalf("session = %+v, want 12%%", snap.Session)
	}
	if snap.Weekly == nil || snap.Weekly.UsedPercent != 42 {
		t.Fatalf("weekly = %+v, want 42%%", snap.Weekly)
	}
	if !snap.Weekly.ResetsAt.IsZero() {
		t.Fatalf("weekly reset = %v, want zero", snap.Weekly.ResetsAt)
	}
}

func TestClaudeFetchSendsOAuthHeaders(t *testing.T) {
	var gotAuth, gotBeta, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		gotAgent = r.Header.Get("User-Agent")
		raw, _ := os.ReadFile(filepath.Join("testdata", "claude_usage.json"))
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	f := &ClaudeFetcher{
		UsageURL:       srv.URL,
		HTTPClient:     srv.Client(),
		readCredential: func() (claudeCredential, error) { return claudeCredential{token: "tok-123"}, nil },
	}
	snap, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBeta != claudeOAuthBeta {
		t.Fatalf("anthropic-beta = %q, want %q", gotBeta, claudeOAuthBeta)
	}
	if gotAgent != claudeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotAgent, claudeUserAgent)
	}
	if snap.Provider != ProviderClaude || snap.Session == nil {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestClaudeFetchUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	f := &ClaudeFetcher{
		UsageURL:       srv.URL,
		HTTPClient:     srv.Client(),
		readCredential: func() (claudeCredential, error) { return claudeCredential{token: "stale"}, nil },
	}
	if _, err := f.Fetch(context.Background()); err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
}

func TestClaudeTokenFromCredentialsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	body := `{"claudeAiOauth":{"accessToken":"file-token","expiresAt":1787837178798}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cred, err := claudeCredentialFromFile(path)
	if err != nil {
		t.Fatalf("claudeCredentialFromFile: %v", err)
	}
	if tok := cred.token; tok != "file-token" {
		t.Fatalf("token = %q, want file-token", tok)
	}
}

func TestClaudeTokenFromCredentialsFileMissingOAuth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(path, []byte(`{"mcpOAuth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeCredentialFromFile(path); err == nil {
		t.Fatal("expected an error when claudeAiOauth is absent")
	}
}
