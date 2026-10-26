package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/asheshgoplani/agent-deck/internal/sandboxguard"
)

// codexLabel is the display name. Spelled once so the CLI and the cache agree.
const codexLabel = "Codex"

// Codex is read from its own rollout transcripts rather than from the usage
// endpoint its CLI polls. The endpoint would mean replaying the bearer token
// out of $CODEX_HOME/auth.json to a host this package invented, which is what
// the package comment rules out for Claude and what zai.go's host guard exists
// to prevent. The rollout carries the same numbers, written by Codex itself:
// no credential is read and no request is made.
//
// The reading is therefore as old as the last Codex turn, exactly like
// Claude's pushed snapshot, and is dated by the record rather than by the read.

// codexScanFiles bounds how many rollouts are opened per read. The newest
// reading is in the newest session the user ran, but a rollout's mtime moves
// with any write, so a couple of recently-touched sessions are checked rather
// than only the first. The session tree grows without bound (764 files on the
// development machine after two months), so this must not be "all of them".
const codexScanFiles = 5

// maxCodexTailBytes bounds what is read from one rollout. A rollout is append
// only and ~1 MiB after a long session, while the record this reads is one of
// the last written, so the tail is both sufficient and cheap. A var so the
// tail test can shrink it instead of generating a megabyte fixture.
var maxCodexTailBytes int64 = 256 << 10

// codexFutureSkew is how far ahead of this machine's clock a record may be
// dated and still be believed. UpdatedAt drives the staleness marker, and a
// reading dated in the future can never go stale, so a record from a skewed
// clock — or one written by anything else running as the user — would
// otherwise pin itself as the newest reading for good.
const codexFutureSkew = 2 * time.Minute

// maxCodexPlanRunes bounds the reported plan name. Nothing but Codex should be
// writing these files, but the whole tail is attacker-shaped input if anything
// else is, and Plan is printed.
const maxCodexPlanRunes = 32

// codexWalkTimeout bounds the recursive walk over $CODEX_HOME/sessions. The
// tree sits wherever the user's home does, and a readdir there has hung in
// kernel D-state before (#2674, WSL 9p), which would wedge every `agent-deck
// usage`. internal/session bounds the same walk for the same reason.
var codexWalkTimeout = 5 * time.Second

// codexWalkDir is the walk itself. Test seam.
var codexWalkDir = filepath.WalkDir

var errCodexWalkTimedOut = errors.New("timed out")

type codexRecord struct {
	Timestamp string `json:"timestamp"`
	Payload   struct {
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

type codexRateLimits struct {
	// PlanType is Codex's own name for the tier ("plus", "pro"), the
	// counterpart of Z.ai's data.level. It is reported, never inferred from
	// the limits.
	PlanType  string       `json:"plan_type"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexWindow struct {
	UsedPercent float64 `json:"used_percent"`
	// WindowMinutes is the window's own length, which is what names it:
	// unlike Claude's statusLine, the field names here ("primary",
	// "secondary") carry no duration, and observed values are 300 and 10080.
	WindowMinutes int `json:"window_minutes"`
	// ResetsAt is epoch SECONDS, as Claude reports it and unlike Z.ai's
	// milliseconds. Absent on a window Codex has not populated.
	ResetsAt *int64 `json:"resets_at"`
}

// ReadCodex returns the newest quota reading Codex has written to its rollout
// transcripts under codexHome (empty: $CODEX_HOME, then ~/.codex).
//
// ok is false, with no error, when no rollout carries a rate_limits block.
// That is the normal state for a Codex that has never run and the permanent
// state for API-key auth, so it is not a failure. ErrNotConfigured means the
// home itself is absent: Codex is not installed for this account.
func ReadCodex(codexHome string) (Snapshot, bool, error) {
	home, err := resolveCodexHome(codexHome)
	if err != nil {
		return Snapshot{}, false, err
	}
	if _, err := os.Stat(home); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{}, false, fmt.Errorf("%w: no Codex home at %s", ErrNotConfigured, home)
		}
		return Snapshot{}, false, fmt.Errorf("reading Codex home: %w", err)
	}

	rollouts, err := newestCodexRollouts(filepath.Join(home, "sessions"), codexScanFiles)
	if err != nil {
		return Snapshot{}, false, err
	}

	notAfter := time.Now().Add(codexFutureSkew)
	var best codexRateLimits
	var bestAt time.Time
	for _, path := range rollouts {
		limits, at, ok := latestCodexReading(path, notAfter)
		if ok && at.After(bestAt) {
			best, bestAt = limits, at
		}
	}
	if bestAt.IsZero() {
		return Snapshot{}, false, nil
	}
	return codexSnapshot(best, bestAt), true, nil
}

func resolveCodexHome(codexHome string) (string, error) {
	home, err := unguardedCodexHome(codexHome)
	if err != nil {
		return "", err
	}
	// CODEX_HOME wins over $HOME in every agent-deck resolver, so a test whose
	// HOME is isolated but whose CODEX_HOME leaked would read the developer's
	// own transcripts. The guard is a no-op outside test binaries.
	if err := sandboxguard.Check("Codex home", home); err != nil {
		return "", err
	}
	return home, nil
}

func unguardedCodexHome(codexHome string) (string, error) {
	if home := strings.TrimSpace(codexHome); home != "" {
		return home, nil
	}
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return home, nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(userHome, ".codex"), nil
}

// newestCodexRollouts returns up to limit rollout paths, most recently written
// first. The tree is walked rather than globbed on a date layout: Codex has
// shipped both a flat sessions/ and the current sessions/YYYY/MM/DD, and a
// layout change must degrade to "no reading", never to a crash.
func newestCodexRollouts(sessionsDir string, limit int) ([]string, error) {
	type result struct {
		paths []string
		err   error
	}
	// Buffered, so the goroutine this abandons on timeout still finishes
	// instead of blocking on the send forever.
	done := make(chan result, 1)
	go func() {
		paths, err := scanCodexRollouts(sessionsDir, limit)
		done <- result{paths: paths, err: err}
	}()
	select {
	case r := <-done:
		return r.paths, r.err
	case <-time.After(codexWalkTimeout):
		return nil, fmt.Errorf("scanning Codex sessions: %w", errCodexWalkTimedOut)
	}
}

func scanCodexRollouts(sessionsDir string, limit int) ([]string, error) {
	type rollout struct {
		path    string
		modTime time.Time
	}
	if _, err := os.Stat(sessionsDir); err != nil {
		// Nothing to read is the normal state for a Codex that has never run;
		// anything else (a permission problem, say) is worth reporting rather
		// than rendering as an account with no usage.
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning Codex sessions: %w", err)
	}

	var found []rollout
	//nolint:errcheck // every per-entry error is handled in the callback, so the walk itself cannot fail
	_ = codexWalkDir(sessionsDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			//nolint:nilerr // an unreadable subdirectory must not blank out the readings in its siblings
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		found = append(found, rollout{path: path, modTime: info.ModTime()})
		return nil
	})

	sort.Slice(found, func(i, j int) bool { return found[i].modTime.After(found[j].modTime) })
	if len(found) > limit {
		found = found[:limit]
	}
	paths := make([]string, 0, len(found))
	for _, r := range found {
		paths = append(paths, r.path)
	}
	return paths, nil
}

// latestCodexReading returns the last datable rate_limits block in one
// rollout, with the time the record itself carries.
func latestCodexReading(path string, notAfter time.Time) (codexRateLimits, time.Time, bool) {
	lines, err := tailLines(path, maxCodexTailBytes)
	if err != nil {
		return codexRateLimits{}, time.Time{}, false
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.Contains(lines[i], `"rate_limits"`) {
			continue
		}
		var record codexRecord
		if err := json.Unmarshal([]byte(lines[i]), &record); err != nil {
			continue
		}
		if record.Payload.RateLimits == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, record.Timestamp)
		if err != nil {
			// An undatable reading is dropped rather than dated from the
			// file's mtime: that would be a guess about when the provider
			// took the measurement, and the staleness marker is only worth
			// anything if nothing synthesises its input.
			continue
		}
		if at.After(notAfter) {
			continue
		}
		return *record.Payload.RateLimits, at, true
	}
	return codexRateLimits{}, time.Time{}, false
}

// tailLines returns the complete lines in the last max bytes of path.
func tailLines(path string, max int64) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	truncated := info.Size() > max
	if truncated {
		if _, err := file.Seek(info.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	raw, err := io.ReadAll(io.LimitReader(file, max))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines, nil
}

// codexPlan is plan_type as it is safe to print. Snapshot.Plan is rendered
// straight into the terminal, where a CSI or OSC sequence could rewrite the
// rest of the output, so control characters and an unbounded length are
// dropped here rather than at every render site.
func codexPlan(raw string) string {
	var plan []rune
	for _, r := range raw {
		if !unicode.IsGraphic(r) || unicode.IsSpace(r) {
			continue
		}
		plan = append(plan, r)
		if len(plan) == maxCodexPlanRunes {
			break
		}
	}
	return string(plan)
}

func codexSnapshot(limits codexRateLimits, at time.Time) Snapshot {
	snapshot := Snapshot{
		ID:        ProviderCodex,
		Label:     codexLabel,
		Plan:      codexPlan(limits.PlanType),
		UpdatedAt: at.Unix(),
	}
	// Primary before secondary, the order Codex reports them in and the order
	// they shorten in, so the rendering is stable across readings.
	for _, window := range []*codexWindow{limits.Primary, limits.Secondary} {
		if window == nil {
			continue
		}
		kind, label := codexWindowKind(window.WindowMinutes)
		snapshot.Windows = append(snapshot.Windows, Window{
			Kind:  kind,
			Label: label,
			// Not clamped, for the same reason Claude's spend_limit is not.
			UsedPercentage: window.UsedPercent,
			ResetsAt:       window.ResetsAt,
		})
	}
	return snapshot
}

// codexWindowKind names a window by its reported length. Only the two observed
// lengths are given a kind; anything else keeps WindowOther and a label built
// from Codex's own number rather than being forced into the nearest kind.
func codexWindowKind(minutes int) (WindowKind, string) {
	switch minutes {
	case 300:
		return WindowFiveHour, "5h"
	case 10080:
		return WindowSevenDay, "7d"
	}
	switch {
	case minutes <= 0:
		return WindowOther, "window"
	case minutes%1440 == 0:
		return WindowOther, fmt.Sprintf("%dd", minutes/1440)
	case minutes%60 == 0:
		return WindowOther, fmt.Sprintf("%dh", minutes/60)
	default:
		return WindowOther, fmt.Sprintf("%dm", minutes)
	}
}
