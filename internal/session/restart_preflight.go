package session

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// preflightResumeRestart proves that Restart can preserve both the workspace
// and conversation before it mutates the currently running session.
func (i *Instance) preflightResumeRestart() error {
	if !IsClaudeCompatible(i.Tool) && !IsCodexCompatible(i.Tool) {
		return nil
	}
	if err := validateRestartDir("working directory", i.EffectiveWorkingDir()); err != nil {
		return err
	}
	// WorktreePath is persisted independently from ProjectPath. Validate it
	// explicitly so corrupt/legacy state cannot fall back to an unrelated repo.
	if strings.TrimSpace(i.WorktreePath) != "" {
		if err := validateRestartDir("worktree", i.WorktreePath); err != nil {
			return err
		}
	}

	if IsClaudeCompatible(i.Tool) {
		originalID := i.ClaudeSessionID
		originalDetectedAt := i.ClaudeDetectedAt
		if i.ClaudeSessionID == "" {
			i.ensureClaudeSessionIDFromDiskForRestart()
		}
		if i.ClaudeSessionID == "" {
			return fmt.Errorf("restart refused: no Claude resume session ID; use restart fresh to start a new conversation")
		}
		// Restore the known backup before checking resumability. The live-pane
		// branch historically did this after dispatch, which is too late now that
		// preflight must prove the transcript exists before any pane mutation.
		if i.Tool == "claude" {
			if _, err := RestoreOrphanedConversationBackup(i, GetClaudeConfigDirForInstance(i)); err != nil {
				return fmt.Errorf("restart refused: restore Claude conversation backup: %w", err)
			}
		}
		if !canResumeClaudeSession(i, i.ClaudeSessionID) {
			refusedID := i.ClaudeSessionID
			// Disk discovery records an unverified hint on the Instance. Do not
			// let a refused restart leak that hint into the next persistence pass.
			i.markClaudeSessionIDVerified() // removes the transient hint's taint
			i.ClaudeSessionID = originalID
			i.ClaudeDetectedAt = originalDetectedAt
			return fmt.Errorf("restart refused: Claude resume session %s cannot be resolved from this session's workspace; use restart fresh to start a new conversation", refusedID)
		}
	}

	if IsCodexCompatible(i.Tool) {
		if i.CodexSessionID == "" {
			i.mu.Lock()
			i.pendingCodexRestartWarning = ""
			i.mu.Unlock()
			if missingDep := i.updateCodexSession(i.collectOtherCodexSessionIDs(), true); missingDep != "" {
				i.mu.Lock()
				i.pendingCodexRestartWarning = codexProbeMissingWarning(missingDep)
				i.mu.Unlock()
				sessionLog.Warn("codex_probe_dep_missing_for_restart", slog.String("dependency", missingDep))
			}
		}
		if i.CodexSessionID == "" {
			return fmt.Errorf("restart refused: no Codex resume session ID; use restart fresh to start a new conversation")
		}
		codexHome := i.codexHomeForCommand(i.resolveCodexCommand(i.Command))
		if !codexRolloutExistsInHome(i.CodexSessionID, codexHome) {
			return fmt.Errorf("restart refused: Codex resume session %s has no rollout under %s; use restart fresh to start a new conversation", i.CodexSessionID, filepath.Join(codexHome, "sessions"))
		}
	}
	return nil
}

func validateRestartDir(kind, path string) error {
	if err := tmux.ValidateStartWorkDir(path); err != nil {
		return fmt.Errorf("restart refused: session %s %q is unavailable: %w", kind, path, err)
	}
	return nil
}
