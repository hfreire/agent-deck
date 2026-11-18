package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRestartPreflightAllowsClaudeSessionThatNeverWroteATranscript(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	sessionID := "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	inst := &Instance{Tool: "claude", ProjectPath: t.TempDir(), ClaudeSessionID: sessionID}

	if err := inst.preflightResumeRestart(); err != nil {
		t.Fatalf("preflight refused a session with no conversation to lose: %v", err)
	}
	if inst.ClaudeSessionID != sessionID {
		t.Fatalf("preflight changed Claude ID to %q", inst.ClaudeSessionID)
	}
}

func TestRestartPreflightRefusesClaudeTranscriptOutsideWorkspace(t *testing.T) {
	home := isolatedHomeDir(t)
	configDir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	sessionID := "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	foreignDir := claudeProjectDirForTest(t, configDir, t.TempDir())
	if err := os.MkdirAll(foreignDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreignDir, sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "claude", ProjectPath: t.TempDir(), ClaudeSessionID: sessionID}

	err := inst.preflightResumeRestart()
	if err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("preflight error = %v, want refusal for an unreachable transcript", err)
	}
}

func TestRestartPreflightAllowsCodexSessionWithoutAnyRollout(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: t.TempDir()}

	if err := inst.preflightResumeRestart(); err != nil {
		t.Fatalf("preflight refused a Codex session with no conversation to lose: %v", err)
	}
}

func TestRestartClearsSkipMCPRegenerateWhenPreflightRefuses(t *testing.T) {
	isolatedHomeDir(t)
	inst := &Instance{
		ID:                "preflight-skip-regen",
		Tool:              "claude",
		ProjectPath:       t.TempDir(),
		WorktreePath:      filepath.Join(t.TempDir(), "missing"),
		ClaudeSessionID:   "01a049cf-368f-7dc0-bf25-fdd5c9772351",
		SkipMCPRegenerate: true,
	}

	if err := inst.Restart(); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("Restart error = %v, want missing-worktree refusal", err)
	}
	if inst.SkipMCPRegenerate {
		t.Fatal("refused restart left SkipMCPRegenerate set; the next restart would skip MCP regeneration")
	}
}

func TestRestartPreflightRefusesRemoteSessionsWithoutResumeID(t *testing.T) {
	isolatedHomeDir(t)
	for _, tool := range []string{"claude", "codex"} {
		t.Run(tool, func(t *testing.T) {
			inst := &Instance{Tool: tool, Command: tool, ProjectPath: t.TempDir(), SSHHost: "devbox"}

			if err := inst.preflightResumeRestart(); err == nil {
				t.Fatal("preflight allowed a remote restart whose conversation cannot be checked locally")
			}
		})
	}
}

func TestRestartPreflightRefusesClaudeTranscriptInAccountConfigDirWithoutID(t *testing.T) {
	accountDir := filepath.Join(t.TempDir(), "work")
	startupNameConfig(t, fmt.Sprintf("[profiles.work.claude]\nconfig_dir=%q\n", accountDir))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "global"))
	workDir := t.TempDir()
	projectDir := claudeProjectDirForTest(t, accountDir, workDir)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "01a049cf-368f-7dc0-bf25-fdd5c9772351.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "claude", Account: "work", ProjectPath: workDir}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a fresh start over a transcript in the account config dir")
	}
}

func TestRestartPreflightRefusesCodexRolloutWhenDiscoveryIsThrottled(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	workDir := t.TempDir()
	seedCodexRolloutCwd(t, codexHome, "01a049cf-368f-7dc0-bf25-fdd5c9772351", "user", workDir)
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: workDir, lastCodexScanAt: time.Now()}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a fresh start over an existing project rollout")
	}
}

func accountInstanceForPreflight(t *testing.T) (inst *Instance, accountDir, globalDir string) {
	t.Helper()
	accountDir = filepath.Join(t.TempDir(), "work")
	startupNameConfig(t, fmt.Sprintf("[profiles.work.claude]\nconfig_dir=%q\n", accountDir))
	globalDir = filepath.Join(t.TempDir(), "global")
	t.Setenv("CLAUDE_CONFIG_DIR", globalDir)
	return &Instance{Tool: "claude", Account: "work", ProjectPath: t.TempDir()}, accountDir, globalDir
}

func writeClaudeTranscriptForTest(t *testing.T, configDir, projectPath, sessionID string) string {
	t.Helper()
	dir := claudeProjectDirForTest(t, configDir, projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRestartPreflightRefusesWhenDiscoveryFindsAnUnboundTranscript(t *testing.T) {
	inst, _, globalDir := accountInstanceForPreflight(t)
	writeClaudeTranscriptForTest(t, globalDir, inst.ProjectPath, "11111111-1111-4111-8111-111111111111")

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a fresh start over a discovered transcript")
	}
}

func TestRestartPreflightRefusesStoredClaudeIDWhoseTranscriptIsInTheGlobalConfigDir(t *testing.T) {
	inst, _, globalDir := accountInstanceForPreflight(t)
	inst.ClaudeSessionID = "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	writeClaudeTranscriptForTest(t, globalDir, t.TempDir(), inst.ClaudeSessionID)

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a fresh start while the stored ID's transcript exists")
	}
}

func TestRestartPreflightRefusesUnreadableClaudeProjectDir(t *testing.T) {
	inst, accountDir, _ := accountInstanceForPreflight(t)
	dir := writeClaudeTranscriptForTest(t, accountDir, inst.ProjectPath, "11111111-1111-4111-8111-111111111111")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight treated an unreadable project dir as holding no transcript")
	}
}

func TestRestartPreflightRefusesCodexWhenRolloutScanIsIncomplete(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	workDir := t.TempDir()
	seedCodexRolloutCwd(t, codexHome, "01a049cf-368f-7dc0-bf25-fdd5c9772351", "user", workDir)
	day := filepath.Join(codexHome, "sessions", "2026", "07", "15")
	if err := os.Chmod(day, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(day, 0o755) })
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: workDir}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight trusted a rollout scan that could not read the sessions tree")
	}
}

func TestRestartPreflightRefusesMultiRepoCodexWithoutResumeID(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: t.TempDir(), MultiRepoEnabled: true, MultiRepoTempDir: t.TempDir()}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight trusted a project-path rollout scan for a multi-repo session")
	}
}

func TestRestartPreflightRefusesClaudeTranscriptUnderUnresolvedSymlinkSpelling(t *testing.T) {
	inst, accountDir, _ := accountInstanceForPreflight(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(inst.ProjectPath, link); err != nil {
		t.Fatal(err)
	}
	inst.ProjectPath = link
	dir := filepath.Join(accountDir, "projects", ConvertToClaudeDirName(link))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "11111111-1111-4111-8111-111111111111.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight missed a transcript stored under the symlink spelling")
	}
}

func TestRestartPreflightRefusesOrphanedClaudeBackupWithoutID(t *testing.T) {
	inst, accountDir, _ := accountInstanceForPreflight(t)
	dir := writeClaudeTranscriptForTest(t, accountDir, inst.ProjectPath, "11111111-1111-4111-8111-111111111111")
	live := filepath.Join(dir, "11111111-1111-4111-8111-111111111111.jsonl")
	if err := os.Rename(live, live+".bak-1700000000"); err != nil {
		t.Fatal(err)
	}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a fresh start over an orphaned transcript backup")
	}
}

func TestRestartPreflightRefusesUnverifiedStoredClaudeIDWithoutTranscript(t *testing.T) {
	inst, _, _ := accountInstanceForPreflight(t)
	inst.ClaudeSessionID = "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	inst.markClaudeSessionIDFromDiskScan(inst.ClaudeSessionID)

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight let a disk-scan ID pass as a pre-minted one")
	}
	if !inst.claudeSessionIDIsUnverified() {
		t.Fatal("refused preflight laundered the disk-scan ID into a verified one")
	}
}

func TestRestartPreflightRefusesCodexWhenARolloutIsUnreadable(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	workDir := t.TempDir()
	sessionID := "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	seedCodexRolloutCwd(t, codexHome, sessionID, "user", workDir)
	rollout := filepath.Join(codexHome, "sessions", "2026", "07", "15", "rollout-2026-07-15T20-00-00-"+sessionID+".jsonl")
	if err := os.Chmod(rollout, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(rollout, 0o600) })
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: workDir}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight trusted a rollout scan that could not read a rollout")
	}
}

func TestRestartPreflightRefusesStoredClaudeIDWithOnlyAnOrphanedBackup(t *testing.T) {
	inst, _, globalDir := accountInstanceForPreflight(t)
	inst.ClaudeSessionID = "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	dir := writeClaudeTranscriptForTest(t, globalDir, inst.ProjectPath, inst.ClaudeSessionID)
	live := filepath.Join(dir, inst.ClaudeSessionID+".jsonl")
	if err := os.Rename(live, live+".bak-1700000000"); err != nil {
		t.Fatal(err)
	}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a fresh start over the stored ID's orphaned backup")
	}
}

func TestRestartPreflightRefusesCodexWhenARolloutHasNoProjectMetadata(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sessionID := "01a049cf-368f-7dc0-bf25-fdd5c9772351"
	dir := filepath.Join(codexHome, "sessions", "2026", "07", "15")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	head := `{"type":"session_meta","payload":{"id":"` + sessionID + `"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-07-15T20-00-00-"+sessionID+".jsonl"), []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{Tool: "codex", Command: "codex", ProjectPath: t.TempDir()}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight treated a rollout of unknown project as proof of absence")
	}
}

func TestRestartPreflightRefusesSandboxedSessionsItCannotInspect(t *testing.T) {
	isolatedHomeDir(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	cases := map[string]*Instance{
		"claude without ID": {Tool: "claude", ProjectPath: t.TempDir()},
		"claude with ID":    {Tool: "claude", ProjectPath: t.TempDir(), ClaudeSessionID: "01a049cf-368f-7dc0-bf25-fdd5c9772351"},
		"codex without ID":  {Tool: "codex", Command: "codex", ProjectPath: t.TempDir()},
	}
	for name, inst := range cases {
		t.Run(name, func(t *testing.T) {
			inst.Sandbox = &SandboxConfig{Enabled: true}

			if err := inst.preflightResumeRestart(); err == nil {
				t.Fatal("preflight trusted host dirs for a sandboxed session's conversation")
			}
		})
	}
}

func TestRestartPreflightRefusesMalformedStoredClaudeID(t *testing.T) {
	isolatedHomeDir(t)
	inst := &Instance{Tool: "claude", ProjectPath: t.TempDir(), ClaudeSessionID: "$(touch pwned)"}

	if err := inst.preflightResumeRestart(); err == nil {
		t.Fatal("preflight allowed a restart with a malformed Claude session ID")
	}
}
