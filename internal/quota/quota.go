// Package quota reads how much of the current plan window each coding agent has
// spent — the numbers Claude, Codex and Gemini show inside their own clients.
//
// None of the three endpoints is a documented public API. They are the ones the
// vendors' own clients call, and they can change shape or disappear without
// notice, so every failure here is soft: the provider's Snapshot carries an Err
// string, the last good numbers are kept, and the caller renders nothing rather
// than an error. Nothing in this package is allowed on a render path.
//
// Reads are strictly read-only. The package never refreshes or rewrites a
// credential file: agent-deck already keeps Claude's token warm from
// internal/credrefresh, and a status bar that rotates the tokens live sessions
// depend on would be trading a cosmetic feature against the login they need.
// An expired token surfaces as an ordinary fetch error until its owner rotates it.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Provider ids, also used as the cache keys in Store.
const (
	ProviderClaude = "claude"
	ProviderCodex  = "codex"
	ProviderGemini = "gemini"
)

// The two window lengths every provider reports in some form. Used as the
// fallback when a payload omits the duration.
const (
	SessionWindowMinutes = 300
	WeeklyWindowMinutes  = 10080
)

// ErrNoCredentials reports that the provider is not signed in on this machine.
// It is not a failure — the caller hides the provider instead of reporting it.
var ErrNoCredentials = errors.New("quota: provider not signed in")

// Window is one rate-limit window: how much of it is spent, how long it runs,
// and when it rolls over. A zero ResetsAt means the provider did not say.
type Window struct {
	UsedPercent   float64   `json:"used_percent"`
	WindowMinutes int       `json:"window_minutes"`
	ResetsAt      time.Time `json:"resets_at,omitempty"`
}

// ResetsIn is how long until the window rolls over, or 0 when unknown or past.
func (w Window) ResetsIn(now time.Time) time.Duration {
	if w.ResetsAt.IsZero() {
		return 0
	}
	if d := w.ResetsAt.Sub(now); d > 0 {
		return d
	}
	return 0
}

// NamedWindow is a Window the UI can label, e.g. a per-model weekly cap.
type NamedWindow struct {
	Name string `json:"name"`
	Window
}

// Snapshot is one provider's usage at a point in time. Session is the short
// rolling window (five hours for Claude and Codex, one hour for Gemini), Weekly
// the long one where the provider has one, and Extra any scoped caps on top.
type Snapshot struct {
	Provider  string        `json:"provider"`
	Session   *Window       `json:"session,omitempty"`
	Weekly    *Window       `json:"weekly,omitempty"`
	Extra     []NamedWindow `json:"extra,omitempty"`
	Plan      string        `json:"plan,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
	Err       string        `json:"error,omitempty"`
}

// Fetcher reads one provider's usage.
type Fetcher interface {
	Provider() string
	Fetch(ctx context.Context) (Snapshot, error)
}

func clampPercent(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// unixEpochMillisThreshold separates a seconds timestamp from a milliseconds
// one. Anything past it is milliseconds — the seconds form only reaches 1e10 in
// the year 2286, and the milliseconds form passed it in 1970.
const unixEpochMillisThreshold = 1e10

// parseResetTime decodes the several shapes a reset timestamp arrives in:
// RFC3339 (Claude), unix seconds (Codex), unix milliseconds, a numeric string,
// or null. Anything unrecognised yields the zero time rather than an error —
// a missing reset only costs the countdown, never the percentage.
func parseResetTime(raw json.RawMessage) time.Time {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return time.Time{}
	}
	if n, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return unixTimestamp(n)
	}

	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return time.Time{}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return unixTimestamp(n)
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	return time.Time{}
}

func unixTimestamp(n float64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	if n > unixEpochMillisThreshold {
		return time.UnixMilli(int64(n))
	}
	return time.Unix(int64(n), 0)
}

func errorIsMissingCredentials(err error) bool {
	return errors.Is(err, ErrNoCredentials)
}
