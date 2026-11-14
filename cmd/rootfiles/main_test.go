package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestProcessOutputContract(t *testing.T) {
	if os.Getenv("ROOTFILES_PROCESS_TEST") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"rootfiles"}, os.Args[i+1:]...)
				break
			}
		}
		main()
		return
	}
	for _, tc := range []struct {
		args  []string
		exit  int
		usage bool
	}{
		{[]string{"unknown-command"}, 2, true},
		{[]string{"status", "--unknown-flag"}, 2, true},
		{[]string{"gpu", "assign"}, 2, true},
		{[]string{"config", "show", "--config", "/nonexistent/rootfiles-test.yaml"}, 1, false},
		{[]string{"--help"}, 0, false},
		{[]string{"-v"}, 0, false},
		{[]string{"--version"}, 0, false},
	} {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestProcessOutputContract$", "--"}, tc.args...)...)
		cmd.Env = append(os.Environ(), "ROOTFILES_PROCESS_TEST=1", "NO_COLOR=1", "LANG=C")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.exit {
			t.Fatalf("%v: exit=%d stderr=%q", tc.args, code, stderr.String())
		}
		if tc.exit != 0 && (!strings.HasPrefix(stderr.String(), "rootfiles: ") || stdout.Len() != 0) {
			t.Fatalf("%v: stdout=%q stderr=%q", tc.args, stdout.String(), stderr.String())
		}
		if tc.usage && !strings.Contains(stderr.String(), "Usage: rootfiles") {
			t.Fatalf("missing usage: %q", stderr.String())
		}
		if len(tc.args) == 1 && (tc.args[0] == "-v" || tc.args[0] == "--version") {
			if !strings.HasPrefix(stdout.String(), "rootfiles version dev (none)\n") || strings.Contains(stdout.String(), "Usage:") || stderr.Len() != 0 {
				t.Fatalf("version shorthand changed: args=%v stdout=%q stderr=%q", tc.args, stdout.String(), stderr.String())
			}
		}
		if strings.ContainsRune(stdout.String()+stderr.String(), 27) {
			t.Fatal("NO_COLOR emitted ESC")
		}
	}
}
