package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The installer and `rootfiles update` are two halves of one contract: the
// installer writes the binary, update replaces it. These cases run the real
// script with a private PATH (curl, uname and id stubbed) so the destination
// rules, checksum verification and the non-root sudo-path skip are exercised
// without root, without the network and without writing outside t.TempDir.

// testTarball builds a release-shaped archive holding one regular file.
func testTarball(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type installFixture struct {
	root          string
	home          string
	binDir        string
	script        string
	archivePath   string
	checksumsPath string
	env           []string
}

func newInstallFixture(t *testing.T, osName, arch, uid string) *installFixture {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("installer: %v", err)
	}

	f := &installFixture{
		root:          t.TempDir(),
		script:        script,
		archivePath:   "",
		checksumsPath: "",
	}
	f.home = filepath.Join(f.root, "home")
	f.binDir = filepath.Join(f.root, "fakebin")
	for _, dir := range []string{f.home, f.binDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.archivePath = filepath.Join(f.root, "release.tar.gz")
	f.checksumsPath = filepath.Join(f.root, "checksums.txt")
	if err := os.WriteFile(f.archivePath, testTarball(t, "rootfiles", []byte("#!/bin/sh\necho 'rootfiles test'\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	f.writeStub(t, "curl", `#!/bin/sh
out=""
url=""
while [ $# -gt 0 ]; do
    case "$1" in
        -o) out="$2"; shift 2 ;;
        -*) shift ;;
        *) url="$1"; shift ;;
    esac
done
case "$url" in
    *checksums.txt) cp "$FAKE_CHECKSUMS" "$out" ;;
    *.tar.gz) cp "$FAKE_ARCHIVE" "$out" ;;
    *) echo "fake curl: unexpected url $url" >&2; exit 22 ;;
esac
`)
	f.writeStub(t, "uname", "#!/bin/sh\ncase \"$1\" in\n    -m) echo "+arch+" ;;\n    *) echo "+osName+" ;;\nesac\n")
	f.writeStub(t, "id", "#!/bin/sh\necho "+uid+"\n")

	f.env = append(os.Environ(),
		"HOME="+f.home,
		"PATH="+f.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_ARCHIVE="+f.archivePath,
		"FAKE_CHECKSUMS="+f.checksumsPath,
	)
	return f
}

func (f *installFixture) writeStub(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.binDir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// release writes checksums.txt for the archive name the script will request.
func (f *installFixture) release(t *testing.T, version, osName, arch string) {
	t.Helper()
	data, err := os.ReadFile(f.archivePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("rootfiles_%s_%s_%s.tar.gz", version, osName, arch)
	line := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	if err := os.WriteFile(f.checksumsPath, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *installFixture) failDownload(t *testing.T) {
	t.Helper()
	f.writeStub(t, "curl", "#!/bin/sh\necho \"fake curl: network disabled\" >&2\nexit 22\n")
}

func (f *installFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{f.script}, args...)...)
	cmd.Env = f.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestInstallScriptInstallsIntoUserPrefix pins AC1: a darwin controller with
// no sudo lands in $HOME/.local/bin, --prefix overrides it, and a non-root
// install of any platform skips the sudo path check and the root alias.
func TestInstallScriptInstallsIntoUserPrefix(t *testing.T) {
	for _, tc := range []struct {
		name, osName, arch, uid string
		prefix                  bool
	}{
		{"darwin default", "Darwin", "arm64", "1000", false},
		{"darwin prefix", "Darwin", "arm64", "1000", true},
		{"linux prefix", "Linux", "x86_64", "1000", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t, tc.osName, tc.arch, tc.uid)
			arch := tc.arch
			if arch == "x86_64" {
				arch = "amd64"
			}
			f.release(t, "0.0.1", strings.ToLower(tc.osName), arch)

			args := []string{"--version", "v0.0.1"}
			want := filepath.Join(f.home, ".local", "bin")
			if tc.prefix {
				want = filepath.Join(f.root, "opt", "bin")
				args = append(args, "--prefix", filepath.Join(f.root, "opt"))
			}

			out, err := f.run(t, args...)
			if err != nil {
				t.Fatalf("install failed: %v\n%s", err, out)
			}
			installed := filepath.Join(want, "rootfiles")
			data, err := os.ReadFile(installed)
			if err != nil {
				t.Fatalf("binary not installed at %s: %v\n%s", installed, err, out)
			}
			if !strings.Contains(string(data), "rootfiles test") {
				t.Fatalf("unexpected binary body: %q", data)
			}
			info, err := os.Stat(installed)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o755 {
				t.Fatalf("mode=%v, want 0755", info.Mode().Perm())
			}
			if !strings.Contains(out, "Installed: "+installed) {
				t.Fatalf("install line missing:\n%s", out)
			}
			if strings.Contains(out, "Verified privileged command path") {
				t.Fatalf("non-root install verified a sudo path:\n%s", out)
			}
			if _, err := os.Lstat(filepath.Join(want, "root")); err == nil {
				t.Fatal("non-root install created a root alias")
			}
			if !tc.prefix && !strings.Contains(out, "is not on PATH") {
				t.Fatalf("missing PATH warning:\n%s", out)
			}
		})
	}
}

// TestInstallScriptRefusesBadChecksum proves the tool fallback never turns a
// mismatch into a skipped check.
func TestInstallScriptRefusesBadChecksum(t *testing.T) {
	f := newInstallFixture(t, "Darwin", "arm64", "1000")
	bad := strings.Repeat("0", 64) + "  rootfiles_0.0.1_darwin_arm64.tar.gz\n"
	if err := os.WriteFile(f.checksumsPath, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := f.run(t, "--version", "v0.0.1")
	if err == nil {
		t.Fatalf("install with a bad checksum succeeded:\n%s", out)
	}
	if !strings.Contains(out, "checksum verification failed") {
		t.Fatalf("missing checksum error:\n%s", out)
	}
	if _, statErr := os.Stat(filepath.Join(f.home, ".local", "bin", "rootfiles")); statErr == nil {
		t.Fatal("bad checksum still installed a binary")
	}
}

// TestInstallScriptDefaultDestinations covers the destinations this test must
// not write to. The download fails on purpose, so the run stops at the
// reported destination and nothing is created on the host.
func TestInstallScriptDefaultDestinations(t *testing.T) {
	for _, tc := range []struct {
		name, osName, uid string
	}{
		{"linux stays /usr/local/bin", "Linux", "1000"},
		{"darwin root stays /usr/local/bin", "Darwin", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t, tc.osName, "arm64", tc.uid)
			f.failDownload(t)
			out, err := f.run(t, "--version", "v0.0.1")
			if err == nil {
				t.Fatalf("expected the stubbed download to fail:\n%s", out)
			}
			if !strings.Contains(out, "Dest: /usr/local/bin\n") {
				t.Fatalf("destination not selected:\n%s", out)
			}
		})
	}
}
