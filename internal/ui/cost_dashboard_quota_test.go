package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

func quotaWindowAt(percent float64, resetsAt time.Time) *quota.Window {
	return &quota.Window{UsedPercent: percent, ResetsAt: resetsAt}
}

func TestRenderQuotaSectionIsEmptyWithoutSnapshots(t *testing.T) {
	if got := renderQuotaSection(nil, 80, time.Now()); got != "" {
		t.Fatalf("renderQuotaSection(nil) = %q, want empty", got)
	}
}

func TestRenderQuotaSectionShowsEveryWindow(t *testing.T) {
	now := time.Now()
	snaps := []quota.Snapshot{{
		Provider:  quota.ProviderClaude,
		Plan:      "max",
		Session:   quotaWindowAt(42, now.Add(3*time.Hour+36*time.Minute)),
		Weekly:    quotaWindowAt(45, now.Add(4*24*time.Hour)),
		Extra:     []quota.NamedWindow{{Name: "Fable", Window: quota.Window{UsedPercent: 0}}},
		UpdatedAt: now,
	}}

	out := stripAnsi(renderQuotaSection(snaps, 100, now))
	for _, want := range []string{"Plan Usage", "[CLAUDE]", "max", "Session", "42%", "3h 36m", "Weekly", "45%", "Fable", "0%"} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderQuotaSection missing %q:\n%s", want, out)
		}
	}
}

// The dashboard has room for the whole story, so unlike the footer bar a
// provider that failed is reported rather than hidden — a silent gap reads as
// "you are not using it".
func TestRenderQuotaSectionReportsAFailedProvider(t *testing.T) {
	now := time.Now()
	snaps := []quota.Snapshot{{
		Provider:  quota.ProviderGemini,
		Err:       "quota: provider not signed in",
		UpdatedAt: now,
	}}

	out := stripAnsi(renderQuotaSection(snaps, 100, now))
	if !strings.Contains(out, "[GEMINI]") || !strings.Contains(out, "not signed in") {
		t.Fatalf("failed provider not reported:\n%s", out)
	}
}

func TestRenderQuotaSectionMarksStaleNumbers(t *testing.T) {
	now := time.Now()
	snaps := []quota.Snapshot{{
		Provider:  quota.ProviderCodex,
		Session:   quotaWindowAt(10, time.Time{}),
		UpdatedAt: now.Add(-quotaStaleAfter - time.Minute),
	}}

	out := stripAnsi(renderQuotaSection(snaps, 100, now))
	if !strings.Contains(out, "stale") {
		t.Fatalf("stale snapshot not marked:\n%s", out)
	}
}

func TestRenderQuotaSectionFitsWidth(t *testing.T) {
	now := time.Now()
	snaps := []quota.Snapshot{{
		Provider:  quota.ProviderClaude,
		Plan:      "max",
		Session:   quotaWindowAt(99, now.Add(72*time.Hour)),
		UpdatedAt: now,
	}}

	for _, width := range []int{40, 60, 120} {
		for _, line := range strings.Split(renderQuotaSection(snaps, width, now), "\n") {
			if got := cellWidth(stripAnsi(line)); got > width {
				t.Fatalf("width %d: line %q is %d cells", width, line, got)
			}
		}
	}
}

func TestCostDashboardIncludesQuota(t *testing.T) {
	now := time.Now()
	d := costDashboard{
		width:  100,
		height: 40,
		quota: []quota.Snapshot{{
			Provider:  quota.ProviderClaude,
			Session:   quotaWindowAt(42, now.Add(time.Hour)),
			UpdatedAt: now,
		}},
	}
	out := stripAnsi(d.View())
	if !strings.Contains(out, "Plan Usage") || !strings.Contains(out, "42%") {
		t.Fatalf("cost dashboard did not render the quota section:\n%s", out)
	}
}

// No quota data must not leave an empty heading behind.
func TestCostDashboardWithoutQuotaHasNoSection(t *testing.T) {
	d := costDashboard{width: 100, height: 40}
	if strings.Contains(stripAnsi(d.View()), "Plan Usage") {
		t.Fatal("cost dashboard rendered a Plan Usage heading with no providers")
	}
}
