package session

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
		originalUnverified := i.claudeSessionIDIsUnverified()
		if i.ClaudeSessionID == "" {
			i.ensureClaudeSessionIDFromDiskForRestart()
		}
		if i.ClaudeSessionID == "" {
			if !i.conversationStoreIsOnHost() || i.claudeWorkspaceMayHoldTranscript() {
				return fmt.Errorf("restart refused: no Claude resume session ID; use restart fresh to start a new conversation")
			}
		} else {
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
				if originalID == "" {
					// Disk discovery records an unverified hint on the Instance. Do not
					// let a refused restart leak that hint into the next persistence pass.
					i.markClaudeSessionIDVerified() // removes the transient hint's taint
				}
				i.ClaudeSessionID = originalID
				i.ClaudeDetectedAt = originalDetectedAt
				// Claude writes the transcript on the first turn, so an ID agent-deck
				// minted with no transcript anywhere is a conversation that never
				// started. A scanned ID came from a transcript, so it always has one.
				if originalID == "" || originalUnverified || !i.conversationStoreIsOnHost() ||
					!claudeSessionIDPattern.MatchString(refusedID) || i.claudeTranscriptMayExist(refusedID) {
					return fmt.Errorf("restart refused: Claude resume session %s cannot be resolved from this session's workspace; use restart fresh to start a new conversation", refusedID)
				}
			}
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
			if !i.conversationStoreIsOnHost() || i.codexProjectMayHoldRollout() {
				return fmt.Errorf("restart refused: no Codex resume session ID; use restart fresh to start a new conversation")
			}
		} else {
			codexHome := i.codexHomeForCommand(i.resolveCodexCommand(i.Command))
			if !codexRolloutExistsInHome(i.CodexSessionID, codexHome) {
				return fmt.Errorf("restart refused: Codex resume session %s has no rollout under %s; use restart fresh to start a new conversation", i.CodexSessionID, filepath.Join(codexHome, "sessions"))
			}
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

var claudeSessionIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// conversationStoreIsOnHost is false for remote and sandboxed sessions, whose
// transcripts live where host scans cannot see them.
func (i *Instance) conversationStoreIsOnHost() bool {
	return i.TranscriptIsResolvableLocally() && !i.IsSandboxed()
}

func (i *Instance) claudeConfigDirsForRestart() []string {
	dirs := []string{GetClaudeConfigDirForInstance(i)}
	if global := GetClaudeConfigDir(); global != "" && global != dirs[0] {
		dirs = append(dirs, global)
	}
	return dirs
}

// claudeWorkspaceMayHoldTranscript fails closed: disk discovery reads only the
// global config dir and ignores read errors, so it cannot prove absence.
func (i *Instance) claudeWorkspaceMayHoldTranscript() bool {
	for _, configDir := range i.claudeConfigDirsForRestart() {
		for _, workDir := range claudeWorkDirSpellings(i.EffectiveWorkingDir(), i.ProjectPath) {
			encoded := ConvertToClaudeDirName(workDir)
			if encoded == "" {
				encoded = "-"
			}
			entries, err := os.ReadDir(filepath.Join(configDir, "projects", encoded))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return true
			}
			for _, e := range entries {
				// Includes orphaned .jsonl.bak-* files, which only a known ID can restore.
				if !e.IsDir() && strings.Contains(e.Name(), ".jsonl") {
					return true
				}
			}
		}
	}
	return false
}

func claudeWorkDirSpellings(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p == "" {
			continue
		}
		out = append(out, p)
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			out = append(out, r)
		}
	}
	return out
}

// claudeTranscriptMayExist fails closed on unreadable dirs and also checks the
// global config dir, where the transcript lives if the account changed.
func (i *Instance) claudeTranscriptMayExist(sessionID string) bool {
	for _, configDir := range i.claudeConfigDirsForRestart() {
		projectsDir := filepath.Join(configDir, "projects")
		entries, err := os.ReadDir(projectsDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return true
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			live := filepath.Join(projectsDir, e.Name(), sessionID+".jsonl")
			if _, err := os.Stat(live); !errors.Is(err, fs.ErrNotExist) {
				return true
			}
			if backups, _ := filepath.Glob(live + ".bak-*"); len(backups) > 0 {
				return true
			}
		}
	}
	return false
}

// codexProjectMayHoldRollout fails closed: updateCodexSession can return before
// its disk scan (probe error, scan throttle), and the scan matches rollouts
// against ProjectPath only, which is not where a multi-repo session runs.
func (i *Instance) codexProjectMayHoldRollout() bool {
	if i.EffectiveWorkingDir() != i.ProjectPath {
		return true
	}
	id, complete := i.queryCodexSessionChecked(i.collectOtherCodexSessionIDs(), false)
	return id != "" || !complete
}
