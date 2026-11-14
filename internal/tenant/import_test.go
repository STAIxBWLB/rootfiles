package tenant

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
)

func importFixture(t *testing.T, merged string, entries []*tar.Header) (*Manager, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	tarBinary, _ := osexec.LookPath("tar")
	t.Setenv("IMPORT_TAR", tarBinary)
	archiveFile := filepath.Join(dir, "alice.tar")
	archive, err := os.Create(archiveFile)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(archive)
	for _, h := range entries {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := io.CopyN(tw, zeros{}, h.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	archive.Close()
	var parent bytes.Buffer
	pt := tar.NewWriter(&parent)
	pt.WriteHeader(&tar.Header{Name: "home/", Typeflag: tar.TypeDir, Mode: 0755})
	pt.Close()
	os.WriteFile(filepath.Join(dir, "home.tar"), parent.Bytes(), 0600)
	old := legacyContainer{}
	old.Config.Image = "ubuntu:test"
	old.State.Running = true
	old.HostConfig.PortBindings = map[string][]struct{ HostPort string }{"22/tcp": {{HostPort: "2201"}}}
	old.GraphDriver.Data = map[string]string{"MergedDir": merged}
	inspect, _ := json.Marshal([]legacyContainer{old})
	t.Setenv("IMPORT_INSPECT", string(inspect))
	t.Setenv("IMPORT_ARCHIVE", archiveFile)
	t.Setenv("IMPORT_PARENT", filepath.Join(dir, "home.tar"))
	t.Setenv("IMPORT_ALICE_PARENT", "")
	t.Setenv("IMPORT_BOB_ARCHIVE", "")
	logFile := filepath.Join(dir, "calls")
	t.Setenv("IMPORT_CALLS", logFile)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$IMPORT_CALLS"
case "$1 $2" in
"inspect -f") exit 1 ;;
"inspect legacy") printf '%s\n' "$IMPORT_INSPECT" ;;
"exec legacy")
 case "$*" in
 */etc/passwd) printf '%s\n' 'alice:x:1000:1000::/home/alice:/bin/bash' ;;
 */etc/group) printf '%s\n' 'sudo:x:27:alice' ;;
 */etc/shadow) printf '%s\n' 'alice:!:0:0:99999:7:::' ;;
 esac ;;
"cp -a") [ "$3" = -- ] || exit 1
 "$IMPORT_TAR" -xpf "$IMPORT_ARCHIVE" --no-same-owner -C "$5" ;;
"cp --") case "$3" in
 legacy:/home) /bin/cat "$IMPORT_PARENT" ;;
 legacy:/home/alice) /bin/cat "${IMPORT_ALICE_PARENT:-$IMPORT_ARCHIVE}" ;;
 legacy:/home/alice/projects) /bin/cat "$IMPORT_ARCHIVE" ;;
 legacy:/home/bob) /bin/cat "$IMPORT_BOB_ARCHIVE" ;;
 *) exit 1 ;;
 esac ;;
*) exit 1 ;;
esac
`
	for name, body := range map[string]string{"docker": script, "rsync": "#!/bin/sh\nprintf 'rsync %s\\n' \"$*\" >> \"$IMPORT_CALLS\"\n[ \"$IMPORT_COPY\" != 1 ] || /bin/cp -pR \"$2\" \"$3\"\n", "systemctl": "#!/bin/sh\nprintf 'enabled\\n'\n", "du": "#!/bin/sh\nprintf '4096 fixture\\n'\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	out := &bytes.Buffer{}
	m := &Manager{Runner: exec.NewRunner(true, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))), Root: t.TempDir(), SpecDir: t.TempDir(), UnitDir: t.TempDir(), Out: out}
	m.Runner.QueryGuard = func(executable string) error {
		if filepath.Dir(executable) != dir {
			t.Fatalf("ambient query %s", executable)
		}
		return nil
	}
	return m, logFile, out
}

func normalKeepArchive() []*tar.Header {
	return []*tar.Header{{Name: "alice/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}, {Name: "alice/data", Typeflag: tar.TypeReg, Mode: 0640, Uid: 1000, Gid: 1000, Size: 7}}
}

func TestImportContainerdDryRunPlansWithoutMutation(t *testing.T) {
	m, log, out := importFixture(t, "", normalKeepArchive())
	_, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M", Keep: []string{"/home/alice"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Import plan") || !strings.Contains(out.String(), "legacy:/home/alice") {
		t.Fatalf("plan=%s", out.String())
	}
	if strings.Contains(calls(t, log), "cp -a") {
		t.Fatal("dry run copied data")
	}
	for _, dir := range []string{m.Root, m.SpecDir, m.UnitDir} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("dry run wrote %s", dir)
		}
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestImportFallbackRefusesBeforeChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []*tar.Header
		want    string
	}{
		{"root-symlink", []*tar.Header{{Name: "alice", Typeflag: tar.TypeSymlink, Linkname: "/etc"}}, "symlink source"},
		{"traversal", []*tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}}, "escapes"},
		{"nested-escape", []*tar.Header{{Name: "alice", Typeflag: tar.TypeDir}, {Name: "alice/link", Typeflag: tar.TypeSymlink, Linkname: "../../../../host"}}, "escapes"},
		{"link-ancestor", []*tar.Header{{Name: "alice", Typeflag: tar.TypeDir}, {Name: "alice/link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, {Name: "alice/link/file", Typeflag: tar.TypeReg}}, "non-directory"},
		{"hardlink-escape", []*tar.Header{{Name: "alice", Typeflag: tar.TypeDir}, {Name: "alice/link", Typeflag: tar.TypeLink, Linkname: "../../host"}}, "hardlink"},
		{"oversize", []*tar.Header{{Name: "alice", Typeflag: tar.TypeDir}, {Name: "alice/big", Typeflag: tar.TypeReg, Size: 64 << 20}}, "headroom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, log, _ := importFixture(t, "", tc.entries)
			reserved := false
			m.ReserveGPUs = func(context.Context, string, []int) error { reserved = true; return nil }
			_, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "64M", Keep: []string{"/home/alice"}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want %s", err, tc.want)
			}
			if reserved || strings.Contains(calls(t, log), "cp -a") {
				t.Fatal("changed state before refusal")
			}
			for _, dir := range []string{m.Root, m.SpecDir, m.UnitDir} {
				entries, _ := os.ReadDir(dir)
				if len(entries) != 0 {
					t.Fatalf("refusal wrote %s", dir)
				}
			}
		})
	}
}

func TestImportOverlayPathUnchanged(t *testing.T) {
	merged := t.TempDir()
	if err := os.MkdirAll(filepath.Join(merged, "home", "alice"), 0755); err != nil {
		t.Fatal(err)
	}
	m, log, _ := importFixture(t, merged, nil)
	if _, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M", Keep: []string{"/home/alice"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(calls(t, log), "cp --") {
		t.Fatal("overlay path used archive fallback")
	}
}

func TestDockerKeepCopiesIntoPrivateStaging(t *testing.T) {
	m, log, _ := importFixture(t, "", normalKeepArchive())
	if os.Getenv("IMPORT_TAR") == "" {
		t.Skip("tar unavailable for fixture")
	}
	fixtureBin := os.Getenv("PATH")
	for name, binary := range map[string]string{"mktemp": "/usr/bin/mktemp", "rm": "/bin/rm", "chmod": "/bin/chmod"} {
		if _, err := os.Stat(binary); err != nil {
			t.Skipf("%s unavailable", binary)
		}
		if err := os.Symlink(binary, filepath.Join(fixtureBin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("IMPORT_COPY", "1")
	m.Runner.DryRun = false
	mount := t.TempDir()
	if err := m.copyDockerKeep(context.Background(), "legacy", "/home/alice", mount); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(mount, "home", "alice", "data"))
	if err != nil || len(data) != 7 {
		t.Fatalf("data=%q err=%v", data, err)
	}
	fi, err := os.Stat(filepath.Join(mount, "home", "alice"))
	if err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("directory metadata=%v err=%v", fi, err)
	}
	fi, err = os.Stat(filepath.Join(mount, "home", "alice", "data"))
	if err != nil || fi.Mode().Perm() != 0640 {
		t.Fatalf("file metadata=%v err=%v", fi, err)
	}
	if !strings.Contains(calls(t, log), "cp -a -- legacy:/home/alice") {
		t.Fatalf("numeric-owner option absent: %s", calls(t, log))
	}
	entries, _ := os.ReadDir(m.Root)
	if len(entries) != 0 {
		t.Fatal("private staging not removed")
	}
}

func TestDockerKeepRefusesDestinationSymlink(t *testing.T) {
	mount := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(mount, "home"))
	if err := safeImportDestination(mount, filepath.Join(mount, "home")); err == nil {
		t.Fatal("destination symlink accepted")
	}
}

func TestImportFallbackSourceParentSymlink(t *testing.T) {
	m, _, _ := importFixture(t, "", normalKeepArchive())
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	tw.WriteHeader(&tar.Header{Name: "home", Typeflag: tar.TypeSymlink, Linkname: "/"})
	tw.Close()
	if err := os.WriteFile(os.Getenv("IMPORT_PARENT"), archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.measureDockerKeep(context.Background(), "legacy", "/home/alice"); err == nil || !strings.Contains(err.Error(), "symlink source") {
		t.Fatalf("parent symlink err=%v", err)
	}
}

func TestImportFallbackRequiresFreeSpaceBeforeChanges(t *testing.T) {
	for _, free := range []uint64{1, 25000} {
		t.Run(strconv.FormatUint(free, 10), func(t *testing.T) {
			m, log, _ := importFixture(t, "", normalKeepArchive())
			old := importFreeSpace
			importFreeSpace = func(string) uint64 { return free }
			t.Cleanup(func() { importFreeSpace = old })
			reserved := false
			m.ReserveGPUs = func(context.Context, string, []int) error { reserved = true; return nil }
			_, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M", Keep: []string{"/home/alice"}})
			if err == nil || reserved || strings.Contains(calls(t, log), "cp -a") {
				t.Fatalf("free=%d err=%v reserved=%v", free, err, reserved)
			}
			entries, _ := os.ReadDir(m.SpecDir)
			if len(entries) != 0 {
				t.Fatal("spec written before free-space refusal")
			}
		})
	}
}

func TestImportOverlayEscapeStillRefused(t *testing.T) {
	merged := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(merged, "home")); err != nil {
		t.Fatal(err)
	}
	m, _, _ := importFixture(t, merged, nil)
	_, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M", Keep: []string{"/home/alice"}})
	if err == nil || !strings.Contains(err.Error(), "not a path inside") {
		t.Fatalf("overlay escape err=%v", err)
	}
}

func TestArchiveAllocationAccountsForSmallFiles(t *testing.T) {
	const files = 16384
	var needed uint64
	for i := 0; i < files; i++ {
		n, err := archiveAllocation(&tar.Header{Typeflag: tar.TypeReg, Size: 1})
		if err != nil {
			t.Fatal(err)
		}
		needed += n
	}
	if needed <= (64<<20)*95/100 {
		t.Fatalf("small files could pass 64M volume preflight: %d", needed)
	}
	if _, err := archiveAllocation(&tar.Header{Typeflag: tar.TypeReg, Size: -1}); err == nil {
		t.Fatal("negative size accepted")
	}
}

func TestDockerKeepMissingOrFailingToolIsReadOnly(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(strconv.FormatBool(missing), func(t *testing.T) {
			m, _, _ := importFixture(t, "", normalKeepArchive())
			binary := filepath.Join(os.Getenv("PATH"), "docker")
			if missing {
				if err := os.Remove(binary); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 9\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := m.measureDockerKeep(context.Background(), "legacy", "/home/alice"); err == nil {
				t.Fatal("missing/failing Docker accepted")
			}
			entries, _ := os.ReadDir(m.Root)
			if len(entries) != 0 {
				t.Fatal("query failure wrote staging")
			}
		})
	}
}

type importHookHandler struct{ hook func(slog.Record) }

func (h importHookHandler) Enabled(context.Context, slog.Level) bool      { return true }
func (h importHookHandler) Handle(_ context.Context, r slog.Record) error { h.hook(r); return nil }
func (h importHookHandler) WithAttrs([]slog.Attr) slog.Handler            { return h }
func (h importHookHandler) WithGroup(string) slog.Handler                 { return h }

func TestDockerKeepCancellationCleansOnlyPrivateStaging(t *testing.T) {
	m, _, _ := importFixture(t, "", normalKeepArchive())
	for name, binary := range map[string]string{"mktemp": "/usr/bin/mktemp", "rm": "/bin/rm", "chmod": "/bin/chmod"} {
		if _, err := os.Stat(binary); err != nil {
			t.Skipf("%s unavailable", binary)
		}
		if err := os.Symlink(binary, filepath.Join(os.Getenv("PATH"), name)); err != nil {
			t.Fatal(err)
		}
	}
	untouched := filepath.Join(m.Root, "existing-volume")
	if err := os.Mkdir(untouched, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reached := false
	m.Runner.Logger = slog.New(importHookHandler{hook: func(r slog.Record) {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "cmd" && strings.HasPrefix(a.Value.String(), "docker cp -a") {
				reached = true
				entries, err := os.ReadDir(m.Root)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".rootfiles-import-") {
						fi, err := entry.Info()
						if err != nil || fi.Mode().Perm() != 0700 {
							t.Fatalf("scratch mode=%v err=%v", fi, err)
						}
					}
				}
				cancel()
			}
			return true
		})
	}})
	m.Runner.DryRun = false
	if err := m.copyDockerKeep(ctx, "legacy", "/home/alice", t.TempDir()); err == nil || !reached {
		t.Fatalf("copy cancellation err=%v reached=%v", err, reached)
	}
	entries, err := os.ReadDir(m.Root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing-volume" {
		t.Fatalf("cleanup removed wrong path or leaked staging: %v err=%v", entries, err)
	}
}

func TestImportCleanupHasNoDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	cancel()
	cleanup := importCleanupContext(ctx)
	if _, bounded := cleanup.Deadline(); bounded {
		t.Fatal("cleanup has a deadline that can truncate a large-data removal")
	}
	if cleanup.Done() != nil || cleanup.Err() != nil {
		t.Fatal("cleanup inherits invocation cancellation")
	}
}

func TestDockerKeepWaitsForSlowCleanup(t *testing.T) {
	m, _, _ := importFixture(t, "", normalKeepArchive())
	for name, binary := range map[string]string{"mktemp": "/usr/bin/mktemp", "chmod": "/bin/chmod"} {
		if _, err := os.Stat(binary); err != nil {
			t.Skipf("%s unavailable", binary)
		}
		if err := os.Symlink(binary, filepath.Join(os.Getenv("PATH"), name)); err != nil {
			t.Fatal(err)
		}
	}
	completed := filepath.Join(t.TempDir(), "removed")
	t.Setenv("IMPORT_REMOVED", completed)
	// Deliberately wait after the invocation has been canceled. A short parent
	// deadline cannot cut off cleanup, and the function must wait for rm to finish.
	rm := []byte("#!/bin/sh\n/bin/sleep 0.05\n/bin/rm \"$@\" || exit $?\nprintf done > \"$IMPORT_REMOVED\"\n")
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "rm"), rm, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m.Runner.Logger = slog.New(importHookHandler{hook: func(r slog.Record) {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "cmd" && strings.HasPrefix(a.Value.String(), "docker cp -a") {
				cancel()
			}
			return true
		})
	}})
	m.Runner.DryRun = false
	if err := m.copyDockerKeep(ctx, "legacy", "/home/alice", t.TempDir()); err == nil {
		t.Fatal("canceled copy succeeded")
	}
	if _, err := os.Stat(completed); err != nil {
		t.Fatalf("returned before slow cleanup finished: %v", err)
	}
	entries, _ := os.ReadDir(m.Root)
	if len(entries) != 0 {
		t.Fatal("slow cleanup left a partial duplicate")
	}
}

func TestDockerKeepStagingMkdirErrorNamesStepAndRecovery(t *testing.T) {
	m, _, _ := importFixture(t, "", normalKeepArchive())
	m.Runner.DryRun = false
	stage := filepath.Join(m.Root, ".rootfiles-import-mkdir-failure")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "home"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMPORT_STAGE", stage)
	bin := os.Getenv("PATH")
	for name, script := range map[string]string{
		"mktemp": "#!/bin/sh\nprintf '%s\\n' \"$IMPORT_STAGE\"\n",
		"chmod":  "#!/bin/sh\nexit 0\n",
		"rm":     "#!/bin/sh\n/bin/rm \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	err := m.copyDockerKeep(context.Background(), "legacy", "/home/alice", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "creating import staging directory "+filepath.Join(stage, "home")) || !strings.Contains(err.Error(), "check free space and permissions under the tenant root") {
		t.Fatalf("unhelpful staging mkdir error: %v", err)
	}
}

func TestImportNestedKeepRestoresParentAttributes(t *testing.T) {
	m, _, _ := importFixture(t, "", []*tar.Header{{Name: "projects/", Typeflag: tar.TypeDir, Mode: 0750, Uid: 1000, Gid: 1000}})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "alice/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "alice-parent.tar")
	if err := os.WriteFile(parent, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMPORT_ALICE_PARENT", parent)
	var commands bytes.Buffer
	m.Runner.Logger = slog.New(slog.NewTextHandler(&commands, nil))
	if _, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M", Keep: []string{"/home/alice/projects"}}); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(m.Paths("imported").Mount, "home", "alice")
	for _, want := range []string{"chown -- 1000:1000 " + target, "chmod 0700 -- " + target} {
		if !strings.Contains(commands.String(), want) {
			t.Fatalf("missing parent metadata command %q:\n%s", want, commands.String())
		}
	}
	for _, dir := range []string{m.Root, m.SpecDir, m.UnitDir} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("dry-run metadata plan wrote %s", dir)
		}
	}
}

func TestImportSequentialKeepsNeedOnlyLargestScratchCopy(t *testing.T) {
	m, _, _ := importFixture(t, "", normalKeepArchive())
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "bob/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 1000, Gid: 1000}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	bob := filepath.Join(t.TempDir(), "bob.tar")
	if err := os.WriteFile(bob, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMPORT_BOB_ARCHIVE", bob)
	aliceSize, _, err := m.measureDockerKeep(context.Background(), "legacy", "/home/alice")
	if err != nil {
		t.Fatal(err)
	}
	bobSize, _, err := m.measureDockerKeep(context.Background(), "legacy", "/home/bob")
	if err != nil {
		t.Fatal(err)
	}
	if aliceSize == 0 || bobSize == 0 {
		t.Fatal("fixture must contain two nonempty keep estimates")
	}
	total := aliceSize + bobSize
	peak := max(aliceSize, bobSize)
	// This budget fits all final data plus one staging copy, but cannot fit
	// a second full copy of both keeps simultaneously.
	free := total + peak
	if free >= 2*total {
		t.Fatal("fixture does not distinguish summed from peak scratch")
	}
	old := importFreeSpace
	importFreeSpace = func(string) uint64 { return free }
	t.Cleanup(func() { importFreeSpace = old })
	if _, err := m.Import(context.Background(), ImportOptions{Tenant: "imported", From: "legacy", Size: "128M", Keep: []string{"/home/alice", "/home/bob"}}); err != nil {
		t.Fatalf("sequential copies should fit peak scratch: %v", err)
	}
}
