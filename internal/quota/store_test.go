package quota

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	s := NewStore(path)
	reset := time.Date(2026, 8, 27, 16, 0, 0, 0, time.UTC)
	s.Set(Snapshot{
		Provider:  ProviderClaude,
		Session:   &Window{UsedPercent: 17, WindowMinutes: 300, ResetsAt: reset},
		Weekly:    &Window{UsedPercent: 43, WindowMinutes: 10080},
		UpdatedAt: reset,
	})
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded := NewStore(path)
	if err := loaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := loaded.Get(ProviderClaude)
	if !ok {
		t.Fatal("claude snapshot missing after reload")
	}
	if got.Session == nil || got.Session.UsedPercent != 17 || !got.Session.ResetsAt.Equal(reset) {
		t.Fatalf("session = %+v", got.Session)
	}
	if got.Weekly == nil || got.Weekly.UsedPercent != 43 {
		t.Fatalf("weekly = %+v", got.Weekly)
	}
}

func TestStoreLoadMissingFileIsNotAnError(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "absent.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("Load of a missing cache: %v", err)
	}
	if len(s.All()) != 0 {
		t.Fatalf("All = %v, want empty", s.All())
	}
}

// A failed refresh must not erase the last good numbers — a stale bar beats a
// blank one, and the UI dims it using UpdatedAt.
func TestStoreSetErrorKeepsLastGoodWindows(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "quota.json"))
	good := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	s.Set(Snapshot{Provider: ProviderCodex, Session: &Window{UsedPercent: 5}, UpdatedAt: good})
	s.Set(Snapshot{Provider: ProviderCodex, Err: "boom", UpdatedAt: good.Add(time.Minute)})

	got, _ := s.Get(ProviderCodex)
	if got.Session == nil || got.Session.UsedPercent != 5 {
		t.Fatalf("session = %+v, want the last good 5%%", got.Session)
	}
	if got.Err != "boom" {
		t.Fatalf("err = %q, want boom", got.Err)
	}
	if !got.UpdatedAt.Equal(good) {
		t.Fatalf("UpdatedAt = %v, want the last good fetch time %v", got.UpdatedAt, good)
	}
}
