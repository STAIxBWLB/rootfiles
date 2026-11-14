package state

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRecordAndLast(t *testing.T) {
	t.Setenv("ROOTFILES_STATE_DIR", t.TempDir())
	if r, err := Last(); r != nil || err != nil {
		t.Fatalf("empty state: %v, %v", r, err)
	}
	for _, p := range []string{"minimal", "dgx"} {
		if err := Record(Run{Profile: p, Success: true, FinishedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Last()
	if err != nil || r.Profile != "dgx" {
		t.Fatalf("Last = %+v, %v", r, err)
	}
	hist, _ := os.ReadFile(filepath.Join(Dir(), "history.jsonl"))
	if n := strings.Count(string(hist), "\n"); n != 2 {
		t.Errorf("history has %d lines, want 2", n)
	}
}

func TestAcquireIsExclusive(t *testing.T) {
	t.Setenv("ROOTFILES_LOCK_FILE", filepath.Join(t.TempDir(), "rootfiles.lock"))
	l1, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(); err == nil || !strings.Contains(err.Error(), "another rootfiles") {
		t.Errorf("second Acquire should fail while held, got %v", err)
	}
	l1.Release()
	l2, err := Acquire()
	if err != nil {
		t.Fatalf("Acquire after Release: %v", err)
	}
	l2.Release()
	var nilLock *Lock
	nilLock.Release() // must not panic
}

func TestAcquireReturnsTypedHolder(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "rootfiles.lock")
	t.Setenv("ROOTFILES_LOCK_FILE", lockPath)
	lock, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lock.Release)
	_, err = Acquire()
	var held *LockHeldError
	if !errors.As(err, &held) || held.PID != os.Getpid() || held.Path != lockPath || !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("typed holder=%+v error=%v", held, err)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) || !strings.Contains(err.Error(), lockPath) {
		t.Fatalf("holder diagnostic missing fields: %v", err)
	}
	for _, invalid := range []string{"", "-1", "0", "42; unwanted command", "999999999999999999999999999999999"} {
		if err := os.WriteFile(lockPath, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Acquire()
		var held *LockHeldError
		if !errors.As(err, &held) || held.PID != 0 || !strings.Contains(err.Error(), "pid unknown") {
			t.Fatalf("invalid PID %q accepted: error=%v", invalid, err)
		}
	}
}
