package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const archivedBacklog = 50

func seedQuotaTranscript(t *testing.T, inst *Instance) {
	t.Helper()
	dir := filepath.Join(GetClaudeConfigDirForInstance(inst), "projects", ConvertToClaudeDirName(inst.ProjectPath))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, inst.ClaudeSessionID+".jsonl"),
		[]byte(recRateLimitAt(time.Now().UTC().Format(time.RFC3339Nano))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalSubstate_ArchivedRowsSkipTranscriptScan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	newDead := func(id string, archived bool) *Instance {
		inst := NewInstanceWithTool(id, resolved, "claude")
		inst.ClaudeSessionID = "conv-" + id
		inst.Status = StatusError
		inst.tmuxSession = nil
		if archived {
			inst.ArchivedAt = time.Now().UTC()
		}
		seedQuotaTranscript(t, inst)
		return inst
	}

	live := newDead("unarchived", false)
	if got := live.HistoricalSubstate(); got != SubstateUsageLimit {
		t.Fatalf("unarchived dead row substate=%s, want usage-limit", got)
	}

	scanned := 0
	for n := 0; n < archivedBacklog; n++ {
		inst := newDead(fmt.Sprintf("archived-%d", n), true)
		_ = inst.HistoricalSubstate()
		if !inst.lastUsageLimitScanAt.IsZero() {
			scanned++
		}
	}
	if scanned != 0 {
		t.Fatalf("%d of %d archived rows scanned their transcript; want 0", scanned, archivedBacklog)
	}
}

func TestSyncProfile_ArchivedRowsAreNotProbed(t *testing.T) {
	const profile = "_test_archived_not_probed"
	d, storage := bootstrapDaemonProfile(t, profile)

	var mu sync.Mutex
	probed := map[string]int{}
	orig := updateInstanceStatus.Load().(statusProbeFunc)
	updateInstanceStatus.Store(statusProbeFunc(func(inst *Instance) error {
		mu.Lock()
		probed[inst.ID]++
		mu.Unlock()
		return nil
	}))
	t.Cleanup(func() { updateInstanceStatus.Store(orig) })

	archivedAt := time.Now().Add(-time.Hour).UTC()
	newRow := func(id string, status Status, archived bool) *Instance {
		pp := filepath.Join(os.Getenv("HOME"), "proj-"+id)
		if err := os.MkdirAll(pp, 0o755); err != nil {
			t.Fatal(err)
		}
		inst := &Instance{ID: id, Title: id, ProjectPath: pp, GroupPath: DefaultGroupPath,
			Tool: "claude", Status: status, CreatedAt: time.Now().Add(-2 * time.Hour)}
		if archived {
			inst.ArchivedAt = archivedAt
		}
		return inst
	}
	insts := []*Instance{
		newRow("live", StatusRunning, false),
		newRow("archived-claims-running", StatusRunning, true),
	}
	for n := 0; n < archivedBacklog; n++ {
		status := StatusError
		if n%2 == 0 {
			status = StatusStopped
		}
		insts = append(insts, newRow(fmt.Sprintf("archived-%d", n), status, true))
	}
	if err := storage.SaveWithGroups(insts, nil); err != nil {
		t.Fatal(err)
	}

	const journaled = "error|unknown-exit"
	d.lastJournaled[profile] = map[string]string{"archived-1": journaled}

	runSyncProfileWithDeadline(t, d, profile, 5*time.Second)
	runSyncProfileWithDeadline(t, d, profile, 5*time.Second)

	if got := d.lastJournaled[profile]["archived-1"]; got != journaled {
		t.Errorf("frozen archived row journal key=%q, want %q kept (a dropped substate journals a fake status change)", got, journaled)
	}
	mu.Lock()
	defer mu.Unlock()
	if probed["live"] == 0 {
		t.Fatal("live row was never probed")
	}
	if probed["archived-claims-running"] == 0 {
		t.Fatal("archived row still claiming running must be probed until it settles")
	}
	for _, inst := range insts[2:] {
		if probed[inst.ID] != 0 {
			t.Errorf("archived %s row %s probed %d times; want 0", inst.Status, inst.ID, probed[inst.ID])
		}
		if got := d.lastStatus[profile][inst.ID]; got != string(inst.Status) {
			t.Errorf("archived row %s status=%q, want stored %q", inst.ID, got, inst.Status)
		}
	}
}
