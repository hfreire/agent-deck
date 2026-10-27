package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/quota"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// quotaProviderOrder fixes the response order so clients never sort and the
// list never reshuffles as providers appear and disappear. A provider this
// build does not know about is appended after these, rather than dropped.
var quotaProviderOrder = []string{quota.ProviderClaude, quota.ProviderCodex, quota.ProviderZai}

// Nominal window lengths, in minutes. The wire shape predates internal/quota's
// window kinds and carries a duration instead, so a client can label a window
// without knowing the kinds. It is the window's NOMINAL length, never a
// substitute for the provider-reported reset.
const (
	quotaSessionWindowMinutes = 300
	quotaWeeklyWindowMinutes  = 7 * 24 * 60
)

type quotaWindowWire struct {
	UsedPercent   float64    `json:"used_percent"`
	WindowMinutes int        `json:"window_minutes"`
	ResetsAt      *time.Time `json:"resets_at,omitempty"`
}

type quotaNamedWindowWire struct {
	Name string `json:"name"`
	quotaWindowWire
}

// quotaProviderWire is one provider in the /api/quota response. The field
// names are a contract with the phone app (AgentDeckKit QuotaSnapshot), which
// is why the mapping from quota.Snapshot lives here rather than being the
// store's own JSON.
type quotaProviderWire struct {
	Provider  string                 `json:"provider"`
	Session   *quotaWindowWire       `json:"session,omitempty"`
	Weekly    *quotaWindowWire       `json:"weekly,omitempty"`
	Extra     []quotaNamedWindowWire `json:"extra,omitempty"`
	Plan      string                 `json:"plan,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
	Err       string                 `json:"error,omitempty"`
}

// handleQuota serves the cached per-provider plan usage.
//
// A host with nothing cached yet answers 200 with an empty list rather than
// 404, so a client can tell "no data" from "server too old to have this".
func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	if !s.authorizeRequest(r) {
		writeAPIError(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return
	}

	snapshots := s.loadQuotaSnapshots()
	providers := make([]quotaProviderWire, 0, len(snapshots))
	for _, id := range quotaProviderOrder {
		for _, snapshot := range snapshots {
			if snapshot.ID == id {
				providers = append(providers, quotaProviderToWire(snapshot))
			}
		}
	}
	for _, snapshot := range snapshots {
		if !knownQuotaProvider(snapshot.ID) {
			providers = append(providers, quotaProviderToWire(snapshot))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

func knownQuotaProvider(id string) bool {
	for _, known := range quotaProviderOrder {
		if known == id {
			return true
		}
	}
	return false
}

// loadQuotaSnapshots reads this profile's cache, bringing Codex's reading
// forward when it has gone stale. Claude is pushed by its statusLine and Z.ai
// needs the network, so Codex is the only provider this endpoint can refresh
// itself: its numbers come from transcripts Codex already wrote locally.
func (s *Server) loadQuotaSnapshots() []quota.Snapshot {
	profile, err := session.ResolveProfileForStorage(s.cfg.Profile)
	if err != nil {
		return nil
	}
	store, err := quota.NewStore(profile)
	if err != nil {
		return nil
	}
	snapshots, err := store.Load()
	if err != nil {
		return nil
	}
	return refreshStaleCodexQuota(store, profile, snapshots)
}

func refreshStaleCodexQuota(store *quota.Store, profile string, snapshots []quota.Snapshot) []quota.Snapshot {
	index := -1
	for i, snapshot := range snapshots {
		if snapshot.ID == quota.ProviderCodex {
			index = i
			break
		}
	}
	if index >= 0 && !snapshots[index].Stale {
		return snapshots
	}

	read, ok, err := quota.ReadCodex(session.CodexHomeForProfile(profile))
	switch {
	case err == nil && ok:
	case errors.Is(err, quota.ErrNotConfigured), err == nil:
		// Not installed for this account, or installed but never rate-limited.
		// Neither is a failure, and neither should discard what is cached.
		return snapshots
	default:
		if index >= 0 {
			snapshots[index].Error = err.Error()
		}
		return snapshots
	}

	// Best effort: a cache that cannot be written still serves this request,
	// and the next one re-reads the transcripts.
	_ = store.Save(read)
	read = store.MarkStale(read, time.Now())
	if index >= 0 {
		snapshots[index] = read
		return snapshots
	}
	return append(snapshots, read)
}

func quotaProviderToWire(snapshot quota.Snapshot) quotaProviderWire {
	out := quotaProviderWire{
		Provider:  snapshot.ID,
		Plan:      snapshot.Plan,
		UpdatedAt: time.Unix(snapshot.UpdatedAt, 0).UTC(),
		Err:       snapshot.Error,
	}
	for _, window := range snapshot.Windows {
		switch window.Kind {
		case quota.WindowFiveHour:
			wire := quotaWindowToWire(window, quotaSessionWindowMinutes)
			out.Session = &wire
		case quota.WindowSevenDay:
			wire := quotaWindowToWire(window, quotaWeeklyWindowMinutes)
			out.Weekly = &wire
		default:
			out.Extra = append(out.Extra, quotaNamedWindowWire{
				Name:            window.Label,
				quotaWindowWire: quotaWindowToWire(window, 0),
			})
		}
	}
	return out
}

func quotaWindowToWire(window quota.Window, minutes int) quotaWindowWire {
	wire := quotaWindowWire{UsedPercent: window.UsedPercentage, WindowMinutes: minutes}
	if window.ResetsAt != nil {
		resetsAt := time.Unix(*window.ResetsAt, 0).UTC()
		wire.ResetsAt = &resetsAt
	}
	return wire
}

func init() {
	registerFeatureRoutes(func(s *Server, mux *http.ServeMux) {
		mux.HandleFunc("/api/quota", s.handleQuota)
	})
}
