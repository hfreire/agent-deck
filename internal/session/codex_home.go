package session

import (
	"os"
	"strings"
)

// CodexHomeForProfile is the Codex home whose transcripts hold this profile's
// usage: CODEX_HOME, then the profile's own override, then the global [codex]
// setting, then empty — which leaves ~/.codex to the caller's own default, so
// a user who configured neither still gets their numbers. The order is the one
// internal/session resolves a Codex home in, and has to be: that is the home
// the sessions themselves run against, and usage attributed to the wrong
// account would be worse than no usage.
func CodexHomeForProfile(profile string) string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return ExpandPath(home)
	}
	config, err := LoadUserConfig()
	if err != nil || config == nil {
		return ""
	}
	if resolved, err := ResolveProfileForStorage(profile); err == nil {
		if dir := config.GetProfileCodexConfigDir(resolved); dir != "" {
			return dir
		}
	}
	return ExpandPath(config.Codex.ConfigDir)
}
