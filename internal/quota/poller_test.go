package quota

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeFetcher struct {
	provider string
	calls    atomic.Int32
	snap     Snapshot
	err      error
}

func (f *fakeFetcher) Provider() string { return f.provider }

func (f *fakeFetcher) Fetch(context.Context) (Snapshot, error) {
	f.calls.Add(1)
	if f.err != nil {
		return Snapshot{}, f.err
	}
	return f.snap, nil
}

func newTestPoller(t *testing.T, fetchers ...Fetcher) *Poller {
	t.Helper()
	return &Poller{
		Store:    NewStore(filepath.Join(t.TempDir(), "quota.json")),
		Fetchers: fetchers,
	}
}

func TestPollerRefreshStoresSnapshots(t *testing.T) {
	ok := &fakeFetcher{provider: ProviderClaude, snap: Snapshot{Provider: ProviderClaude, Session: &Window{UsedPercent: 17}}}
	p := newTestPoller(t, ok)
	p.Refresh(context.Background())

	got, found := p.Store.Get(ProviderClaude)
	if !found || got.Session == nil || got.Session.UsedPercent != 17 {
		t.Fatalf("snapshot = %+v found=%v", got, found)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt not stamped")
	}
}

func TestPollerRecordsFetchErrors(t *testing.T) {
	bad := &fakeFetcher{provider: ProviderCodex, err: errors.New("network down")}
	p := newTestPoller(t, bad)
	p.Refresh(context.Background())

	got, _ := p.Store.Get(ProviderCodex)
	if got.Err == "" {
		t.Fatalf("snapshot = %+v, want an error recorded", got)
	}
}

// A provider with no credentials is not an error the user should see; it is
// simply absent from the bar, and must not be retried on every tick either.
func TestPollerSkipsProvidersWithoutCredentials(t *testing.T) {
	none := &fakeFetcher{provider: ProviderGemini, err: ErrNoCredentials}
	p := newTestPoller(t, none)
	p.Refresh(context.Background())

	if _, found := p.Store.Get(ProviderGemini); found {
		t.Fatal("a provider without credentials must not be stored")
	}
}

func TestPollerBacksOffAfterFailure(t *testing.T) {
	bad := &fakeFetcher{provider: ProviderCodex, err: errors.New("boom")}
	p := newTestPoller(t, bad)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }

	p.Refresh(context.Background())
	p.Refresh(context.Background())
	if got := bad.calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1 — the second tick is inside the backoff window", got)
	}

	now = now.Add(defaultBackoffStep + time.Second)
	p.Refresh(context.Background())
	if got := bad.calls.Load(); got != 2 {
		t.Fatalf("fetch calls = %d, want 2 once the backoff has elapsed", got)
	}
}

func TestPollerBackoffClearsAfterSuccess(t *testing.T) {
	f := &fakeFetcher{provider: ProviderClaude, err: errors.New("boom")}
	p := newTestPoller(t, f)
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }

	p.Refresh(context.Background())
	now = now.Add(defaultBackoffStep + time.Second)
	f.err = nil
	f.snap = Snapshot{Provider: ProviderClaude, Session: &Window{UsedPercent: 1}}
	p.Refresh(context.Background())
	p.Refresh(context.Background())

	if got := f.calls.Load(); got != 3 {
		t.Fatalf("fetch calls = %d, want 3 — a success must clear the backoff", got)
	}
}
