package quota

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Deliberately unlike a real Google client: values shaped like one trip secret
// scanning on push even when they are fake.
const (
	testClientID     = "fake-client-id-for-tests"
	testClientSecret = "fake-client-secret-for-tests"
)

// bundleSource is the shape the Gemini CLI's esbuild output actually carries.
func bundleSource() string {
	return fmt.Sprintf("var OAUTH_CLIENT_ID = %q;\nvar OAUTH_CLIENT_SECRET = %q;\n", testClientID, testClientSecret)
}

func writeBundle(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanGeminiOAuthClient(t *testing.T) {
	client, ok := scanGeminiOAuthClient(strings.NewReader(bundleSource()))
	if !ok {
		t.Fatal("scanGeminiOAuthClient found nothing in a bundle that carries both constants")
	}
	if client.id != testClientID || client.secret != testClientSecret {
		t.Fatalf("client = %+v, want id %q secret %q", client, testClientID, testClientSecret)
	}
}

// The real chunks are ~16MB on one line, so the scan reads in blocks. A
// constant that straddles a block boundary must still be found.
func TestScanGeminiOAuthClientAcrossBlockBoundary(t *testing.T) {
	padding := strings.Repeat("x", geminiScanBlock-20)
	client, ok := scanGeminiOAuthClient(strings.NewReader(padding + bundleSource()))
	if !ok {
		t.Fatal("constants straddling a block boundary were missed")
	}
	if client.id != testClientID || client.secret != testClientSecret {
		t.Fatalf("client = %+v", client)
	}
}

func TestScanGeminiOAuthClientNeedsBothConstants(t *testing.T) {
	only := fmt.Sprintf("var OAUTH_CLIENT_ID = %q;\n", testClientID)
	if _, ok := scanGeminiOAuthClient(strings.NewReader(only)); ok {
		t.Fatal("a bundle with only the id must not report a usable client")
	}
}

func TestGeminiOAuthClientFromDirScansEveryChunk(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, dir, "chunk-AAAA.js", "var somethingElse = 1;\n")
	writeBundle(t, dir, "chunk-BBBB.js", bundleSource())

	client, err := geminiOAuthClientFromDir(dir)
	if err != nil {
		t.Fatalf("geminiOAuthClientFromDir: %v", err)
	}
	if client.id != testClientID {
		t.Fatalf("id = %q, want %q", client.id, testClientID)
	}
}

func TestGeminiOAuthClientFromDirMissing(t *testing.T) {
	dir := t.TempDir()
	writeBundle(t, dir, "chunk-AAAA.js", "var somethingElse = 1;\n")

	_, err := geminiOAuthClientFromDir(dir)
	if !errors.Is(err, ErrNoOAuthClient) {
		t.Fatalf("err = %v, want ErrNoOAuthClient", err)
	}
	// The error has to name where we looked, or it is unactionable.
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("err = %v, want it to name %q", err, dir)
	}
}

func TestGeminiBundleDirFollowsTheBinSymlink(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "lib", "node_modules", "@google", "gemini-cli", "bundle")
	writeBundle(t, bundle, "gemini.js", "#!/usr/bin/env node\n")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(bin, "gemini")
	if err := os.Symlink(filepath.Join(bundle, "gemini.js"), link); err != nil {
		t.Fatal(err)
	}

	dir, err := geminiBundleDir(func(string) (string, error) { return link, nil }, root)
	if err != nil {
		t.Fatalf("geminiBundleDir: %v", err)
	}
	// The resolver follows symlinks, and on macOS /var is itself one.
	want, err := filepath.EvalSymlinks(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
}

func TestGeminiBundleDirFallsBackToNpmGlobal(t *testing.T) {
	home := t.TempDir()
	bundle := filepath.Join(home, ".npm-global", "lib", "node_modules", "@google", "gemini-cli", "bundle")
	writeBundle(t, bundle, "chunk-AAAA.js", bundleSource())

	dir, err := geminiBundleDir(func(string) (string, error) { return "", errors.New("not on PATH") }, home)
	if err != nil {
		t.Fatalf("geminiBundleDir: %v", err)
	}
	if dir != bundle {
		t.Fatalf("dir = %q, want %q", dir, bundle)
	}
}

func TestGeminiBundleDirWithNoInstall(t *testing.T) {
	_, err := geminiBundleDir(func(string) (string, error) { return "", errors.New("not on PATH") }, t.TempDir())
	if !errors.Is(err, ErrNoOAuthClient) {
		t.Fatalf("err = %v, want ErrNoOAuthClient", err)
	}
}

// The whole point of the extraction: the refresh grant must carry the client
// the local Gemini CLI was built with, and nothing may be compiled in.
func TestGeminiRefreshUsesTheClientFromTheBundle(t *testing.T) {
	bundle := t.TempDir()
	writeBundle(t, bundle, "chunk-AAAA.js", bundleSource())

	var gotID, gotSecret, gotGrant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		gotID, gotSecret, gotGrant = r.Form.Get("client_id"), r.Form.Get("client_secret"), r.Form.Get("grant_type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"fresh-token","expires_in":3600}`))
	}))
	defer srv.Close()

	f := &GeminiFetcher{BundleDir: bundle, TokenURL: srv.URL}
	token, expiry, err := f.refresh(t.Context(), "stored-refresh-token")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if token != "fresh-token" {
		t.Fatalf("token = %q", token)
	}
	if expiry.IsZero() || expiry.After(time.Now().Add(time.Hour)) {
		t.Fatalf("expiry = %v, want under an hour out", expiry)
	}
	if gotID != testClientID || gotSecret != testClientSecret {
		t.Fatalf("grant sent id=%q secret=%q, want the bundle's", gotID, gotSecret)
	}
	if gotGrant != "refresh_token" {
		t.Fatalf("grant_type = %q", gotGrant)
	}
}

func TestGeminiRefreshWithoutABundleIsATypedError(t *testing.T) {
	f := &GeminiFetcher{BundleDir: t.TempDir(), TokenURL: "http://127.0.0.1:0"}
	_, _, err := f.refresh(t.Context(), "stored-refresh-token")
	if !errors.Is(err, ErrNoOAuthClient) {
		t.Fatalf("err = %v, want ErrNoOAuthClient", err)
	}
}

// The scrape costs a multi-megabyte read; it must happen once per process.
func TestGeminiOAuthClientIsCachedPerFetcher(t *testing.T) {
	bundle := t.TempDir()
	writeBundle(t, bundle, "chunk-AAAA.js", bundleSource())

	f := &GeminiFetcher{BundleDir: bundle}
	first, err := f.oauthClient()
	if err != nil {
		t.Fatalf("oauthClient: %v", err)
	}
	if err := os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}
	second, err := f.oauthClient()
	if err != nil {
		t.Fatalf("second oauthClient after the bundle vanished: %v", err)
	}
	if first != second {
		t.Fatalf("cached client changed: %+v then %+v", first, second)
	}
}

// Runs against whatever Gemini CLI is installed on this machine, and skips when
// there is none. It asserts shape only — asserting the values would re-embed
// the very constants this extraction exists to keep out of the tree.
func TestGeminiOAuthClientFromTheRealInstall(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	dir, err := geminiBundleDir(exec.LookPath, home)
	if err != nil {
		t.Skipf("no gemini cli installed: %v", err)
	}
	client, err := geminiOAuthClientFromDir(dir)
	if err != nil {
		t.Fatalf("scraping the installed bundle at %s: %v", dir, err)
	}
	if !strings.HasSuffix(client.id, ".apps.googleusercontent.com") {
		t.Fatalf("client id %q does not look like a Google OAuth client", client.id)
	}
	if len(client.secret) < 10 {
		t.Fatalf("client secret is implausibly short (%d chars)", len(client.secret))
	}
	t.Logf("bundle %s -> client id ...%s, secret %d chars", dir, client.id[len(client.id)-30:], len(client.secret))
}
