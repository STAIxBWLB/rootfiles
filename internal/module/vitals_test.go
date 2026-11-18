package module

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestCollectHostVitals_ProcAndMounts(t *testing.T) {
	fakeProc := t.TempDir()
	savedProc := procRoot
	savedStatfs := statfsFn
	savedStat := statFn
	t.Cleanup(func() {
		procRoot = savedProc
		statfsFn = savedStatfs
		statFn = savedStat
	})
	procRoot = fakeProc

	if err := os.WriteFile(filepath.Join(fakeProc, "loadavg"), []byte("2.50 1.75 0.90 3/512 4242\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeProc, "uptime"), []byte("7200.00 14000.00\n"), 0644); err != nil {
		t.Fatal(err)
	}
	meminfo := "MemTotal:       65536000 kB\nMemFree:        10000000 kB\nMemAvailable:   32768000 kB\nSwapTotal:       8192000 kB\nSwapFree:        6144000 kB\n"
	if err := os.WriteFile(filepath.Join(fakeProc, "meminfo"), []byte(meminfo), 0644); err != nil {
		t.Fatal(err)
	}

	statFn = func(path string, st *syscall.Stat_t) error {
		if path == "/" {
			st.Dev = 1
		} else {
			st.Dev = 2
		}
		return nil
	}
	statfsFn = func(path string, st *syscall.Statfs_t) error {
		st.Bsize = 4096
		st.Blocks = 100 * (1 << 30) / 4096
		if path == "/" {
			st.Bfree = 60 * (1 << 30) / 4096
			st.Bavail = 60 * (1 << 30) / 4096
		} else {
			st.Bfree = 8 * (1 << 30) / 4096
			st.Bavail = 8 * (1 << 30) / 4096
		}
		return nil
	}

	rc := &RunContext{
		Config: &config.Config{
			Users: config.UsersConfig{HomeBase: "/raid/home"},
		},
	}
	v := CollectHostVitals(rc)
	if !v.LoadKnown || v.Load1 != 2.5 || v.Load5 != 1.75 || v.Load15 != 0.9 {
		t.Fatalf("unexpected load: %+v", v)
	}
	if v.Uptime != 2*time.Hour {
		t.Fatalf("uptime=%v, want 2h", v.Uptime)
	}
	if v.MemTotalMiB != 64000 || v.MemUsedMiB != 32000 || v.MemUsedPct != 50 {
		t.Fatalf("unexpected mem: %+v", v)
	}
	if v.SwapTotalMiB != 8000 || v.SwapUsedMiB != 2000 {
		t.Fatalf("unexpected swap: %+v", v)
	}
	if len(v.Mounts) != 2 {
		t.Fatalf("mounts=%d, want 2: %+v", len(v.Mounts), v.Mounts)
	}
	if v.Mounts[0].Path != "/" || v.Mounts[0].Level != LevelOK {
		t.Fatalf("root mount=%+v", v.Mounts[0])
	}
	if v.Mounts[1].Path != "/raid/home" || v.Mounts[1].Level != LevelFail {
		t.Fatalf("raid mount=%+v", v.Mounts[1])
	}
}
