package quota

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrNoOAuthClient reports that the local Gemini CLI install could not be found
// or carries no OAuth client. Unlike ErrNoCredentials this IS reported: the
// account is signed in, so the user expects a number and deserves to know why
// there isn't one.
var ErrNoOAuthClient = errors.New("quota: gemini cli oauth client not found")

const (
	// geminiScanBlock is how much of a bundle chunk is held at once. The real
	// chunks are ~16MB on a single line, so this is read in blocks rather than
	// slurped, with geminiScanOverlap carried between them so a constant lying
	// across a boundary is still matched.
	geminiScanBlock   = 1 << 20
	geminiScanOverlap = 4 << 10

	// geminiMaxBundleFile skips anything too large to be a bundle chunk. It
	// bounds the worst case when the directory holds something unexpected.
	geminiMaxBundleFile = 256 << 20
)

// geminiPackageSuffix is where npm puts the CLI under a global prefix.
var geminiPackageSuffix = filepath.Join("lib", "node_modules", "@google", "gemini-cli", "bundle")

// geminiGlobalPrefixes are the usual npm global prefixes, tried in order when
// `gemini` is not on PATH.
var geminiGlobalPrefixes = []string{"/opt/homebrew", "/usr/local", "/usr"}

var (
	geminiClientIDRe     = regexp.MustCompile(`OAUTH_CLIENT_ID\s*=\s*["']([^"']{10,256})["']`)
	geminiClientSecretRe = regexp.MustCompile(`OAUTH_CLIENT_SECRET\s*=\s*["']([^"']{5,256})["']`)
)

// geminiOAuthClient is the OAuth client the local Gemini CLI authenticates
// with. It is read from that install rather than compiled in: it is Google's
// client, not ours, and a refresh_token grant is only honoured for the client
// that issued the token, so the CLI's own copy is both the correct value and
// the only one that can work.
type geminiOAuthClient struct {
	id     string
	secret string
}

func (c geminiOAuthClient) usable() bool { return c.id != "" && c.secret != "" }

// oauthClient resolves and memoises the client for this fetcher.
//
// Callers hold f.mu (refresh runs inside accessToken's critical section), so
// this reads and writes the cache fields directly. The scrape reads megabytes,
// so it must happen once per process, and a bundle that disappears afterwards
// must not invalidate what was already read.
func (f *GeminiFetcher) oauthClient() (geminiOAuthClient, error) {
	if f.cachedOAuth.usable() {
		return f.cachedOAuth, nil
	}
	dir := f.BundleDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return geminiOAuthClient{}, fmt.Errorf("%w: home unknown", ErrNoOAuthClient)
		}
		if dir, err = geminiBundleDir(exec.LookPath, home); err != nil {
			return geminiOAuthClient{}, err
		}
	}
	client, err := geminiOAuthClientFromDir(dir)
	if err != nil {
		return geminiOAuthClient{}, err
	}
	f.cachedOAuth = client
	return client, nil
}

// geminiBundleDir locates the CLI's bundle directory, preferring the install
// actually on PATH so a machine with several node prefixes resolves the same
// one the user's `gemini` runs from.
func geminiBundleDir(lookPath func(string) (string, error), home string) (string, error) {
	tried := make([]string, 0, len(geminiGlobalPrefixes)+2)

	if bin, err := lookPath("gemini"); err == nil {
		// npm links the command at <prefix>/bin/gemini -> <pkg>/bundle/gemini.js.
		if resolved, rerr := filepath.EvalSymlinks(bin); rerr == nil {
			dir := filepath.Dir(resolved)
			if isDir(dir) {
				return dir, nil
			}
			tried = append(tried, dir)
		}
	}

	prefixes := append([]string{filepath.Join(home, ".npm-global")}, geminiGlobalPrefixes...)
	for _, prefix := range prefixes {
		dir := filepath.Join(prefix, geminiPackageSuffix)
		if isDir(dir) {
			return dir, nil
		}
		tried = append(tried, dir)
	}
	return "", fmt.Errorf("%w: no gemini cli install found (looked in %s)", ErrNoOAuthClient, strings.Join(tried, ", "))
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// geminiOAuthClientFromDir scans the bundle's .js files for the client. The
// chunk carrying it is hash-named and moves between releases, so every chunk is
// a candidate; they are scanned in name order for a deterministic answer.
func geminiOAuthClientFromDir(dir string) (geminiOAuthClient, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return geminiOAuthClient{}, fmt.Errorf("%w: %s unreadable: %v", ErrNoOAuthClient, dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		if info, ierr := entry.Info(); ierr == nil && info.Size() > geminiMaxBundleFile {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		file, ferr := os.Open(filepath.Join(dir, name)) // #nosec G304 -- a .js file in the resolved bundle dir
		if ferr != nil {
			continue
		}
		client, ok := scanGeminiOAuthClient(file)
		_ = file.Close()
		if ok {
			return client, nil
		}
	}
	return geminiOAuthClient{}, fmt.Errorf("%w: no chunk in %s declares OAUTH_CLIENT_ID and OAUTH_CLIENT_SECRET", ErrNoOAuthClient, dir)
}

// scanGeminiOAuthClient reads r in blocks, carrying geminiScanOverlap bytes
// between them so a declaration split across a block boundary still matches.
func scanGeminiOAuthClient(r io.Reader) (geminiOAuthClient, bool) {
	var client geminiOAuthClient
	buf := make([]byte, geminiScanBlock+geminiScanOverlap)
	carried := 0

	for {
		n, err := io.ReadFull(r, buf[carried:])
		window := buf[:carried+n]

		if client.id == "" {
			if m := geminiClientIDRe.FindSubmatch(window); m != nil {
				client.id = string(m[1])
			}
		}
		if client.secret == "" {
			if m := geminiClientSecretRe.FindSubmatch(window); m != nil {
				client.secret = string(m[1])
			}
		}
		if client.usable() {
			return client, true
		}
		if err != nil {
			return geminiOAuthClient{}, false
		}

		carried = min(len(window), geminiScanOverlap)
		copy(buf, window[len(window)-carried:])
	}
}
