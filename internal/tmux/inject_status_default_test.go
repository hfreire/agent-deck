package tmux

import "testing"

// TestReconnectSessionLazyHonorsInjectDefault guards the fix for
// inject_status_line=false being ignored on the watcher/reconnect path: sessions
// rebuilt for existing tmux sessions must adopt the process-wide default seeded
// from config, not a hardcoded "always inject".
func TestReconnectSessionLazyHonorsInjectDefault(t *testing.T) {
	orig := getDefaultInjectStatusLine()
	defer SetDefaultInjectStatusLine(orig)

	SetDefaultInjectStatusLine(false)
	if s := ReconnectSessionLazy("no-such-session", "d", "/tmp", "", ""); s.injectStatusLine {
		t.Fatalf("expected injectStatusLine=false when default is off")
	}

	SetDefaultInjectStatusLine(true)
	if s := ReconnectSessionLazy("no-such-session", "d", "/tmp", "", ""); !s.injectStatusLine {
		t.Fatalf("expected injectStatusLine=true when default is on")
	}
}
