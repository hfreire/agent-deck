package ui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func quotaNow() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }

func claudeSnapshot(now time.Time) quota.Snapshot {
	return quota.Snapshot{
		Provider:  quota.ProviderClaude,
		Session:   &quota.Window{UsedPercent: 17, WindowMinutes: 300, ResetsAt: now.Add(3*time.Hour + 36*time.Minute)},
		Weekly:    &quota.Window{UsedPercent: 43, WindowMinutes: 10080, ResetsAt: now.Add(4*24*time.Hour + 13*time.Hour)},
		Extra:     []quota.NamedWindow{{Name: "Fable", Window: quota.Window{UsedPercent: 0, WindowMinutes: 10080}}},
		UpdatedAt: now,
	}
}

func codexSnapshot(now time.Time) quota.Snapshot {
	return quota.Snapshot{
		Provider:  quota.ProviderCodex,
		Session:   &quota.Window{UsedPercent: 0, WindowMinutes: 300, ResetsAt: now.Add(4*time.Hour + 59*time.Minute)},
		Weekly:    &quota.Window{UsedPercent: 6, WindowMinutes: 10080, ResetsAt: now.Add(5*24*time.Hour + 20*time.Hour)},
		UpdatedAt: now,
	}
}

func TestRenderQuotaBarFullWidth(t *testing.T) {
	now := quotaNow()
	got := stripAnsi(renderQuotaBar([]quota.Snapshot{claudeSnapshot(now), codexSnapshot(now)}, 200, now))

	for _, want := range []string{"QUOTA", "[CLAUDE]", "17% 3h 36m", "43% 4d 13h", "0% Fable", "[CODEX]", "0% 4h 59m", "6% 5d 20h"} {
		if !strings.Contains(got, want) {
			t.Fatalf("bar %q is missing %q", got, want)
		}
	}
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("bar must be a single line, got %q", got)
	}
}

// Each narrower terminal drops the next-least-useful piece rather than
// truncating mid-number.
func TestRenderQuotaBarDegradesWithWidth(t *testing.T) {
	now := quotaNow()
	snaps := []quota.Snapshot{claudeSnapshot(now), codexSnapshot(now)}

	tests := []struct {
		width      int
		wantAbsent []string
		wantPresen []string
	}{
		{width: 85, wantAbsent: []string{"Fable"}, wantPresen: []string{"17% 3h 36m", "43%"}},
		{width: 70, wantAbsent: []string{"Fable", "3h 36m"}, wantPresen: []string{"17%", "43%"}},
		// The meter is decoration; the weekly number outranks it.
		{width: 46, wantAbsent: []string{"░"}, wantPresen: []string{"QUOTA", "17%", "43%"}},
		{width: 30, wantAbsent: []string{"QUOTA", "43%", "6%"}, wantPresen: []string{"[CLAUDE]", "17%", "0%"}},
	}
	for _, tc := range tests {
		got := stripAnsi(renderQuotaBar(snaps, tc.width, now))
		if w := lipgloss.Width(got); w > tc.width {
			t.Fatalf("width %d: bar is %d wide: %q", tc.width, w, got)
		}
		for _, absent := range tc.wantAbsent {
			if strings.Contains(got, absent) {
				t.Fatalf("width %d: bar %q should have dropped %q", tc.width, got, absent)
			}
		}
		for _, present := range tc.wantPresen {
			if !strings.Contains(got, present) {
				t.Fatalf("width %d: bar %q should still show %q", tc.width, got, present)
			}
		}
	}
}

func TestRenderQuotaBarHidesWhenTooNarrow(t *testing.T) {
	now := quotaNow()
	if got := renderQuotaBar([]quota.Snapshot{claudeSnapshot(now)}, 4, now); got != "" {
		t.Fatalf("bar = %q, want empty when it cannot fit", got)
	}
}

func TestRenderQuotaBarSkipsProvidersWithoutWindows(t *testing.T) {
	now := quotaNow()
	broken := quota.Snapshot{Provider: quota.ProviderGemini, Err: "network down", UpdatedAt: now}
	got := stripAnsi(renderQuotaBar([]quota.Snapshot{claudeSnapshot(now), broken}, 200, now))
	if strings.Contains(got, quotaLabel(quota.ProviderGemini)) {
		t.Fatalf("bar %q should omit a provider with no numbers", got)
	}
}

func TestRenderQuotaBarEmptyInput(t *testing.T) {
	if got := renderQuotaBar(nil, 200, quotaNow()); got != "" {
		t.Fatalf("bar = %q, want empty", got)
	}
}

func TestQuotaLabel(t *testing.T) {
	tests := map[string]string{
		quota.ProviderClaude: "[CLAUDE]",
		quota.ProviderCodex:  "[CODEX]",
		quota.ProviderGemini: "[GEMINI]",
		"mystery":            "[MYSTERY]",
	}
	for provider, want := range tests {
		if got := quotaLabel(provider); got != want {
			t.Fatalf("quotaLabel(%q) = %q, want %q", provider, got, want)
		}
	}
}

// The block is a rule plus the data row; the rule must span the terminal so it
// lines up with the help bar's own border.
func TestHomeQuotaBarBlock(t *testing.T) {
	now := quotaNow()
	h := &Home{width: 60, quotaMode: session.QuotaBarAuto, quotaSnaps: []quota.Snapshot{claudeSnapshot(now)}}
	lines := strings.Split(stripAnsi(h.quotaBarBlock()), "\n")
	if len(lines) != 2 {
		t.Fatalf("block = %q, want 2 lines", lines)
	}
	if lines[0] != strings.Repeat("─", 60) {
		t.Fatalf("top line = %q, want a full-width rule", lines[0])
	}
	if !strings.HasPrefix(lines[1], "QUOTA") {
		t.Fatalf("data line = %q, want the QUOTA title first", lines[1])
	}

	// In the dual layout the rule stops at the divider so it reads as part of
	// the sessions column, not as a lid over the PREVIEW pane.
	dual := &Home{width: 200, quotaMode: session.QuotaBarAuto, quotaSnaps: []quota.Snapshot{claudeSnapshot(now)}}
	dualLines := strings.Split(stripAnsi(dual.quotaBarBlock()), "\n")
	if want := dual.sessionsPaneWidth(); lipgloss.Width(dualLines[0]) != want {
		t.Fatalf("dual top line is %d wide, want the sessions pane width %d", lipgloss.Width(dualLines[0]), want)
	}

	off := &Home{width: 60, quotaMode: session.QuotaBarOff, quotaSnaps: []quota.Snapshot{claudeSnapshot(now)}}
	if got := off.quotaBarBlock(); got != "" {
		t.Fatalf("block = %q, want empty when disabled", got)
	}
}

// Healthy usage must not shout: only the warning bands carry a colour.
func TestQuotaColorIsQuietUntilItMatters(t *testing.T) {
	tests := []struct {
		pct  float64
		want lipgloss.Color
	}{
		{0, ColorText},
		{49, ColorText},
		{50, ColorYellow},
		{80, ColorRed},
	}
	for _, tc := range tests {
		if got := quotaColor(tc.pct); got != tc.want {
			t.Fatalf("quotaColor(%v) = %v, want %v", tc.pct, got, tc.want)
		}
	}
}

// The QUOTA title is a panel title like SESSIONS, and must stay in step with it.
func TestQuotaTitleMatchesPanelTitle(t *testing.T) {
	panel := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render("SESSIONS")
	if got := quotaTitleStyle().Render("SESSIONS"); got != panel {
		t.Fatalf("quota title style = %q, want the panel title style %q", got, panel)
	}
}

func TestFormatQuotaReset(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{-time.Hour, ""},
		{30 * time.Second, "<1m"},
		{41 * time.Minute, "41m"},
		{3*time.Hour + 36*time.Minute, "3h 36m"},
		{4*24*time.Hour + 13*time.Hour, "4d 13h"},
	}
	for _, tc := range tests {
		if got := formatQuotaReset(tc.in); got != tc.want {
			t.Fatalf("formatQuotaReset(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestQuotaMeter(t *testing.T) {
	tests := []struct {
		pct  float64
		want string
	}{
		{0, "░░░░░░"},
		{50, "███░░░"},
		{100, "██████"},
		// A window that has barely started still shows a sliver, so "in use"
		// never looks identical to "untouched".
		{1, "█░░░░░"},
	}
	for _, tc := range tests {
		if got := quotaMeter(tc.pct); got != tc.want {
			t.Fatalf("quotaMeter(%v) = %q, want %q", tc.pct, got, tc.want)
		}
	}
}

func TestHomeQuotaBarLine(t *testing.T) {
	now := quotaNow()
	snaps := []quota.Snapshot{claudeSnapshot(now), codexSnapshot(now)}

	t.Run("renders when enabled", func(t *testing.T) {
		h := &Home{width: 200, quotaMode: session.QuotaBarAuto, quotaSnaps: snaps}
		if got := stripAnsi(h.quotaBarLine()); !strings.Contains(got, "17%") {
			t.Fatalf("quotaBarLine() = %q, want claude usage", got)
		}
	})

	t.Run("off suppresses the line", func(t *testing.T) {
		h := &Home{width: 200, quotaMode: session.QuotaBarOff, quotaSnaps: snaps}
		if got := h.quotaBarLine(); got != "" {
			t.Fatalf("quotaBarLine() = %q, want empty when disabled", got)
		}
	})

	t.Run("no snapshots means no line", func(t *testing.T) {
		h := &Home{width: 200, quotaMode: session.QuotaBarAuto}
		if got := h.quotaBarLine(); got != "" {
			t.Fatalf("quotaBarLine() = %q, want empty", got)
		}
	})
}

// The bar must claim a row in the height budget only when it is actually
// drawn, otherwise the session list loses a row to nothing.
func TestHomeQuotaBarHeight(t *testing.T) {
	now := quotaNow()
	with := &Home{width: 200, quotaMode: session.QuotaBarAuto, quotaSnaps: []quota.Snapshot{claudeSnapshot(now)}}
	if got := with.quotaBarHeight(); got != 2 {
		t.Fatalf("quotaBarHeight() = %d, want 2", got)
	}
	without := &Home{width: 200, quotaMode: session.QuotaBarOff, quotaSnaps: []quota.Snapshot{claudeSnapshot(now)}}
	if got := without.quotaBarHeight(); got != 0 {
		t.Fatalf("quotaBarHeight() = %d, want 0", got)
	}
}

func TestHomeRefreshQuotaSnapshotsOrdersProviders(t *testing.T) {
	now := quotaNow()
	store := quota.NewStore(filepath.Join(t.TempDir(), "quota.json"))
	store.Set(codexSnapshot(now))
	store.Set(claudeSnapshot(now))

	h := &Home{width: 200, quotaMode: session.QuotaBarAuto, quotaStore: store}
	h.refreshQuotaSnapshots()

	if len(h.quotaSnaps) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(h.quotaSnaps))
	}
	if h.quotaSnaps[0].Provider != quota.ProviderClaude || h.quotaSnaps[1].Provider != quota.ProviderCodex {
		t.Fatalf("providers out of order: %v", h.quotaSnaps)
	}
}
