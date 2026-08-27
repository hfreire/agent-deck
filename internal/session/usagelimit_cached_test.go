package session

import (
	"testing"
	"time"
)

// seedUsageLimitMemo puts a formed verdict in the memo without touching the
// filesystem, so the cached-path tests exercise the wiring rather than the
// transcript walk.
func seedUsageLimitMemo(t *testing.T, inst *Instance, limited bool, scannedAt time.Time) {
	t.Helper()
	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.usageLimitSessionID = inst.ClaudeSessionID
	inst.usageLimitedCached = limited
	inst.lastUsageLimitScanAt = scannedAt
}

func TestCachedSubstate_ReportsUsageLimitFromMemo(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-cached", t.TempDir(), "claude")
	inst.ClaudeSessionID = "bound-session-id"
	seedUsageLimitMemo(t, inst, true, time.Now())

	if got := inst.CachedSubstate(); got != SubstateUsageLimit {
		t.Fatalf("CachedSubstate() = %q with a fresh usage-limit memo, want %q", got, SubstateUsageLimit)
	}
}

// The memo only refreshes while something polls this session. A supervisor
// reading an hours-old verdict as current is the same lie the detector's own
// usageLimitMaxAge bound exists to prevent, so the cached path applies it too.
func TestCachedSubstate_IgnoresStaleUsageLimitMemo(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-stale", t.TempDir(), "claude")
	inst.ClaudeSessionID = "bound-session-id"
	seedUsageLimitMemo(t, inst, true, time.Now().Add(-usageLimitMaxAge-time.Minute))

	if got := inst.CachedSubstate(); got == SubstateUsageLimit {
		t.Fatalf("CachedSubstate() = %q from a memo older than usageLimitMaxAge, want anything else", got)
	}
}

func TestCachedSubstate_IgnoresVerdictFormedForAnotherSession(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-rebound", t.TempDir(), "claude")
	inst.ClaudeSessionID = "session-b"
	inst.mu.Lock()
	inst.usageLimitSessionID = "session-a"
	inst.usageLimitedCached = true
	inst.lastUsageLimitScanAt = time.Now()
	inst.mu.Unlock()

	if got := inst.CachedSubstate(); got == SubstateUsageLimit {
		t.Fatalf("CachedSubstate() = %q from session-a's verdict, want anything else", got)
	}
}

func TestCachedSubstate_NoMemoIsNotUsageLimit(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-none", t.TempDir(), "claude")
	inst.ClaudeSessionID = "bound-session-id"

	if got := inst.CachedSubstate(); got == SubstateUsageLimit {
		t.Fatalf("CachedSubstate() = %q with no verdict at all, want anything else", got)
	}
}

// The cached path is only honest if something keeps the memo fresh. UpdateStatus
// is that something: it is what every status poll already calls, so wiring the
// refresh there covers the TUI sweep, the log workers and the daemon at once.
func TestUpdateStatusClaimsAUsageLimitScan(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-refresh", t.TempDir(), "claude")
	inst.ClaudeSessionID = "no-such-session-id"

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus() = %v", err)
	}

	inst.mu.RLock()
	stamped, gen := inst.lastUsageLimitScanAt, inst.usageLimitScanGen
	inst.mu.RUnlock()
	if stamped.IsZero() || gen == 0 {
		t.Fatal("UpdateStatus did not refresh the usage-limit memo (no scan claimed)")
	}
}

// A non-Claude session has no transcript of this shape, so the refresh must not
// even claim a scan window for it — this is the guard that keeps the added I/O
// off every other tool in a fleet.
func TestUpdateStatusSkipsUsageLimitScanForOtherTools(t *testing.T) {
	inst := NewInstanceWithTool("usage-limit-aider", t.TempDir(), "aider")
	inst.ClaudeSessionID = "no-such-session-id"

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus() = %v", err)
	}

	inst.mu.RLock()
	gen := inst.usageLimitScanGen
	inst.mu.RUnlock()
	if gen != 0 {
		t.Fatalf("usageLimitScanGen = %d for a non-Claude tool, want 0", gen)
	}
}
