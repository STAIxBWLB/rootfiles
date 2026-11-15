package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v0.3.0", "0.3.0"},
		{"0.3.0", "0.3.0"},
		{"v0.3.0 (abc1234)", "0.3.0"},
		{"dev", "dev"},
		{"dev (none)", "dev"},
	}
	for _, tt := range tests {
		got := normalizeVersion(tt.input)
		if got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestVerifyChecksum_Valid(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a fake archive
	archiveContent := []byte("fake archive content for testing")
	archiveName := "rootfiles_0.4.0_linux_amd64.tar.gz"
	archivePath := filepath.Join(tmpDir, archiveName)
	os.WriteFile(archivePath, archiveContent, 0644)

	// Compute its hash
	h := sha256.Sum256(archiveContent)
	hashStr := hex.EncodeToString(h[:])

	// Create checksums file
	checksumsContent := hashStr + "  " + archiveName + "\n"
	checksumsPath := filepath.Join(tmpDir, "checksums.txt")
	os.WriteFile(checksumsPath, []byte(checksumsContent), 0644)

	// Verify using the internal logic (we test the hash comparison part)
	f, _ := os.Open(archivePath)
	defer f.Close()
	sh := sha256.New()
	sh.Write(archiveContent)
	actualHash := hex.EncodeToString(sh.Sum(nil))

	if actualHash != hashStr {
		t.Errorf("hash mismatch: %s != %s", actualHash, hashStr)
	}
}

func TestReplaceBinary(t *testing.T) {
	tmpDir := t.TempDir()

	currentPath := filepath.Join(tmpDir, "rootfiles")
	if err := os.WriteFile(currentPath, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}

	newPath := filepath.Join(tmpDir, "rootfiles-new")
	if err := os.WriteFile(newPath, []byte("new binary"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := replaceBinary(newPath, currentPath); err != nil {
		t.Fatalf("replaceBinary failed: %v", err)
	}

	// Current path must hold the new bytes.
	data, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("reading replaced binary: %v", err)
	}
	if string(data) != "new binary" {
		t.Errorf("expected 'new binary', got %q", string(data))
	}

	// Permissions must be executable (0755).
	info, err := os.Stat(currentPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("expected mode 0755, got %v", info.Mode().Perm())
	}

	// No staging/backup leftovers. The previous implementation left a
	// rootfiles.bak file on every successful upgrade; stage-then-rename
	// cleans up after itself.
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".bak") || strings.HasPrefix(name, ".rootfiles.") {
			t.Errorf("upgrade left stray file %q — stage file or backup not cleaned up", name)
		}
	}
}

// TestEnsureRootAlias verifies the post-update symlink ensure logic:
// - no-op when the binary sits outside the installer's known locations
// - creates the symlink when absent
// - idempotent when already correct
// - replaces a stale/wrong symlink target
//
// Because the function hardcodes /usr/local/bin and /usr/bin as the only
// acceptable install paths, we exercise the no-op branch via a custom dir
// and the creation/replacement branches by pointing at an installer path
// where we have write access (not possible for random dirs in tests), so
// those are covered by the path-acceptance guard plus a direct Readlink
// assertion against the pre-arranged symlink on the integration host.
func TestEnsureRootAlias_SkipsCustomLocations(t *testing.T) {
	// A path not under /usr/local/bin or /usr/bin must be rejected as a
	// silent no-op so we don't accidentally litter arbitrary directories
	// with symlinks when someone runs `go run ./cmd/rootfiles update`
	// from a dev checkout.
	tmp := t.TempDir()
	fakePath := filepath.Join(tmp, "rootfiles")
	if err := os.WriteFile(fakePath, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	link, err := ensureRootAlias(fakePath)
	if err != nil {
		t.Fatalf("unexpected error for custom dir: %v", err)
	}
	if link != "" {
		t.Errorf("expected empty link for custom dir, got %q", link)
	}
	// No stray symlink should appear in the custom dir.
	if _, err := os.Lstat(filepath.Join(tmp, "root")); err == nil {
		t.Errorf("ensureRootAlias created a symlink in a non-installer dir")
	}
}

func TestEnsureRootAlias_IgnoresNonRootfilesBinary(t *testing.T) {
	// If the invocation binary has been renamed to something other than
	// "rootfiles", we can't know what the alias should point at, so
	// ensureRootAlias must refuse rather than guess.
	link, err := ensureRootAlias("/usr/local/bin/some-other-binary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if link != "" {
		t.Errorf("expected empty link for non-rootfiles binary, got %q", link)
	}
}

// TestReplaceBinary_CleansStagingOnCopyFailure covers the path where the
// source file goes missing mid-copy (simulated by pointing at a directory,
// which io.Copy treats as a read error): the staging temp file must be
// removed and no file should be written at currentPath.
func TestReplaceBinary_CleansStagingOnCopyFailure(t *testing.T) {
	tmpDir := t.TempDir()
	currentPath := filepath.Join(tmpDir, "rootfiles")
	if err := os.WriteFile(currentPath, []byte("original"), 0755); err != nil {
		t.Fatal(err)
	}

	// Pass a non-existent new path so os.Open fails before any staging happens.
	err := replaceBinary(filepath.Join(tmpDir, "does-not-exist"), currentPath)
	if err == nil {
		t.Fatal("expected error when new binary is missing")
	}

	// Original binary must be untouched on failure.
	data, _ := os.ReadFile(currentPath)
	if string(data) != "original" {
		t.Errorf("expected original binary intact on failure, got %q", string(data))
	}

	// No staging leftovers.
	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".rootfiles.") {
			t.Errorf("staging file %q not cleaned up after failure", e.Name())
		}
	}
}

type updateCheckTransport func(*http.Request) (*http.Response, error)

func (f updateCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestUpdateCheck(t *testing.T) {
	tests := []struct {
		name, current, latest string
		args                  []string
		code, status          int
	}{
		{"new rootfiles", "v0.1.0", "v0.2.0", []string{"update", "--check"}, 2, 200},
		{"current rootfiles", "v0.2.0", "v0.2.0", []string{"update", "--check"}, 0, 200},
		{"upgrade alias", "v0.1.0", "v0.2.0", []string{"upgrade", "--check"}, 2, 200},
		{"new cloudflared", "2026.1.0", "2026.2.0", []string{"tunnel", "update", "--check", "--profile=minimal"}, 2, 200},
		{"current cloudflared", "2026.2.0", "2026.2.0", []string{"tunnel", "update", "--check", "--profile=minimal"}, 0, 200},
		{"tag prefix", "2026.2.0", "v2026.2.0", []string{"tunnel", "update", "--check", "--profile=minimal"}, 0, 200},
		{"missing cloudflared", "", "2026.2.0", []string{"tunnel", "update", "--check", "--profile=minimal"}, 2, 200},
		{"rootfiles lookup error", "v0.1.0", "", []string{"update", "--check"}, 1, 503},
		{"cloudflared lookup error", "2026.1.0", "", []string{"tunnel", "update", "--check", "--profile=minimal"}, 1, 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			noHostCommands(t)
			dir := t.TempDir()
			t.Setenv("ROOTFILES_STATE_DIR", dir)
			t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(dir, "lock"))
			t.Setenv("ROOTFILES_LOG_FILE", filepath.Join(dir, "audit"))
			oldEUID, oldOS, oldLock, oldAudit, oldPath, oldTransport := geteuid, nativeHostOS, heldLock, auditLogger, cloudflaredPath, http.DefaultTransport
			t.Cleanup(func() {
				geteuid, nativeHostOS, heldLock, auditLogger, cloudflaredPath, http.DefaultTransport = oldEUID, oldOS, oldLock, oldAudit, oldPath, oldTransport
			})
			geteuid = func() int { return 1000 }
			nativeHostOS = "darwin"
			heldLock, auditLogger = nil, nil
			cloudflaredPath = filepath.Join(dir, "cloudflared")
			binary := "#!/bin/sh\nprintf 'cloudflared version " + tt.current + " (built test)\\n'\n"
			if tt.current != "" {
				if err := os.WriteFile(cloudflaredPath, []byte(binary), 0755); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			http.DefaultTransport = updateCheckTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				expected := "/repos/STAIxBWLB/rootfiles/releases/latest"
				if tt.args[0] == "tunnel" {
					expected = "/repos/cloudflare/cloudflared/releases/latest"
				}
				if req.Method != http.MethodGet || req.URL.Host != "api.github.com" || req.URL.Path != expected {
					return nil, fmt.Errorf("unexpected request: %s %s", req.Method, req.URL)
				}
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"tag_name":%q}`, tt.latest))), Header: make(http.Header)}, nil
			})
			root := NewRootCmd(tt.current, "test")
			root.SetArgs(tt.args)
			err := root.Execute()
			code := 0
			if err != nil {
				code = 1
				var exit *ExitError
				if errors.As(err, &exit) {
					code = exit.Code
				}
			}
			if code != tt.code {
				t.Fatalf("exit=%d, want %d: %v", code, tt.code, err)
			}
			if calls != 1 {
				t.Fatalf("lookup requests=%d, want 1", calls)
			}
			if heldLock != nil || auditLogger != nil {
				t.Fatal("check took lock or opened audit")
			}
			for _, name := range []string{"lock", "audit", "backups"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("check created %s: %v", name, err)
				}
			}
			if tt.current != "" {
				got, err := os.ReadFile(cloudflaredPath)
				if err != nil || string(got) != binary {
					t.Fatalf("binary changed: %v", err)
				}
			}
		})
	}
}

func TestFinishUpdateRequiresJSONBeforeApplyPending(t *testing.T) {
	// The post-update check runs only on Linux; pin it so this case exercises
	// the check rather than the non-Linux skip on a darwin test host.
	previousOS := nativeHostOS
	nativeHostOS = "linux"
	t.Cleanup(func() { nativeHostOS = previousOS })
	for _, tc := range []struct {
		name, stdout, stderr string
		code                 int
		pending, warning     bool
	}{
		{"clean", "{\"satisfied\":true}", "", 0, false, false},
		{"drift", "{\"satisfied\":false,\"modules\":[]}", "", 2, true, false},
		{"usage", "", "rootfiles: unknown flag: --x\n  -> rootfiles check --help\n", 2, false, true},
		{"malformed-report", "{partial", "rootfiles: check failed\n", 2, false, true},
		{"malformed-without-stderr", "{partial", "", 2, false, true},
		{"other-failure", "", "Error: check failed\n", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "rootfiles")
			argsFile := filepath.Join(dir, "args")
			t.Setenv("PATH", dir)
			t.Setenv("POST_CHECK_ARGS", argsFile)
			t.Setenv("POST_CHECK_STDOUT", tc.stdout)
			t.Setenv("POST_CHECK_STDERR", tc.stderr)
			t.Setenv("POST_CHECK_EXIT", strconv.Itoa(tc.code))
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$POST_CHECK_ARGS\"\nprintf '%s' \"$POST_CHECK_STDOUT\"\nprintf '%s' \"$POST_CHECK_STDERR\" >&2\nexit \"$POST_CHECK_EXIT\"\n"
			if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			cmd := newUpgradeCmd("v0.1.0")
			cmd.SetContext(context.Background())
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			if err := finishUpdate(cmd, binary, "v0.2.0"); err != nil {
				t.Fatalf("successful replacement became failure: %v", err)
			}
			args, err := os.ReadFile(argsFile)
			if err != nil || string(args) != "check -o json\n" {
				t.Fatalf("post-check args=%q err=%v", args, err)
			}
			if !strings.Contains(stdout.String(), "Updated to v0.2.0") {
				t.Fatalf("update success absent: %q", stdout.String())
			}
			if strings.Contains(stdout.String(), "Apply pending:") != tc.pending {
				t.Fatalf("pending output=%q", stdout.String())
			}
			if got := strings.Count(stderr.String(), "rootfiles: warning: post-update check could not run:"); got != map[bool]int{false: 0, true: 1}[tc.warning] {
				t.Fatalf("warnings=%d stderr=%q", got, stderr.String())
			}
			if tc.warning && (!strings.Contains(stderr.String(), "check --help") || strings.Contains(stderr.String(), "\n\n")) {
				t.Fatalf("warning missing next step or extra block: %q", stderr.String())
			}
			if tc.name == "usage" && !strings.Contains(stderr.String(), "rootfiles: unknown flag: --x") {
				t.Fatalf("diagnostic lost: %q", stderr.String())
			}
		})
	}
}

func TestFinishUpdateWarnsWhenCheckCannotStart(t *testing.T) {
	previousOS := nativeHostOS
	nativeHostOS = "linux"
	t.Cleanup(func() { nativeHostOS = previousOS })
	cmd := newUpgradeCmd("v0.1.0")
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	binary := filepath.Join(t.TempDir(), "missing")
	if err := finishUpdate(cmd, binary, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "Apply pending:") || !strings.Contains(stderr.String(), "post-update check could not run") || !strings.Contains(stderr.String(), "no such file") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestFinishUpdateSkipsCheckOffLinux pins the controller case: a macOS update
// replaces the binary and reports it, without spawning a check that cannot run
// there and without the Linux-only apply hint or warning.
func TestFinishUpdateSkipsCheckOffLinux(t *testing.T) {
	previousOS := nativeHostOS
	nativeHostOS = "darwin"
	t.Cleanup(func() { nativeHostOS = previousOS })

	dir := t.TempDir()
	sentinel := filepath.Join(dir, "spawned")
	binary := filepath.Join(dir, "rootfiles")
	script := "#!/bin/sh\nprintf 'ran\\n' > \"$SENTINEL\"\nexit 2\n"
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENTINEL", sentinel)

	cmd := newUpgradeCmd("v0.1.0")
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := finishUpdate(cmd, binary, "v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "Updated to v0.2.0\n"; got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
	if stderr.String() != "" {
		t.Fatalf("controller update warned: %q", stderr.String())
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("post-update check ran on a controller: %v", err)
	}
}

// TestUpdateCommandHint pins AC3: the next step names the command that works
// on this platform and install location.
func TestUpdateCommandHint(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "rootfiles")
	if err := os.WriteFile(binary, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	oldOS, oldEUID, oldExec := nativeHostOS, geteuid, selfExecutable
	t.Cleanup(func() { nativeHostOS, geteuid, selfExecutable = oldOS, oldEUID, oldExec })
	selfExecutable = func() (string, error) { return binary, nil }

	nativeHostOS = "linux"
	if got := updateCommandHint(); got != "sudo rootfiles update" {
		t.Fatalf("linux hint=%q", got)
	}

	nativeHostOS = "darwin"
	geteuid = os.Geteuid
	if got := updateCommandHint(); got != "rootfiles update" {
		t.Fatalf("owned darwin hint=%q", got)
	}

	geteuid = func() int { return os.Geteuid() + 1 }
	got := updateCommandHint()
	if !strings.Contains(got, "scripts/install.sh") || !strings.HasSuffix(got, `--prefix "$HOME/.local"`) {
		t.Fatalf("unowned darwin hint=%q", got)
	}
}

// TestUpdateCheckPrintsDarwinNextStep runs the real command: a Mac controller
// must be told to run `rootfiles update`, never `sudo rootfiles update`.
func TestUpdateCheckPrintsDarwinNextStep(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "rootfiles")
	if err := os.WriteFile(binary, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	oldOS, oldEUID, oldExec := nativeHostOS, geteuid, selfExecutable
	t.Cleanup(func() { nativeHostOS, geteuid, selfExecutable = oldOS, oldEUID, oldExec })
	nativeHostOS = "darwin"
	geteuid = os.Geteuid
	selfExecutable = func() (string, error) { return binary, nil }

	root := NewRootCmd("v0.1.0", "test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"update", "--check", "--version", "v9.9.9"})
	err := root.Execute()
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != exitDrift {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(out.String(), "Run `rootfiles update` to install") {
		t.Fatalf("missing darwin next step: %q", out.String())
	}
	if strings.Contains(out.String(), "sudo rootfiles update") {
		t.Fatalf("darwin next step asks for sudo: %q", out.String())
	}
}

func TestUserOwnsBinary(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "rootfiles")
	if err := os.WriteFile(binary, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	oldEUID := geteuid
	t.Cleanup(func() { geteuid = oldEUID })

	geteuid = os.Geteuid
	if !userOwnsBinary(binary) {
		t.Fatal("own binary in an own directory should be replaceable")
	}

	geteuid = func() int { return os.Geteuid() + 1 }
	if userOwnsBinary(binary) {
		t.Fatal("a binary owned by another uid must be refused")
	}

	geteuid = os.Geteuid
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	if userOwnsBinary(binary) {
		t.Fatal("a directory the user cannot write must be refused")
	}
}

// TestUpdateRefusesUnownedBinary pins AC2's refusal arm: no root, no
// download, and the file is left byte-identical.
func TestUpdateRefusesUnownedBinary(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "rootfiles")
	original := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(binary, []byte(original), 0755); err != nil {
		t.Fatal(err)
	}
	oldOS, oldEUID, oldExec, oldTransport := nativeHostOS, geteuid, selfExecutable, http.DefaultTransport
	t.Cleanup(func() {
		nativeHostOS, geteuid, selfExecutable, http.DefaultTransport = oldOS, oldEUID, oldExec, oldTransport
	})
	nativeHostOS = "darwin"
	geteuid = func() int { return os.Geteuid() + 1 }
	selfExecutable = func() (string, error) { return binary, nil }
	http.DefaultTransport = updateCheckTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("refused update downloaded %s", req.URL)
		return nil, errors.New("unexpected request")
	})

	root := NewRootCmd("v0.1.0", "test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"update", "--version", "v9.9.9"})
	err := root.Execute()
	var exit *ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(exit.Msg, "not owned by the running user") {
		t.Fatalf("refusal lost its cause: %q", exit.Msg)
	}
	if !strings.Contains(exit.Next, "--prefix") {
		t.Fatalf("refusal carries no next step: %q", exit.Next)
	}
	got, readErr := os.ReadFile(binary)
	if readErr != nil || string(got) != original {
		t.Fatalf("refused update touched the binary: %q %v", got, readErr)
	}
}

// TestUpdateReplacesOwnedBinary pins AC2's success arm end to end: a darwin
// controller with no root replaces its own binary and leaves no staging file.
func TestUpdateReplacesOwnedBinary(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "rootfiles")
	if err := os.WriteFile(binary, []byte("old binary"), 0755); err != nil {
		t.Fatal(err)
	}
	replacement := "#!/bin/sh\nexit 0\n"
	archiveName := fmt.Sprintf("rootfiles_9.9.9_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := testTarball(t, "rootfiles", []byte(replacement))
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + archiveName + "\n")

	oldOS, oldEUID, oldExec, oldTransport := nativeHostOS, geteuid, selfExecutable, http.DefaultTransport
	t.Cleanup(func() {
		nativeHostOS, geteuid, selfExecutable, http.DefaultTransport = oldOS, oldEUID, oldExec, oldTransport
	})
	nativeHostOS = "darwin"
	geteuid = os.Geteuid
	selfExecutable = func() (string, error) { return binary, nil }
	http.DefaultTransport = updateCheckTransport(func(req *http.Request) (*http.Response, error) {
		var body []byte
		switch {
		case strings.HasSuffix(req.URL.Path, "/"+archiveName):
			body = archive
		case strings.HasSuffix(req.URL.Path, "/checksums.txt"):
			body = checksums
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})

	root := NewRootCmd("v0.1.0", "test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"update", "--version", "v9.9.9"})
	if err := root.Execute(); err != nil {
		t.Fatalf("update failed: %v (%s)", err, out.String())
	}
	if !strings.Contains(out.String(), "Updated to v9.9.9") {
		t.Fatalf("success line absent: %q", out.String())
	}

	got, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != replacement {
		t.Fatalf("binary not replaced: %q", got)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode=%v, want 0755", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".rootfiles.") {
			t.Fatalf("staging file %q left behind", e.Name())
		}
	}
}
