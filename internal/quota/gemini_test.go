package quota

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseGeminiQuota(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "gemini_quota.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := parseGeminiQuota(raw)
	if err != nil {
		t.Fatalf("parseGeminiQuota: %v", err)
	}
	// The bar shows the bucket closest to exhaustion: 3.0 Pro at 31%.
	if snap.Session == nil || snap.Session.UsedPercent != 31 {
		t.Fatalf("session = %+v, want 31%%", snap.Session)
	}
	if snap.Session.WindowMinutes != geminiWindowMinutes {
		t.Fatalf("window = %d, want %d", snap.Session.WindowMinutes, geminiWindowMinutes)
	}
	if !snap.Session.ResetsAt.Equal(time.Date(2026, 8, 27, 15, 10, 0, 0, time.UTC)) {
		t.Fatalf("reset = %v", snap.Session.ResetsAt)
	}
	if snap.Weekly != nil {
		t.Fatalf("gemini exposes no weekly window, got %+v", snap.Weekly)
	}
	if len(snap.Extra) != 3 {
		t.Fatalf("extra = %+v, want one entry per bucket", snap.Extra)
	}
	if snap.Extra[0].Name != "3.0 Pro" {
		t.Fatalf("first bucket name = %q, want %q", snap.Extra[0].Name, "3.0 Pro")
	}
}

func TestParseGeminiQuotaBareArray(t *testing.T) {
	snap, err := parseGeminiQuota([]byte(`[{"modelId":"gemini-4.0-ultra","remainingFraction":0.5,"resetTime":"2026-08-27T15:10:00Z"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session == nil || snap.Session.UsedPercent != 50 {
		t.Fatalf("session = %+v, want 50%%", snap.Session)
	}
	// Unknown model ids are humanized rather than dropped.
	if snap.Extra[0].Name != "4.0 Ultra" {
		t.Fatalf("name = %q, want %q", snap.Extra[0].Name, "4.0 Ultra")
	}
}

func TestParseGeminiQuotaNoBuckets(t *testing.T) {
	snap, err := parseGeminiQuota([]byte(`{"buckets":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if snap.Session != nil {
		t.Fatalf("session = %+v, want nil", snap.Session)
	}
}
