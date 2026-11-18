package session

import "testing"

func TestCodexHookRefusesSessionIDOwnedByAnotherLivePane(t *testing.T) {
	const sharedID = "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	inst := &Instance{ID: "new-codex", Tool: "codex"}

	orig := peerCodexSessionIDs
	peerCodexSessionIDs = func(*Instance, *StatusUpdatePass) map[string]bool {
		return map[string]bool{sharedID: true}
	}
	t.Cleanup(func() { peerCodexSessionIDs = orig })

	inst.UpdateHookStatus(&HookStatus{SessionID: sharedID, Event: "agent-turn-complete"})
	if inst.CodexSessionID != "" {
		t.Fatalf("foreign Codex ID was adopted: got %q, want empty", inst.CodexSessionID)
	}
}
