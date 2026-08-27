package web

import (
	"net/http"

	"github.com/asheshgoplani/agent-deck/internal/quota"
)

// quotaProviderOrder fixes the response order so clients never sort and the
// list never reshuffles as providers appear and disappear.
var quotaProviderOrder = []string{quota.ProviderClaude, quota.ProviderCodex, quota.ProviderGemini}

// handleQuota serves the provider usage snapshots the poller keeps in memory.
// A host with nothing polled yet answers 200 with an empty list rather than
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

	providers := []quota.Snapshot{}
	if s.quotaStore != nil {
		all := s.quotaStore.All()
		for _, provider := range quotaProviderOrder {
			if snap, ok := all[provider]; ok {
				providers = append(providers, snap)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}
