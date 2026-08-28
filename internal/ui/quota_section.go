package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

const (
	quotaSectionTitle      = "Plan Usage"
	quotaSectionIndent     = "    "
	quotaSectionMeterCells = 20
	quotaSectionLabelMax   = 12
	quotaSectionGap        = 2
)

// quotaSectionWindows flattens a snapshot into the rows to draw, in the order a
// reader needs them: the window that bites first, then the long one, then any
// scoped caps.
func quotaSectionWindows(snap quota.Snapshot) []quota.NamedWindow {
	rows := make([]quota.NamedWindow, 0, 2+len(snap.Extra))
	if snap.Session != nil {
		rows = append(rows, quota.NamedWindow{Name: "Session", Window: *snap.Session})
	}
	if snap.Weekly != nil {
		rows = append(rows, quota.NamedWindow{Name: "Weekly", Window: *snap.Weekly})
	}
	return append(rows, snap.Extra...)
}

func quotaSectionLabelWidth(rows []quota.NamedWindow) int {
	width := 0
	for _, row := range rows {
		if n := cellWidth(row.Name); n > width {
			width = n
		}
	}
	return min(width, quotaSectionLabelMax)
}

// quotaSectionHeading names the provider, its plan, and — unlike the footer bar,
// which has no room to explain itself — why a provider has no numbers.
func quotaSectionHeading(snap quota.Snapshot, width int, now time.Time) string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	head := lipgloss.NewStyle().Foreground(ColorText).Bold(true).Render("  " + quotaLabel(snap.Provider, false))
	if snap.Plan != "" {
		head += dimStyle.Render(" " + snap.Plan)
	}
	if snap.Err != "" {
		head += lipgloss.NewStyle().Foreground(ColorYellow).Render(" — " + snap.Err)
	} else if quotaSnapshotStale(snap, now) {
		head += dimStyle.Render(" (stale)")
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(head) + "\n"
}

func quotaSnapshotStale(snap quota.Snapshot, now time.Time) bool {
	return snap.UpdatedAt.IsZero() || now.Sub(snap.UpdatedAt) > quotaStaleAfter
}

// quotaSectionRow draws one window. The meter takes whatever width is left after
// the numbers, and is dropped entirely rather than squeezed to a couple of cells
// where it would read as a value instead of a bar.
func quotaSectionRow(row quota.NamedWindow, labelWidth, width int, dim bool, now time.Time) string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	style := lipgloss.NewStyle().Foreground(quotaColor(row.UsedPercent))
	if dim {
		style = dimStyle
	}

	label := row.Name
	if cellWidth(label) > labelWidth {
		label = cellTruncate(label, labelWidth, "…")
	}
	label += strings.Repeat(" ", max(0, labelWidth-cellWidth(label)))
	percent := fmt.Sprintf("%3.0f%%", row.UsedPercent)

	prefix := quotaSectionIndent + label + strings.Repeat(" ", quotaSectionGap)
	used := cellWidth(prefix) + len(percent)
	out := dimStyle.Render(prefix)

	if cells := min(quotaSectionMeterCells, width-used-quotaSectionGap); cells >= 4 {
		out += style.Render(quotaMeterWidth(row.UsedPercent, cells)) + strings.Repeat(" ", quotaSectionGap)
		used += cells + quotaSectionGap
	}
	out += style.Render(percent)

	if reset := formatQuotaReset(row.ResetsIn(now)); reset != "" {
		text := strings.Repeat(" ", quotaSectionGap) + "resets in " + reset
		if used+cellWidth(text) <= width {
			out += dimStyle.Render(text)
		}
	}
	return out + "\n"
}

// renderQuotaSection is the cost dashboard's per-provider plan usage, where
// there is room for every window at full width. Returns "" when the poller has
// nothing at all, so the dashboard never shows an empty heading.
func renderQuotaSection(snaps []quota.Snapshot, width int, now time.Time) string {
	if len(snaps) == 0 || width <= 0 {
		return ""
	}

	var b strings.Builder
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorText).Underline(true)
	b.WriteString("  " + sectionStyle.Render(quotaSectionTitle) + "\n")

	for _, snap := range snaps {
		b.WriteString(quotaSectionHeading(snap, width, now))
		rows := quotaSectionWindows(snap)
		labelWidth := quotaSectionLabelWidth(rows)
		dim := snap.Err != "" || quotaSnapshotStale(snap, now)
		for _, row := range rows {
			b.WriteString(quotaSectionRow(row, labelWidth, width, dim, now))
		}
	}
	return b.String()
}
