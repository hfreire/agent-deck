package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// geminiQuotaURL and geminiLoadURL are the Code Assist endpoints the Gemini
	// CLI itself calls; retrieveUserQuota needs the project id that
	// loadCodeAssist hands back for the signed-in account.
	geminiQuotaURL = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
	geminiLoadURL  = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"

	geminiTokenURL = "https://oauth2.googleapis.com/token"

	geminiCredentialsFile = "oauth_creds.json"

	// geminiTokenSkew keeps a cached token from being used up to the second it
	// expires.
	geminiTokenSkew = time.Minute

	// geminiWindowMinutes is the Code Assist bucket length. The endpoint states
	// a reset time but not a duration; the buckets refill hourly.
	geminiWindowMinutes = 60
)

// GeminiFetcher reads Code Assist quota for the signed-in Gemini CLI account.
type GeminiFetcher struct {
	// ConfigDir is the Gemini config dir (e.g. ~/.gemini). Empty means the default.
	ConfigDir string
	// BundleDir is the Gemini CLI's bundle dir. Empty means the local install
	// is located at first use (see oauthClient).
	BundleDir string
	// ProjectID short-circuits the loadCodeAssist lookup.
	ProjectID string
	// QuotaURL, LoadURL and TokenURL override the endpoints.
	QuotaURL   string
	LoadURL    string
	TokenURL   string
	HTTPClient *http.Client

	// The project and a refreshed token are per-account constants for the life
	// of the process; re-resolving them every poll triples the request count
	// for one number.
	mu            sync.Mutex
	cachedProject string
	cachedToken   string
	cachedExpiry  time.Time
	cachedOAuth   geminiOAuthClient
}

func (f *GeminiFetcher) Provider() string { return ProviderGemini }

func (f *GeminiFetcher) Fetch(ctx context.Context) (Snapshot, error) {
	token, err := f.accessToken(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	projectID, err := f.projectID(ctx, token)
	if err != nil {
		return Snapshot{}, err
	}

	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return Snapshot{}, err
	}
	data, err := requestJSON(ctx, f.HTTPClient, http.MethodPost, orDefault(f.QuotaURL, geminiQuotaURL), bytes.NewReader(body), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return Snapshot{}, err
	}
	return parseGeminiQuota(data)
}

func (f *GeminiFetcher) accessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	if f.cachedToken != "" && now.Before(f.cachedExpiry) {
		return f.cachedToken, nil
	}
	creds, err := geminiCredentials(f.ConfigDir)
	if err != nil {
		return "", err
	}
	if !creds.expired(now) && creds.AccessToken != "" {
		return creds.AccessToken, nil
	}
	token, expiry, err := f.refresh(ctx, creds.RefreshToken)
	if err != nil {
		return "", err
	}
	f.cachedToken, f.cachedExpiry = token, expiry
	return token, nil
}

func (f *GeminiFetcher) projectID(ctx context.Context, token string) (string, error) {
	if f.ProjectID != "" {
		return f.ProjectID, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cachedProject != "" {
		return f.cachedProject, nil
	}
	project, err := f.loadProjectID(ctx, token)
	if err != nil {
		return "", err
	}
	f.cachedProject = project
	return project, nil
}

// loadProjectID resolves the Code Assist project for the signed-in account.
func (f *GeminiFetcher) loadProjectID(ctx context.Context, token string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"metadata": map[string]string{"ideType": "GEMINI_CLI", "pluginType": "GEMINI"},
	})
	if err != nil {
		return "", err
	}
	data, err := requestJSON(ctx, f.HTTPClient, http.MethodPost, orDefault(f.LoadURL, geminiLoadURL), bytes.NewReader(body), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if err != nil {
		return "", err
	}
	var payload struct {
		Project string `json:"cloudaicompanionProject"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("parse gemini project: %w", err)
	}
	if strings.TrimSpace(payload.Project) == "" {
		return "", fmt.Errorf("gemini: no Code Assist project for this account")
	}
	return payload.Project, nil
}

// refresh exchanges the stored refresh token for an access token. The result is
// deliberately not written back to oauth_creds.json — see the package comment.
func (f *GeminiFetcher) refresh(ctx context.Context, refreshToken string) (string, time.Time, error) {
	if refreshToken == "" {
		return "", time.Time{}, fmt.Errorf("%w: gemini access token expired and no refresh token stored", ErrNoCredentials)
	}
	client, err := f.oauthClient()
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{
		"client_id":     {client.id},
		"client_secret": {client.secret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
	}
	data, err := requestJSON(ctx, f.HTTPClient, http.MethodPost, orDefault(f.TokenURL, geminiTokenURL), strings.NewReader(form.Encode()), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("refresh gemini token: %w", err)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", time.Time{}, fmt.Errorf("parse gemini token: %w", err)
	}
	if payload.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("gemini token response carries no access token")
	}
	expiry := time.Time{}
	if payload.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(payload.ExpiresIn)*time.Second - geminiTokenSkew)
	}
	return payload.AccessToken, expiry, nil
}

type geminiCreds struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiryDate   int64  `json:"expiry_date"`
}

func (c geminiCreds) expired(now time.Time) bool {
	return c.ExpiryDate <= 0 || time.UnixMilli(c.ExpiryDate).Before(now)
}

func geminiCredentials(configDir string) (geminiCreds, error) {
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return geminiCreds{}, fmt.Errorf("%w: gemini home unknown", ErrNoCredentials)
		}
		configDir = filepath.Join(home, ".gemini")
	}
	path := filepath.Join(configDir, geminiCredentialsFile)
	data, err := os.ReadFile(path) // #nosec G304 -- path is the resolved gemini config dir
	if err != nil {
		return geminiCreds{}, fmt.Errorf("%w: gemini (%s)", ErrNoCredentials, path)
	}
	var creds geminiCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return geminiCreds{}, fmt.Errorf("parse gemini credentials: %w", err)
	}
	if creds.AccessToken == "" && creds.RefreshToken == "" {
		return geminiCreds{}, fmt.Errorf("%w: gemini credentials are empty", ErrNoCredentials)
	}
	return creds, nil
}

type geminiBucket struct {
	ModelID           string  `json:"modelId"`
	RemainingFraction float64 `json:"remainingFraction"`
	ResetTime         string  `json:"resetTime"`
}

// geminiBucketNames are the display names the Gemini CLI uses. Anything absent
// is humanized from its model id rather than dropped, so a model released after
// this table still shows up.
var geminiBucketNames = map[string]string{
	"gemini-3.0-pro":        "3.0 Pro",
	"gemini-3.0-flash":      "3.0 Flash",
	"gemini-2.5-pro":        "Pro",
	"gemini-2.5-flash":      "Flash",
	"gemini-2.5-flash-lite": "Flash Lite",
}

// parseGeminiQuota maps the Code Assist buckets. Gemini publishes no single
// plan window, so the bucket closest to exhaustion stands in for the session
// window and every bucket is kept in Extra for the dashboard.
func parseGeminiQuota(data []byte) (Snapshot, error) {
	buckets, err := decodeGeminiBuckets(data)
	if err != nil {
		return Snapshot{}, err
	}

	snap := Snapshot{Provider: ProviderGemini, UpdatedAt: time.Now()}
	seen := make(map[string]bool, len(buckets))
	for _, b := range buckets {
		w := Window{
			UsedPercent:   clampPercent(math.Round((1 - b.RemainingFraction) * 100)),
			WindowMinutes: geminiWindowMinutes,
		}
		if t, err := time.Parse(time.RFC3339Nano, b.ResetTime); err == nil {
			w.ResetsAt = t
		}
		// The endpoint repeats a bucket per alias of the same underlying quota;
		// they are identical in both fields, so collapse them.
		key := fmt.Sprintf("%v-%v", w.UsedPercent, w.ResetsAt)
		if seen[key] {
			continue
		}
		seen[key] = true
		snap.Extra = append(snap.Extra, NamedWindow{Name: geminiBucketName(b.ModelID), Window: w})
	}

	for i := range snap.Extra {
		if snap.Session == nil || snap.Extra[i].UsedPercent > snap.Session.UsedPercent {
			w := snap.Extra[i].Window
			snap.Session = &w
		}
	}
	return snap, nil
}

func decodeGeminiBuckets(data []byte) ([]geminiBucket, error) {
	var wrapped struct {
		Buckets []geminiBucket `json:"buckets"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Buckets != nil {
		return wrapped.Buckets, nil
	}
	var bare []geminiBucket
	if err := json.Unmarshal(data, &bare); err != nil {
		return nil, fmt.Errorf("parse gemini quota: %w", err)
	}
	return bare, nil
}

func geminiBucketName(modelID string) string {
	if name, ok := geminiBucketNames[modelID]; ok {
		return name
	}
	parts := strings.Split(strings.TrimPrefix(modelID, "gemini-"), "-")
	for i, part := range parts {
		if part != "" {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
