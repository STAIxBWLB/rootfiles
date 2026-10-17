package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

// ExitError carries a process exit code. An empty Msg exits silently
// (e.g. `check` reporting drift with code 2).
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }
func (e *ExitError) ExitCode() int { return e.Code }

// Exit codes shared by read-only commands, suitable for monitoring:
// 0 = everything satisfied, 2 = pending changes / problems found.
const exitDrift = 2

const annotPrometheus = "rootfiles/prometheus"

func addOutputFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("output", "o", "text", "Output format: text or json")
}

// addOutputFlagWithMetrics also allows Prometheus text exposition, for the
// node_exporter textfile collector.
func addOutputFlagWithMetrics(cmd *cobra.Command) {
	cmd.Flags().StringP("output", "o", "text", "Output format: text, json or prometheus")
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[annotPrometheus] = "true"
}

func outputFormat(cmd *cobra.Command) (string, error) {
	f, _ := cmd.Flags().GetString("output")
	switch {
	case f == "" || f == "text":
		return "text", nil
	case f == "json":
		return "json", nil
	case f == "prometheus" && cmd.Annotations[annotPrometheus] == "true":
		return "prometheus", nil
	default:
		return "", fmt.Errorf("unknown --output %q", f)
	}
}

func boolMetric(b bool) int {
	if b {
		return 1
	}
	return 0
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// resolveTarget picks the profile/config to evaluate: explicit flags or
// env first, then the resolved copy the last recorded apply kept, reused
// only when it is that run's config, root alone controls it and this
// process can read it (the same checks as resolveRunTarget), then the
// recorded config path, the recorded profile and the profile suggested by
// system detection. When the kept copy is selected, applied carries the
// recorded run it was validated against (nil otherwise); reports name that
// run's target (reportedTarget) so the copy path never reaches them and a
// later apply cannot relabel the copy with another run's metadata.
func resolveTarget(cmd *cobra.Command, sys *config.SystemInfo) (profile, configPath string, applied *state.Run) {
	profile, _ = cmd.Flags().GetString("profile")
	configPath, _ = cmd.Flags().GetString("config")
	if profile == "" {
		profile = os.Getenv("ROOTFILES_PROFILE")
	}
	if profile != "" || configPath != "" {
		return profile, configPath, nil
	}
	if last, err := state.Last(); err == nil && last != nil {
		snap := state.AppliedConfigPath()
		err = reusableConfig(snap)
		if err == nil {
			err = sameRun(snap, last.ConfigSHA256)
		}
		if err == nil {
			return "", snap, last
		}
		// Fall back silently: a non-root operator running fleet status is
		// expected to fail the permission check on the root-only copy.
		// Only an absolute recorded path names the applied file; a relative
		// one would resolve against the current directory.
		if filepath.IsAbs(last.ConfigPath) {
			if _, err := os.Stat(last.ConfigPath); err == nil {
				return "", last.ConfigPath, nil
			}
		}
		if last.Profile != "" {
			return last.Profile, "", nil
		}
	}
	return sys.SuggestProfile(), "", nil
}

// reportedTarget is what reports name as the evaluated target: with the
// kept applied copy in use, the profile and path of the recorded run the
// copy was validated against, so the copy path never leaks into reports
// and provenance matches that run, even if a later apply has since
// recorded another.
func reportedTarget(profile, configPath string, applied *state.Run) (string, string) {
	if applied == nil {
		return profile, configPath
	}
	return applied.Profile, applied.ConfigPath
}

// Where the ownership walk of a reused config starts and the uid that must
// own it; tests stub both.
var (
	configTrustRoot        = "/"
	configOwnerUID  uint32 = 0
)

// resolveRunTarget picks the config for the user, gpu, tunnel, schedule and
// backup subcommands: flags or env first; then the resolved copy the last
// recorded apply kept (from a profile or a config file, with whatever that
// apply added), reused only when it is that run's config, root alone
// controls it and this process can read it; otherwise minimal with
// home-base detection. A config they were not given decides what root does
// (sudoers, groups, tunnel settings), so they never re-read the file or
// re-resolve the profile the apply named, and never take the profile
// detection suggests, which on DGX pins /raid/home and a private network.
func resolveRunTarget(cmd *cobra.Command, logger *slog.Logger) (profile, configPath string) {
	profile, _ = cmd.Flags().GetString("profile")
	configPath, _ = cmd.Flags().GetString("config")
	if profile == "" {
		profile = os.Getenv("ROOTFILES_PROFILE")
	}
	if profile != "" || configPath != "" {
		return profile, configPath
	}
	if last, _ := state.Last(); last != nil {
		snap := state.AppliedConfigPath()
		err := reusableConfig(snap)
		if err == nil {
			err = sameRun(snap, last.ConfigSHA256)
		}
		if err == nil {
			return "", snap
		}
		// A non-root user cannot read the root-only copy; that is expected for
		// read-only commands, but a dry run of a mutating one would preview
		// another config than the real run, so it is warned about.
		log := logger.Warn
		if errors.Is(err, fs.ErrPermission) && cmd.Annotations[annotMutates] != "true" {
			log = logger.Debug
		}
		log("not reusing the last applied config; pass --config or --profile to choose, or run as root", "config", snap, "err", err)
	}
	// As before the copy existed: the home base is detected from what apply
	// wrote (HOME= in /etc/default/useradd).
	return "minimal", ""
}

// sameRun reports whether the kept copy is the config of the recorded run:
// its bytes hash to the run's fingerprint, since both drop extends and the
// inline token before encoding. A copy left by another run, for example by
// an older version that applied a different config, is not reused.
func sameRun(path, fingerprint string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != fingerprint {
		return fmt.Errorf("%s is not the config of the last recorded run", path)
	}
	return nil
}

// reusableConfig reports why path may not be reused: a directory from the
// root down to it, or the file itself, is not root-owned, is writable by
// group or others, or is a symlink, or this process cannot read the file.
func reusableConfig(path string) error {
	if err := config.RootOnlyBase(configTrustRoot, filepath.Dir(path), configOwnerUID); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	case !ok || st.Uid != configOwnerUID:
		return fmt.Errorf("%s is not owned by root", path)
	case fi.Mode().Perm()&0o022 != 0:
		return fmt.Errorf("%s is writable by group or others", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return f.Close()
}
