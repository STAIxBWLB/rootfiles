package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runexec "github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
	"github.com/spf13/cobra"
)

func TestRawPassthroughKeepsControlBytesAndFollowingLines(t *testing.T) {
	c1 := append([]byte("line1\nbad "), 0x9d)
	c1 = append(c1, []byte(" byte\nline3\n")...)
	osc := append([]byte("line1\nbad "), 27, ']')
	osc = append(osc, []byte(" open\nline3\n")...)
	for _, payload := range [][]byte{c1, osc} {
		for _, args := range [][]string{{"tenant", "logs", "demo"}, {"tenant", "ops", "demo", "logs"}, {"config", "show"}, {"fleet", "targets"}} {
			t.Run(strings.Join(args, "-")+fmt.Sprint(payload[10]), func(t *testing.T) {
				t.Setenv("NO_COLOR", "1")
				t.Setenv("LC_ALL", "C")
				bin := t.TempDir()
				fixture := filepath.Join(bin, "payload")
				if err := os.WriteFile(fixture, payload, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("RAW_LOG_FIXTURE", fixture)
				if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n[ \"$1\" = logs ] || exit 9\nexec /bin/cat \"$RAW_LOG_FIXTURE\"\n"), 0755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin)
				root := NewRootCmd("test", "test")
				cmd, _, err := root.Find(args)
				if err != nil {
					t.Fatal(err)
				}
				// Keep native configuration, ownership checks and audit off the host.
				// Exercise the real command output boundary and real Manager.Logs.
				root.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
				cmd.RunE = func(c *cobra.Command, _ []string) error {
					if args[0] == "tenant" {
						runner := runexec.NewRunner(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
						runner.QueryGuard = func(executable string) error {
							if filepath.Dir(executable) != bin {
								return fmt.Errorf("ambient command %s", executable)
							}
							return nil
						}
						m := &tenant.Manager{Runner: runner, Out: c.OutOrStdout()}
						return m.Logs(c.Context(), &tenant.Spec{Name: "demo"}, 200)
					}
					_, err := c.OutOrStdout().Write(payload)
					return err
				}
				var out, stderr bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&stderr)
				root.SetArgs(args)
				if err := executeCommand(context.Background(), root); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out.Bytes(), payload) {
					t.Fatalf("raw payload changed/truncated:\ngot=%q\nwant=%q", out.Bytes(), payload)
				}
			})
		}
	}
}

func TestRawPassthroughOnlyForLogsActionAndRenderedTargets(t *testing.T) {
	root := NewRootCmd("test", "test")
	ops, _, _ := root.Find([]string{"tenant", "ops"})
	if rawPassthroughOutput(ops, []string{"demo", "status"}) {
		t.Fatal("human tenant status bypasses policy")
	}
	if !rawPassthroughOutput(ops, []string{"demo", "logs"}) {
		t.Fatal("ops logs is not raw")
	}
	targets, _, _ := root.Find([]string{"fleet", "targets"})
	if err := targets.Flags().Set("push", "hub"); err != nil {
		t.Fatal(err)
	}
	if rawPassthroughOutput(targets, nil) {
		t.Fatal("human targets push result bypasses policy")
	}
}
