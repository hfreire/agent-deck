package quota

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"
)

func codexRecordLine(timestamp string, planType string, windows string) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":1}},"rate_limits":{"limit_id":"codex","limit_name":null,%s"plan_type":%q}}}`,
		timestamp, windows, planType)
}

const codexBothWindows = `"primary":{"used_percent":13.5,"window_minutes":300,"resets_at":1793021976},"secondary":{"used_percent":2,"window_minutes":10080,"resets_at":1793608776},`

func writeRollout(t *testing.T, home, day, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating rollout dir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing rollout: %v", err)
	}
	return path
}

func TestReadCodexReportsBothWindows(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-a.jsonl",
		`{"timestamp":"2026-10-26T08:00:00.000Z","type":"session_meta","payload":{}}`,
		codexRecordLine("2026-10-26T08:41:18.737Z", "plus", codexBothWindows))

	snapshot, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	if snapshot.ID != ProviderCodex || snapshot.Label != "Codex" || snapshot.Plan != "plus" {
		t.Fatalf("snapshot identity: %+v", snapshot)
	}
	want, err := time.Parse(time.RFC3339, "2026-10-26T08:41:18.737Z")
	if err != nil {
		t.Fatalf("parsing want: %v", err)
	}
	if snapshot.UpdatedAt != want.Unix() {
		t.Fatalf("UpdatedAt = %d, want the record's own timestamp %d", snapshot.UpdatedAt, want.Unix())
	}
	if len(snapshot.Windows) != 2 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
	five := snapshot.Windows[0]
	if five.Kind != WindowFiveHour || five.Label != "5h" || five.UsedPercentage != 13.5 {
		t.Fatalf("five-hour window = %+v", five)
	}
	if five.ResetsAt == nil || *five.ResetsAt != 1793021976 {
		t.Fatalf("five-hour reset = %v", five.ResetsAt)
	}
	seven := snapshot.Windows[1]
	if seven.Kind != WindowSevenDay || seven.Label != "7d" || seven.UsedPercentage != 2 {
		t.Fatalf("seven-day window = %+v", seven)
	}
}

func TestReadCodexTakesNewestRecordAcrossFiles(t *testing.T) {
	home := t.TempDir()
	// Dated off the clock, not off a fixed day: a reading ahead of the clock
	// is deliberately ignored, so a hard-coded timestamp would make this test
	// depend on the day it runs.
	recent := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	earlier := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	newest := writeRollout(t, home, "2026/10/25", "rollout-old-mtime.jsonl",
		codexRecordLine(recent, "plus", `"primary":{"used_percent":44,"window_minutes":300,"resets_at":1793021976},`))
	older := writeRollout(t, home, "2026/10/26", "rollout-new-mtime.jsonl",
		codexRecordLine(earlier, "plus", `"primary":{"used_percent":11,"window_minutes":300,"resets_at":1793021976},`))
	// The file written last is not the one holding the newest reading: a
	// rollout's mtime moves with any write, so only the record's own
	// timestamp orders the readings.
	stamp := time.Now()
	if err := os.Chtimes(older, stamp, stamp); err != nil {
		t.Fatalf("touching rollout: %v", err)
	}
	if err := os.Chtimes(newest, stamp.Add(-time.Hour), stamp.Add(-time.Hour)); err != nil {
		t.Fatalf("touching rollout: %v", err)
	}

	snapshot, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	if snapshot.Windows[0].UsedPercentage != 44 {
		t.Fatalf("used the wrong record: %+v", snapshot.Windows)
	}
}

func TestReadCodexWithoutRateLimitsIsNotAFailure(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-a.jsonl",
		`{"timestamp":"2026-10-26T08:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{}}}`)

	snapshot, ok, err := ReadCodex(home)
	if err != nil {
		t.Fatalf("ReadCodex: %v", err)
	}
	if ok {
		t.Fatalf("ok = true for a rollout with no rate_limits: %+v", snapshot)
	}
}

func TestReadCodexWithoutSessionsIsNotAFailure(t *testing.T) {
	home := t.TempDir()
	if _, ok, err := ReadCodex(home); err != nil || ok {
		t.Fatalf("ReadCodex on a home with no sessions: ok=%v err=%v", ok, err)
	}
}

func TestReadCodexMissingHomeIsNotConfigured(t *testing.T) {
	_, _, err := ReadCodex(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestReadCodexFallsBackToCodexHomeEnv(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-a.jsonl",
		codexRecordLine("2026-10-26T08:41:18.737Z", "pro", codexBothWindows))
	t.Setenv("CODEX_HOME", home)

	snapshot, ok, err := ReadCodex("")
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	if snapshot.Plan != "pro" {
		t.Fatalf("plan = %q", snapshot.Plan)
	}
}

func TestReadCodexSkipsUnreadableRecords(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-a.jsonl",
		codexRecordLine("2026-10-26T08:00:00.000Z", "plus", codexBothWindows),
		`{"timestamp":"2026-10-26T08:10:00.000Z","payload":{"rate_limits":{"primary":{`,
		`{"timestamp":"not-a-timestamp","payload":{"rate_limits":{"primary":{"used_percent":99,"window_minutes":300,"resets_at":1}}}}`)

	snapshot, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	if snapshot.Windows[0].UsedPercentage != 13.5 {
		t.Fatalf("a corrupt or undatable record won: %+v", snapshot.Windows)
	}
}

func TestReadCodexNamesAnUnrecognisedWindow(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-a.jsonl",
		codexRecordLine("2026-10-26T08:41:18.737Z", "plus",
			`"primary":{"used_percent":5,"window_minutes":60,"resets_at":1793021976},"secondary":{"used_percent":7,"window_minutes":90},`))

	snapshot, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	if snapshot.Windows[0].Kind != WindowOther || snapshot.Windows[0].Label != "1h" {
		t.Fatalf("hour window = %+v", snapshot.Windows[0])
	}
	if snapshot.Windows[1].Kind != WindowOther || snapshot.Windows[1].Label != "90m" {
		t.Fatalf("odd window = %+v", snapshot.Windows[1])
	}
	if snapshot.Windows[1].ResetsAt != nil {
		t.Fatalf("a window with no reported reset must not carry one: %+v", snapshot.Windows[1])
	}
}

func TestReadCodexReadsOnlyTheTailOfARollout(t *testing.T) {
	home := t.TempDir()
	reading := codexRecordLine("2026-10-26T08:41:18.737Z", "plus", codexBothWindows)
	filler := strings.Repeat(`{"timestamp":"2026-10-26T07:00:00.000Z","type":"event_msg","payload":{"type":"agent_message"}}`+"\n", 64)

	t.Run("a reading inside the tail is read", func(t *testing.T) {
		path := writeRollout(t, home, "2026/10/26", "rollout-tail.jsonl")
		if err := os.WriteFile(path, []byte(filler+reading+"\n"), 0o644); err != nil {
			t.Fatalf("writing rollout: %v", err)
		}
		withCodexTailBytes(t, 512)
		if _, ok, err := ReadCodex(home); err != nil || !ok {
			t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
		}
	})

	t.Run("a reading older than the tail is not", func(t *testing.T) {
		path := writeRollout(t, home, "2026/10/26", "rollout-tail.jsonl")
		if err := os.WriteFile(path, []byte(reading+"\n"+filler), 0o644); err != nil {
			t.Fatalf("writing rollout: %v", err)
		}
		withCodexTailBytes(t, 512)
		if _, ok, err := ReadCodex(home); err != nil || ok {
			t.Fatalf("a record before the tail was read: ok=%v err=%v", ok, err)
		}
	})

	t.Run("the partial line the cut lands in is dropped", func(t *testing.T) {
		path := writeRollout(t, home, "2026/10/26", "rollout-tail.jsonl")
		// The cut falls inside the reading, leaving a JSON fragment that
		// still contains "rate_limits" — the case a naive tail would hand
		// to the decoder.
		if err := os.WriteFile(path, []byte(filler+reading+"\n"), 0o644); err != nil {
			t.Fatalf("writing rollout: %v", err)
		}
		withCodexTailBytes(t, int64(len(reading)/2))
		if _, ok, err := ReadCodex(home); err != nil || ok {
			t.Fatalf("a half-read record was decoded: ok=%v err=%v", ok, err)
		}
	})
}

func withCodexTailBytes(t *testing.T, max int64) {
	t.Helper()
	original := maxCodexTailBytes
	maxCodexTailBytes = max
	t.Cleanup(func() { maxCodexTailBytes = original })
}

func TestProviderLabelCodex(t *testing.T) {
	if got := ProviderLabel(ProviderCodex); got != "Codex" {
		t.Fatalf("ProviderLabel(codex) = %q", got)
	}
}

func TestReadCodexIgnoresAReadingDatedInTheFuture(t *testing.T) {
	home := t.TempDir()
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	writeRollout(t, home, "2026/10/26", "rollout-skew.jsonl",
		codexRecordLine("2026-10-26T08:41:18.737Z", "plus", codexBothWindows),
		codexRecordLine(future, "plus", `"primary":{"used_percent":99,"window_minutes":300},`))

	snapshot, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	// A reading dated ahead of this machine's clock can never go stale, so it
	// must not win selection against a real one.
	if snapshot.Windows[0].UsedPercentage != 13.5 {
		t.Fatalf("a future-dated record was taken as the newest reading: %+v", snapshot.Windows[0])
	}
	if snapshot.UpdatedAt > time.Now().Unix() {
		t.Fatalf("UpdatedAt = %d is in the future", snapshot.UpdatedAt)
	}
}

func TestReadCodexWithOnlyAFutureReadingReportsNothing(t *testing.T) {
	home := t.TempDir()
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	writeRollout(t, home, "2026/10/26", "rollout-skew.jsonl",
		codexRecordLine(future, "plus", codexBothWindows))

	if _, ok, err := ReadCodex(home); err != nil || ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
}

func TestReadCodexDropsEscapesFromThePlanName(t *testing.T) {
	home := t.TempDir()
	// \u001b as JSON spells it: Go's %q would emit \x1b, which is not JSON.
	writeRollout(t, home, "2026/10/26", "rollout-plan.jsonl",
		strings.Replace(codexRecordLine("2026-10-26T08:41:18.737Z", "plESCus", codexBothWindows),
			"plESCus", `pl\u001b[2Jus\t`, 1))

	snapshot, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ReadCodex: ok=%v err=%v", ok, err)
	}
	// The transcript is a local file and Plan is printed straight to the
	// terminal, so the escape that makes the rest of a sequence act on the
	// terminal must never reach the renderer. What is left is inert text.
	if strings.ContainsFunc(snapshot.Plan, func(r rune) bool { return !unicode.IsGraphic(r) }) {
		t.Fatalf("Plan = %q still carries control characters", snapshot.Plan)
	}
}

func TestReadCodexTruncatesAnOverlongPlanName(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-plan.jsonl",
		codexRecordLine("2026-10-26T08:41:18.737Z", strings.Repeat("x", 200), codexBothWindows))

	snapshot, _, err := ReadCodex(home)
	if err != nil {
		t.Fatalf("ReadCodex: %v", err)
	}
	if len(snapshot.Plan) != maxCodexPlanRunes {
		t.Fatalf("Plan is %d chars, want it capped at %d", len(snapshot.Plan), maxCodexPlanRunes)
	}
}

func TestReadCodexGivesUpOnAStuckSessionsTree(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, "2026/10/26", "rollout-a.jsonl",
		codexRecordLine("2026-10-26T08:41:18.737Z", "plus", codexBothWindows))

	// A readdir under the user's home has hung indefinitely before (#2674), and
	// `agent-deck usage` must report that rather than wedge behind it.
	release := make(chan struct{})
	finished := make(chan struct{})
	original := codexWalkDir
	codexWalkDir = func(root string, fn fs.WalkDirFunc) error {
		<-release
		close(finished)
		return nil
	}
	// Registered first, so it runs last: the abandoned walk has to be done
	// before the seam is put back, or the restore races with it.
	t.Cleanup(func() { codexWalkDir = original })
	t.Cleanup(func() { close(release); <-finished })
	originalTimeout := codexWalkTimeout
	codexWalkTimeout = 50 * time.Millisecond
	t.Cleanup(func() { codexWalkTimeout = originalTimeout })

	started := time.Now()
	_, ok, err := ReadCodex(home)
	if ok || !errors.Is(err, errCodexWalkTimedOut) {
		t.Fatalf("ReadCodex: ok=%v err=%v, want the timeout", ok, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("ReadCodex waited %s on a stuck walk", elapsed)
	}
}
