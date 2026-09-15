package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

const (
	quotaMeterCells   = 6
	quotaMeterFull    = "█"
	quotaMeterEmpty   = "░"
	quotaSeparator    = "   "
	quotaTitle        = "QUOTA"
	quotaTitleGap     = "  "
	quotaWindowJoiner = " · "
	quotaStaleAfter   = 10 * time.Minute
)

// quotaTier is how much of each provider survives at the current terminal
// width, widest first. Whole fields are dropped rather than truncated so a
// number is never shown half-rendered.
type quotaTier int

const (
	quotaTierFull quotaTier = iota
	quotaTierShortLabels
	quotaTierNoExtras
	quotaTierNoResets
	quotaTierNoMeter
	quotaTierSessionOnly
	quotaTierNoTitle
)

var quotaTiers = []quotaTier{
	quotaTierFull,
	quotaTierShortLabels,
	quotaTierNoExtras,
	quotaTierNoResets,
	quotaTierNoMeter,
	quotaTierSessionOnly,
	quotaTierNoTitle,
}

// quotaLabel names the provider. The abbreviation is the first two letters
// rather than a per-provider table, so a provider we do not know about yet
// still shrinks.
func quotaLabel(provider string, short bool) string {
	name := strings.ToUpper(provider)
	if short {
		name = string([]rune(name)[:min(2, len([]rune(name)))])
	}
	return "[" + name + "]"
}

// quotaTitleStyle mirrors renderPanelTitle so QUOTA reads as a section title
// alongside SESSIONS and PREVIEW.
func quotaTitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
}

func quotaMeter(percent float64) string {
	filled := int(math.Ceil(percent / 100 * quotaMeterCells))
	if filled < 0 {
		filled = 0
	}
	if filled > quotaMeterCells {
		filled = quotaMeterCells
	}
	return strings.Repeat(quotaMeterFull, filled) + strings.Repeat(quotaMeterEmpty, quotaMeterCells-filled)
}

func quotaColor(percent float64) lipgloss.Color {
	switch {
	case percent >= 80:
		return ColorRed
	case percent >= 50:
		return ColorYellow
	default:
		return ColorText
	}
}

func formatQuotaReset(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return "<1m"
	}
}

// quotaLine accumulates the styled bar alongside a plain copy, so width can be
// measured without paying for an ANSI strip on every tier attempt.
type quotaLine struct {
	plain  strings.Builder
	styled strings.Builder
}

func (l *quotaLine) add(text string, style lipgloss.Style) {
	l.plain.WriteString(text)
	l.styled.WriteString(style.Render(text))
}

func quotaWindowText(w quota.Window, showReset bool, now time.Time) string {
	text := fmt.Sprintf("%.0f%%", w.UsedPercent)
	if !showReset {
		return text
	}
	if reset := formatQuotaReset(w.ResetsIn(now)); reset != "" {
		text += " " + reset
	}
	return text
}

func quotaAppendSegment(line *quotaLine, snap quota.Snapshot, tier quotaTier, now time.Time) {
	dim := snap.Err != "" || snap.UpdatedAt.IsZero() || now.Sub(snap.UpdatedAt) > quotaStaleAfter
	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	style := func(percent float64) lipgloss.Style {
		if dim {
			return dimStyle
		}
		return lipgloss.NewStyle().Foreground(quotaColor(percent))
	}

	primary := snap.Session
	if primary == nil {
		primary = snap.Weekly
	}

	line.add(quotaLabel(snap.Provider, tier >= quotaTierShortLabels), dimStyle)
	line.add(" ", dimStyle)
	if tier < quotaTierNoMeter {
		line.add(quotaMeter(primary.UsedPercent), style(primary.UsedPercent))
		line.add(" ", dimStyle)
	}

	showReset := tier <= quotaTierNoExtras
	line.add(quotaWindowText(*primary, showReset, now), style(primary.UsedPercent))

	if tier >= quotaTierSessionOnly || snap.Weekly == nil || snap.Weekly == primary {
		return
	}
	line.add(quotaWindowJoiner, dimStyle)
	line.add(quotaWindowText(*snap.Weekly, showReset, now), style(snap.Weekly.UsedPercent))

	if tier > quotaTierShortLabels {
		return
	}
	for _, extra := range snap.Extra {
		line.add(quotaWindowJoiner, dimStyle)
		line.add(fmt.Sprintf("%.0f%% %s", extra.UsedPercent, extra.Name), style(extra.UsedPercent))
	}
}

// renderQuotaBar renders one line of provider usage, dropping detail until it
// fits width. It returns "" when there is nothing to show or no tier fits.
func renderQuotaBar(snaps []quota.Snapshot, width int, now time.Time) string {
	usable := make([]quota.Snapshot, 0, len(snaps))
	for _, snap := range snaps {
		if snap.Session == nil && snap.Weekly == nil {
			continue
		}
		usable = append(usable, snap)
	}
	if len(usable) == 0 || width <= 0 {
		return ""
	}

	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	for _, tier := range quotaTiers {
		line := &quotaLine{}
		if tier < quotaTierNoTitle {
			line.add(quotaTitle+quotaTitleGap, quotaTitleStyle())
		}
		for i, snap := range usable {
			if i > 0 {
				line.add(quotaSeparator, sepStyle)
			}
			quotaAppendSegment(line, snap, tier, now)
		}
		if lipgloss.Width(line.plain.String()) <= width {
			return line.styled.String()
		}
	}
	return ""
}

// quotaProviderOrder fixes the left-to-right order of the bar so segments do
// not jump around as providers appear and disappear.
var quotaProviderOrder = []string{quota.ProviderClaude, quota.ProviderCodex, quota.ProviderGemini}

// refreshQuotaSnapshots copies the poller's latest snapshots into render order.
// The store is shared in-process, so this is a map read, not I/O.
func (h *Home) refreshQuotaSnapshots() {
	if h.quotaStore == nil {
		return
	}
	all := h.quotaStore.All()
	snaps := make([]quota.Snapshot, 0, len(all))
	for _, provider := range quotaProviderOrder {
		if snap, ok := all[provider]; ok {
			snaps = append(snaps, snap)
		}
	}
	h.quotaSnaps = snaps
}

func (h *Home) quotaBarLine() string {
	if h.quotaMode == session.QuotaBarOff || len(h.quotaSnaps) == 0 {
		return ""
	}
	return renderQuotaBar(h.quotaSnaps, h.width, time.Now())
}

// quotaBarBlock is the rule plus the data row, so the bar reads as its own
// footer section rather than as an extra line of the session list.
func (h *Home) quotaBarBlock() string {
	line := h.quotaBarLine()
	if line == "" {
		return ""
	}
	rule := lipgloss.NewStyle().Foreground(ColorBorder).Render(strings.Repeat("─", max(0, h.quotaRuleWidth())))
	return rule + "\n" + line
}

// quotaRuleWidth stops the rule at the pane divider in the dual layout, so it
// closes the sessions column instead of cutting across PREVIEW.
func (h *Home) quotaRuleWidth() int {
	if h.getLayoutMode() == LayoutModeDual {
		return h.sessionsPaneWidth()
	}
	return h.width
}

func (h *Home) quotaBarHeight() int {
	if h.quotaBarBlock() == "" {
		return 0
	}
	return 2
}
