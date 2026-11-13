package session

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

func countLoadRowBuilds(t *testing.T) *int {
	t.Helper()
	n := new(int)
	orig := buildLoadBaseline
	buildLoadBaseline = func(inst *Instance) (*statedb.InstanceRow, error) {
		*n++
		return orig(inst)
	}
	t.Cleanup(func() { buildLoadBaseline = orig })
	return n
}

func TestLoadWithGroups_ReusesBaselinesOfUnchangedRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	s := newTestStorage(t)
	now := time.Now().Add(-time.Hour)
	var insts []*Instance
	for _, id := range []string{"a", "b", "c"} {
		insts = append(insts, &Instance{ID: id, Title: id, ProjectPath: filepath.Join(home, id),
			GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusIdle, CreatedAt: now})
	}
	if err := s.SaveWithGroups(insts, nil); err != nil {
		t.Fatal(err)
	}
	builds := countLoadRowBuilds(t)

	load := func() map[string]*Instance {
		t.Helper()
		loaded, _, err := s.LoadWithGroups()
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]*Instance{}
		for _, inst := range loaded {
			byID[inst.ID] = inst
		}
		return byID
	}

	load()
	if *builds != 3 {
		t.Fatalf("first load built %d baselines, want 3", *builds)
	}
	*builds = 0
	second := load()
	if *builds != 0 {
		t.Fatalf("reload of unchanged rows built %d baselines, want 0", *builds)
	}
	for id, inst := range second {
		fresh, err := instanceToRow(inst)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(inst.storageSnapshot.original, fresh) {
			t.Errorf("%s: reused baseline differs from a fresh one", id)
		}
	}

	if _, err := s.db.UpdateTitleIfUnlocked("b", "renamed elsewhere"); err != nil {
		t.Fatal(err)
	}
	*builds = 0
	load()
	if *builds != 1 {
		t.Fatalf("reload after one row changed built %d baselines, want 1", *builds)
	}

	cfgPath, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("default_tool = \"codex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	*builds = 0
	load()
	if *builds != 3 {
		t.Fatalf("reload after config.toml changed built %d baselines, want 3", *builds)
	}
}

func TestSave_AfterReusedBaselineKeepsAConcurrentEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	s := newTestStorage(t)
	inst := &Instance{ID: "a", Title: "a", ProjectPath: filepath.Join(home, "a"),
		GroupPath: DefaultGroupPath, Tool: "claude", Status: StatusIdle, CreatedAt: time.Now().Add(-time.Hour)}
	if err := s.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LoadWithGroups(); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.UpdateTitleIfUnlocked("a", "renamed elsewhere"); err != nil {
		t.Fatal(err)
	}
	loaded[0].Notes = "local edit"
	if err := s.SaveWithGroups(loaded, nil); err != nil {
		t.Fatal(err)
	}
	after, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Title != "renamed elsewhere" || after[0].Notes != "local edit" {
		t.Fatalf("title=%q notes=%q, want the concurrent rename and the local note both kept", after[0].Title, after[0].Notes)
	}
}

func TestLoadUserConfig_ResolvesTheConfigPathAtMostOncePerInterval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	clock := time.Now()
	origNow := userConfigNow
	userConfigNow = func() time.Time { return clock }
	t.Cleanup(func() { userConfigNow = origNow })

	legacy := filepath.Join(home, ".agent-deck", UserConfigFileName)
	xdg := filepath.Join(home, ".config", "agent-deck", UserConfigFileName)
	write := func(path, tool string, mtime time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("default_tool = \""+tool+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	defaultTool := func() string {
		t.Helper()
		cfg, err := LoadUserConfig()
		if err != nil {
			t.Fatal(err)
		}
		return cfg.DefaultTool
	}

	write(legacy, "codex", clock.Add(-time.Minute))
	if got := defaultTool(); got != "codex" {
		t.Fatalf("default_tool=%q, want codex", got)
	}
	write(legacy, "gemini", clock)
	if got := defaultTool(); got != "gemini" {
		t.Fatalf("default_tool=%q, want an edit to the resolved file picked up on the next call", got)
	}
	write(xdg, "claude", clock.Add(time.Minute))
	if got := defaultTool(); got != "gemini" {
		t.Fatalf("default_tool=%q, want the path resolved under a second ago reused", got)
	}
	clock = clock.Add(userConfigPathTTL)
	if got := defaultTool(); got != "claude" {
		t.Fatalf("default_tool=%q after the interval, want the XDG file", got)
	}

	newHome := t.TempDir()
	t.Setenv("HOME", newHome)
	if got := defaultTool(); got != "" {
		t.Fatalf("default_tool=%q right after HOME changed, want the new HOME's default", got)
	}
	write(filepath.Join(newHome, ".agent-deck", UserConfigFileName), "codex", clock)
	if got := defaultTool(); got != "codex" {
		t.Fatalf("default_tool=%q, want a config.toml created where none existed picked up on the next call", got)
	}
}
