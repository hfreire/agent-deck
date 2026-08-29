package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestartPreflightRefusesMissingWorktreeWithLiveBranch(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"-c", "user.name=Agent Deck Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "base"},
		{"branch", "feature/live"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/heads/feature/live").Run(); err != nil {
		t.Fatalf("fixture branch is not live: %v", err)
	}
	missingWorktree := filepath.Join(repo, ".worktrees", "feature-live")
	inst := &Instance{
		Tool:             "claude",
		ProjectPath:      repo, // legacy/corrupt fallback must not hide the missing worktree
		WorktreePath:     missingWorktree,
		WorktreeRepoRoot: repo,
		WorktreeBranch:   "feature/live",
		ClaudeSessionID:  "01a049cf-368f-7dc0-bf25-fdd5c9772351",
	}

	err := inst.preflightResumeRestart()
	if err == nil || !strings.Contains(err.Error(), "worktree") || !strings.Contains(err.Error(), missingWorktree) {
		t.Fatalf("preflight error = %v, want visible missing-worktree error", err)
	}
	if inst.ClaudeSessionID == "" {
		t.Fatal("failed restart cleared the stored Claude resume ID")
	}
}

func TestRestartPreflightPreservesStoredCodexResumeID(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	workDir := t.TempDir()
	sessionID := "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	dir := filepath.Join(codexHome, "sessions", "2026", "08", "28")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-08-28T21-18-31-"+sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: workDir, CodexSessionID: sessionID}

	if err := inst.preflightResumeRestart(); err != nil {
		t.Fatalf("preflight rejected stored resume ID: %v", err)
	}
	command := inst.buildCodexCommand(inst.Command)
	if !strings.Contains(command, "resume "+sessionID) {
		t.Fatalf("restart command = %q, want resume %s", command, sessionID)
	}
	if inst.CodexSessionID != sessionID {
		t.Fatalf("stored Codex ID changed to %q", inst.CodexSessionID)
	}
}

func TestRestartPreflightRefusesMissingCodexRolloutWithoutClearingID(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sessionID := "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: t.TempDir(), CodexSessionID: sessionID}

	err := inst.preflightResumeRestart()
	if err == nil || !strings.Contains(err.Error(), "no rollout") {
		t.Fatalf("preflight error = %v, want missing-rollout error", err)
	}
	if inst.CodexSessionID != sessionID {
		t.Fatalf("failed restart cleared Codex ID: got %q", inst.CodexSessionID)
	}
}
