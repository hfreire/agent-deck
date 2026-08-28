package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// claudeUsageURL is the endpoint the Claude Code client polls for the
	// numbers behind its own /usage output.
	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

	// claudeOAuthBeta gates the OAuth (subscription) auth path; without it the
	// endpoint rejects a subscription bearer token.
	claudeOAuthBeta = "oauth-2025-04-20"

	claudeUserAgent = "claude-code/2.1.0"

	// claudeKeychainService is where Claude Code stores credentials on macOS.
	// The file at $CLAUDE_CONFIG_DIR/.credentials.json — which is what
	// internal/credrefresh manages — does not exist for a default macOS
	// install, so the keychain is the only source there.
	claudeKeychainService = "Claude Code-credentials"

	claudeCredentialsFile = ".credentials.json"

	// claudeTokenSkew keeps the cached token from being used right up to its
	// expiry, where the request would race the refresh daemon.
	claudeTokenSkew = time.Minute
)

// ClaudeFetcher reads plan usage for one Claude profile.
type ClaudeFetcher struct {
	// ConfigDir is the profile's Claude config dir (e.g. ~/.claude). Empty
	// means the default.
	ConfigDir string
	// UsageURL overrides the endpoint. Empty means claudeUsageURL.
	UsageURL string
	// HTTPClient overrides the client. Empty means a client with httpTimeout.
	HTTPClient *http.Client

	// readCredential overrides credential lookup in tests.
	readCredential func() (claudeCredential, error)

	mu     sync.Mutex
	cached claudeCredential
}

// claudeCredential is the subscription token plus the expiry the credentials
// carry, so a valid token can be reused instead of re-shelling to the keychain
// on every poll.
type claudeCredential struct {
	token     string
	expiresAt time.Time
}

func (c claudeCredential) valid(now time.Time) bool {
	return c.token != "" && !c.expiresAt.IsZero() && now.Before(c.expiresAt.Add(-claudeTokenSkew))
}

func (f *ClaudeFetcher) token() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.cached.valid(time.Now()) {
		return f.cached.token, nil
	}
	read := f.readCredential
	if read == nil {
		read = func() (claudeCredential, error) { return claudeCredentials(f.ConfigDir) }
	}
	cred, err := read()
	if err != nil {
		return "", err
	}
	f.cached = cred
	return cred.token, nil
}

func (f *ClaudeFetcher) Provider() string { return ProviderClaude }

func (f *ClaudeFetcher) Fetch(ctx context.Context) (Snapshot, error) {
	token, err := f.token()
	if err != nil {
		return Snapshot{}, err
	}

	url := f.UsageURL
	if url == "" {
		url = claudeUsageURL
	}
	data, err := requestJSON(ctx, f.HTTPClient, http.MethodGet, url, nil, map[string]string{
		"Authorization":  "Bearer " + token,
		"anthropic-beta": claudeOAuthBeta,
		"User-Agent":     claudeUserAgent,
	})
	if err != nil {
		return Snapshot{}, err
	}
	return parseClaudeUsage(data)
}

// claudeCredentials resolves the subscription access token, preferring the
// credentials file (the only form on Linux, and what agent-deck's per-profile
// worker scratch dirs symlink) and falling back to the macOS keychain.
func claudeCredentials(configDir string) (claudeCredential, error) {
	if configDir == "" {
		configDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, ".claude")
		}
	}

	if configDir != "" {
		path := filepath.Join(configDir, claudeCredentialsFile)
		if _, err := os.Stat(path); err == nil {
			return claudeCredentialFromFile(path)
		}
	}
	if runtime.GOOS == "darwin" {
		return claudeCredentialFromKeychain()
	}
	return claudeCredential{}, fmt.Errorf("%w: claude", ErrNoCredentials)
}

func claudeCredentialFromFile(path string) (claudeCredential, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the resolved profile credentials file
	if err != nil {
		return claudeCredential{}, fmt.Errorf("read claude credentials: %w", err)
	}
	return claudeCredentialFromJSON(data)
}

func claudeCredentialFromKeychain() (claudeCredential, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", claudeKeychainService, "-w").Output()
	if err != nil {
		return claudeCredential{}, fmt.Errorf("%w: claude keychain entry %q unreadable", ErrNoCredentials, claudeKeychainService)
	}
	return claudeCredentialFromJSON(out)
}

func claudeCredentialFromJSON(data []byte) (claudeCredential, error) {
	var doc struct {
		OAuth *struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return claudeCredential{}, fmt.Errorf("parse claude credentials: %w", err)
	}
	if doc.OAuth == nil || strings.TrimSpace(doc.OAuth.AccessToken) == "" {
		return claudeCredential{}, fmt.Errorf("%w: claude credentials carry no subscription token", ErrNoCredentials)
	}
	cred := claudeCredential{token: doc.OAuth.AccessToken}
	if doc.OAuth.ExpiresAt > 0 {
		cred.expiresAt = time.UnixMilli(doc.OAuth.ExpiresAt)
	}
	return cred, nil
}

type claudeWindowPayload struct {
	Utilization    *float64        `json:"utilization"`
	UsedPercentage *float64        `json:"used_percentage"`
	ResetsAt       json.RawMessage `json:"resets_at"`
}

func (p *claudeWindowPayload) percent() (float64, bool) {
	if p == nil {
		return 0, false
	}
	if p.Utilization != nil {
		return *p.Utilization, true
	}
	if p.UsedPercentage != nil {
		return *p.UsedPercentage, true
	}
	return 0, false
}

type claudeLimitPayload struct {
	Kind     string          `json:"kind"`
	Percent  *float64        `json:"percent"`
	ResetsAt json.RawMessage `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type claudeUsagePayload struct {
	FiveHour *claudeWindowPayload `json:"five_hour"`
	SevenDay *claudeWindowPayload `json:"seven_day"`
	Limits   []claudeLimitPayload `json:"limits"`
}

// parseClaudeUsage maps the usage payload onto a Snapshot. The scoped weekly
// caps (a per-model limit such as Fable's) exist only in limits[], which is
// also the newer and more descriptive half of the payload; the top-level
// five_hour/seven_day objects are kept as the fallback because they are what
// older responses carry.
func parseClaudeUsage(data []byte) (Snapshot, error) {
	var payload claudeUsagePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Snapshot{}, fmt.Errorf("parse claude usage: %w", err)
	}

	snap := Snapshot{Provider: ProviderClaude}
	if pct, ok := payload.FiveHour.percent(); ok {
		snap.Session = &Window{
			UsedPercent:   clampPercent(pct),
			WindowMinutes: SessionWindowMinutes,
			ResetsAt:      parseResetTime(payload.FiveHour.ResetsAt),
		}
	}
	if pct, ok := payload.SevenDay.percent(); ok {
		snap.Weekly = &Window{
			UsedPercent:   clampPercent(pct),
			WindowMinutes: WeeklyWindowMinutes,
			ResetsAt:      parseResetTime(payload.SevenDay.ResetsAt),
		}
	}

	for _, limit := range payload.Limits {
		if limit.Percent == nil {
			continue
		}
		window := Window{
			UsedPercent: clampPercent(*limit.Percent),
			ResetsAt:    parseResetTime(limit.ResetsAt),
		}
		switch limit.Kind {
		case "session":
			window.WindowMinutes = SessionWindowMinutes
			if snap.Session == nil {
				snap.Session = &window
			}
		case "weekly_all":
			window.WindowMinutes = WeeklyWindowMinutes
			if snap.Weekly == nil {
				snap.Weekly = &window
			}
		case "weekly_scoped":
			name := claudeScopeName(limit)
			if name == "" {
				continue
			}
			window.WindowMinutes = WeeklyWindowMinutes
			snap.Extra = append(snap.Extra, NamedWindow{Name: name, Window: window})
		}
	}

	snap.UpdatedAt = time.Now()
	return snap, nil
}

func claudeScopeName(limit claudeLimitPayload) string {
	if limit.Scope == nil || limit.Scope.Model == nil {
		return ""
	}
	return strings.TrimSpace(limit.Scope.Model.DisplayName)
}
