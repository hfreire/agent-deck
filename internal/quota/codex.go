package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// codexUsageURL is the endpoint the Codex clients poll for the plan usage
	// they render in their own status line.
	codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

	codexUserAgent  = "codex-cli"
	codexBetaHeader = "codex-1"
	codexOriginator = "codex_cli_rs"

	codexAuthFile = "auth.json"

	// codexWindowToleranceMinutes absorbs rounding in limit_window_seconds
	// when matching a window against the 5h/7d shapes.
	codexWindowToleranceMinutes = 1
)

// CodexFetcher reads plan usage for one Codex home.
type CodexFetcher struct {
	// CodexHome is the account's CODEX_HOME (e.g. ~/.codex). Empty means the
	// ambient CODEX_HOME, then ~/.codex.
	CodexHome  string
	UsageURL   string
	HTTPClient *http.Client
}

func (f *CodexFetcher) Provider() string { return ProviderCodex }

func (f *CodexFetcher) Fetch(ctx context.Context) (Snapshot, error) {
	token, accountID, err := codexAuth(f.CodexHome)
	if err != nil {
		return Snapshot{}, err
	}

	url := f.UsageURL
	if url == "" {
		url = codexUsageURL
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"User-Agent":    codexUserAgent,
		"OpenAI-Beta":   codexBetaHeader,
		"originator":    codexOriginator,
	}
	if accountID != "" {
		headers["ChatGPT-Account-Id"] = accountID
	}

	data, err := requestJSON(ctx, f.HTTPClient, http.MethodGet, url, nil, headers)
	if err != nil {
		return Snapshot{}, err
	}
	return parseCodexUsage(data)
}

func codexAuth(codexHome string) (token, accountID string, err error) {
	if codexHome == "" {
		codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
	}
	if codexHome == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", "", fmt.Errorf("%w: codex home unknown", ErrNoCredentials)
		}
		codexHome = filepath.Join(home, ".codex")
	}

	path := filepath.Join(codexHome, codexAuthFile)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the resolved CODEX_HOME auth file
	if err != nil {
		return "", "", fmt.Errorf("%w: codex (%s)", ErrNoCredentials, path)
	}

	var doc struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", "", fmt.Errorf("parse codex auth: %w", err)
	}
	if doc.Tokens == nil || strings.TrimSpace(doc.Tokens.AccessToken) == "" {
		return "", "", fmt.Errorf("%w: codex auth carries no access token", ErrNoCredentials)
	}
	return doc.Tokens.AccessToken, doc.Tokens.AccountID, nil
}

type codexWindowPayload struct {
	UsedPercent        *float64        `json:"used_percent"`
	LimitWindowSeconds *float64        `json:"limit_window_seconds"`
	ResetAt            json.RawMessage `json:"reset_at"`
}

func (p *codexWindowPayload) window(fallbackMinutes int) *Window {
	if p == nil || p.UsedPercent == nil {
		return nil
	}
	minutes := fallbackMinutes
	if m, ok := p.windowMinutes(); ok {
		minutes = m
	}
	return &Window{
		UsedPercent:   clampPercent(*p.UsedPercent),
		WindowMinutes: minutes,
		ResetsAt:      parseResetTime(p.ResetAt),
	}
}

func (p *codexWindowPayload) windowMinutes() (int, bool) {
	if p == nil || p.LimitWindowSeconds == nil || *p.LimitWindowSeconds <= 0 {
		return 0, false
	}
	return int(math.Ceil(*p.LimitWindowSeconds / 60)), true
}

// classify names a window by its declared duration, so the mapping does not
// depend on which slot the payload happened to put it in.
func (p *codexWindowPayload) classify() string {
	minutes, ok := p.windowMinutes()
	if !ok {
		return ""
	}
	switch {
	case absInt(minutes-SessionWindowMinutes) <= codexWindowToleranceMinutes:
		return "session"
	case absInt(minutes-WeeklyWindowMinutes) <= codexWindowToleranceMinutes:
		return "weekly"
	default:
		return ""
	}
}

func parseCodexUsage(data []byte) (Snapshot, error) {
	var payload struct {
		PlanType  string `json:"plan_type"`
		RateLimit *struct {
			Primary   *codexWindowPayload `json:"primary_window"`
			Secondary *codexWindowPayload `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Snapshot{}, fmt.Errorf("parse codex usage: %w", err)
	}

	snap := Snapshot{Provider: ProviderCodex, Plan: payload.PlanType, UpdatedAt: time.Now()}
	if payload.RateLimit == nil {
		return snap, nil
	}

	primary, secondary := payload.RateLimit.Primary, payload.RateLimit.Secondary
	for _, w := range []*codexWindowPayload{primary, secondary} {
		switch w.classify() {
		case "session":
			if snap.Session == nil {
				snap.Session = w.window(SessionWindowMinutes)
			}
		case "weekly":
			if snap.Weekly == nil {
				snap.Weekly = w.window(WeeklyWindowMinutes)
			}
		}
	}
	// An undeclared duration falls back to the slot's conventional meaning:
	// primary is the rolling session window, secondary the weekly one.
	if snap.Session == nil && primary.classify() == "" {
		snap.Session = primary.window(SessionWindowMinutes)
	}
	if snap.Weekly == nil && secondary.classify() == "" {
		snap.Weekly = secondary.window(WeeklyWindowMinutes)
	}
	return snap, nil
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
