package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestRowQuotaBadge(t *testing.T) {
	tests := []struct {
		name     string
		substate session.Substate
		want     string
	}{
		{"usage limit", session.SubstateUsageLimit, " ⚠ quota"},
		{"auth 401 is a different problem", session.SubstateAuth401, ""},
		{"working", session.SubstateRunning, ""},
		{"none", session.SubstateNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripAnsi(rowQuotaBadge(tt.substate, false)); got != tt.want {
				t.Fatalf("rowQuotaBadge(%q) = %q, want %q", tt.substate, got, tt.want)
			}
		})
	}
}

// The badge sits inside the selection highlight on the selected row, so it has
// to take the selection style like every other badge — otherwise its own
// foreground fights the highlight bar and the text goes unreadable.
func TestRowQuotaBadgeUsesSelectionStyle(t *testing.T) {
	plain := rowQuotaBadge(session.SubstateUsageLimit, false)
	selected := rowQuotaBadge(session.SubstateUsageLimit, true)
	if stripAnsi(plain) != stripAnsi(selected) {
		t.Fatalf("selection changed the badge text: %q vs %q", stripAnsi(plain), stripAnsi(selected))
	}
	if want := SessionStatusSelStyle.Render(" ⚠ quota"); selected != want {
		t.Fatalf("selected badge = %q, want SessionStatusSelStyle's %q", selected, want)
	}
	if !strings.Contains(stripAnsi(plain), "quota") {
		t.Fatalf("badge lost its text: %q", plain)
	}
}

// The glyph is a badge, not a status override: a usage-limited session is idle
// in every way the pane reports, and replacing ○ would hide that.
func TestUsageLimitDoesNotChangeTheRowGlyph(t *testing.T) {
	icon, _ := rowStatusGlyph(session.StatusIdle, session.SubstateUsageLimit, false)
	if icon != "○" {
		t.Fatalf("rowStatusGlyph = %q for an idle usage-limited row, want ○", icon)
	}
}

// Row-level pin: the badge is only useful if the row actually carries it, and a
// helper that renders correctly while unwired renders nothing at all.
func TestSessionRowCarriesTheQuotaBadge(t *testing.T) {
	inst := session.NewInstanceWithTool("quota-badge-row", t.TempDir(), "claude")
	h := &Home{width: 140}
	item := session.Item{
		Type:          session.ItemTypeSession,
		Session:       inst,
		Level:         1,
		Path:          "test",
		IsLastInGroup: true,
	}
	snapshot := map[string]sessionRenderState{
		inst.ID: {status: session.StatusIdle, substate: session.SubstateUsageLimit, tool: "claude"},
	}

	var b strings.Builder
	h.renderSessionItem(&b, item, false, snapshot, h.width)
	if got := stripAnsi(b.String()); !strings.Contains(got, "⚠ quota") {
		t.Fatalf("row is missing the quota badge:\n%s", got)
	}

	snapshot[inst.ID] = sessionRenderState{status: session.StatusIdle, tool: "claude"}
	b.Reset()
	h.renderSessionItem(&b, item, false, snapshot, h.width)
	if got := stripAnsi(b.String()); strings.Contains(got, "quota") {
		t.Fatalf("row carries a quota badge with no usage-limit substate:\n%s", got)
	}
}
