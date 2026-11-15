package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/rootfiles/internal/exec"
	"github.com/STAIxBWLB/rootfiles/internal/fleet"
	"github.com/STAIxBWLB/rootfiles/internal/state"
)

// Shared runtime plumbing for mutating commands: root/lock preflight,
// audit logging and per-run file backups.

var nativeHostOS = runtime.GOOS

const annotMutates = "rootfiles/mutates"
const annotReadOnly = "rootfiles/readonly"

// mutatingCommands change the system; they need root and the global lock.
var mutatingCommands = [][]string{
	{"apply"}, {"backup"},
	{"update"},
	{"user", "add"}, {"user", "restore"}, {"user", "rehome"},
	{"user", "group-add"}, {"user", "group-del"}, {"user", "passwd"},
	{"user", "del"}, {"user", "lock"}, {"user", "unlock"}, {"user", "expire"},
	{"user", "key", "add"}, {"user", "key", "rm"},
	{"user", "quota", "set"}, {"user", "quota", "rm"},
	{"schedule", "enable"}, {"schedule", "disable"},
	{"shutdown", "schedule"}, {"shutdown", "notify"}, {"shutdown", "block"}, {"shutdown", "cancel"},
	{"gpu", "assign"}, {"gpu", "revoke"},
	{"tunnel", "setup"}, {"tunnel", "restart"},
	{"tunnel", "update"}, {"tunnel", "uninstall"},
	{"tenant", "create"}, {"tenant", "apply"}, {"tenant", "user", "add"},
	{"tenant", "cpuset"}, {"tenant", "passwd"}, {"tenant", "gpu"}, {"tenant", "image"},
	{"tenant", "port"}, {"tenant", "volume", "resize"}, {"tenant", "restart"},
	{"tenant", "import"}, {"tenant", "ops"},
}

func markMutating(root *cobra.Command) {
	for _, path := range mutatingCommands {
		c, _, err := root.Find(path)
		if err != nil || c == root {
			continue
		}
		if c.Annotations == nil {
			c.Annotations = map[string]string{}
		}
		c.Annotations[annotMutates] = "true"
	}
}

// heldLock is released when the process exits; kept for explicit release.
var heldLock *state.Lock

// geteuid is overridable in tests.
var geteuid = os.Geteuid

// preflight enforces root and the global lock for mutating commands.
// Dry runs, annotated read-only flags and a non-Linux self-update are exempt.
func preflight(cmd *cobra.Command, _ []string) error {
	if err := rejectUnusedNativeFlags(cmd); err != nil {
		return err
	}
	if cmd.Annotations[annotMutates] != "true" {
		return nil
	}
	if readOnlyFlagSet(cmd) {
		return nil
	}
	if selfUpdateWithoutRoot(cmd) {
		return nil
	}
	return requireRootAndLock(cmd)
}

// selfUpdateWithoutRoot reports the one native mutation a non-Linux host runs
// as an ordinary user: `rootfiles update` on a fleet controller. The binary it
// replaces belongs to that user, and there is no native host state for the
// global lock to protect; upgrade.go refuses a binary the user does not own.
func selfUpdateWithoutRoot(cmd *cobra.Command) bool {
	if nativeHostOS == "linux" {
		return false
	}
	return cmd.CommandPath() == cmd.Root().Name()+" update"
}

const annotReadsRootFlag = "rootfiles/reads-root-flag/"

// usesRootFlags declares inherited inputs beside the command's reader.
func usesRootFlags(cmd *cobra.Command, names ...string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	for _, name := range names {
		cmd.Annotations[annotReadsRootFlag+name] = "true"
	}
	return cmd
}

func usesConfigTarget(cmd *cobra.Command) *cobra.Command {
	return usesRootFlags(cmd, "profile", "config", "home-base")
}

// usesConfigInputs accompanies readers of both applyFlagOverrides and
// applyAccountFlags, keeping check and config inspection aligned with apply.
func usesConfigInputs(cmd *cobra.Command) *cobra.Command {
	return usesRootFlags(usesConfigTarget(cmd), "user", "ssh-pubkey", "tunnel-token", "vlan-address")
}

// rejectUnusedNativeFlags catches inherited configuration inputs without a
// reader declaration. Fleet and tenant ops have their own refusal/audit paths.
func rejectUnusedNativeFlags(cmd *cobra.Command) error {
	command := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	if strings.HasPrefix(command, "fleet ") || command == "tenant ops" {
		return nil
	}
	var refused []string
	cmd.Flags().Visit(func(flag *pflag.Flag) {
		switch flag.Name {
		case "profile", "config", "home-base", "user", "ssh-pubkey", "tunnel-token", "vlan-address":
			for reader := cmd; reader != nil; reader = reader.Parent() {
				if reader.Annotations[annotReadsRootFlag+flag.Name] == "true" {
					return
				}
			}
			refused = append(refused, "--"+flag.Name)
		}
	})
	if len(refused) > 0 {
		return withNext(fmt.Errorf("%s: does not use %s", cmd.CommandPath(), strings.Join(refused, ", ")),
			"remove "+strings.Join(refused, ", ")+" from this command")
	}
	return nil
}

// yesFlag is shared by native commands; fleet still requires explicit --yes.
func yesFlag(cmd *cobra.Command) bool {
	yes, _ := cmd.Flags().GetBool("yes")
	return yes || os.Getenv("ROOTFILES_YES") == "true"
}

// readOnlyFlagSet accepts only enabled boolean flags explicitly marked read-only.
func readOnlyFlagSet(cmd *cobra.Command) bool {
	readonly := false
	visit := func(flag *pflag.Flag) {
		if len(flag.Annotations[annotReadOnly]) > 0 && flag.Annotations[annotReadOnly][0] == "true" &&
			flag.Value.Type() == "bool" && flag.Value.String() == "true" {
			readonly = true
		}
	}
	cmd.Flags().VisitAll(visit)
	cmd.InheritedFlags().VisitAll(visit)
	return readonly
}

// redactArgs copies command arguments (without the executable), hiding named
// secret values and the first positional operand of tunnel setup.
func redactArgs(args []string) []string {
	out, _ := redactArgsWithSecrets(args)
	return out
}

// redactArgsWithSecrets also records masking, even when a secret is literally
// the mask text. Such invocations must never be replayed as a next step.
func redactArgsWithSecrets(args []string) ([]string, bool) {
	masked := false
	out := append([]string(nil), args...)
	flags := newRootFlags()
	positional := 0
	tunnelSetup := true
	flagsEnded := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !flagsEnded && arg == "--" {
			flagsEnded = true
			continue
		}
		if !flagsEnded && strings.HasPrefix(arg, "--") {
			name, _, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			if name == "tunnel-token" || name == "password" || name == "suffix" {
				if inline {
					out[i] = "--" + name + "=***"
					masked = true
				} else if i+1 < len(args) {
					i++
					out[i] = "***"
					masked = true
				}
			} else if flag := flags.Lookup(name); flag != nil && !inline && flag.NoOptDefVal == "" && i+1 < len(args) {
				i++
			}
			continue
		}
		if !flagsEnded && strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		switch positional {
		case 0:
			tunnelSetup = arg == "tunnel"
		case 1:
			tunnelSetup = tunnelSetup && (arg == "setup" || arg == "install")
		default:
			if tunnelSetup {
				out[i] = "***"
				masked = true
			}
		}
		positional++
	}
	return out, masked
}

// rootRetryCommand retains argument boundaries without echoing secret values.
func rootRetryCommand(cmd *cobra.Command) string {
	args := os.Args[1:]
	help := "sudo " + cmd.CommandPath() + " --help"
	if _, masked := redactArgsWithSecrets(args); masked {
		return help
	}
	// sudo may reset the environment and silently select another target.
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "ROOTFILES_") {
			return help
		}
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = fleet.ShellQuote(arg)
	}
	return strings.TrimSpace("sudo rootfiles " + strings.Join(quoted, " "))
}

// requireRootAndLock checks for root and takes the global lock.
func requireRootAndLock(cmd *cobra.Command) error {
	if geteuid() != 0 {
		next := "rootfiles fleet --help"
		if nativeHostOS == "linux" {
			next = rootRetryCommand(cmd)
		}
		return withNext(fmt.Errorf("%s: changes the system and must run as root", cmd.CommandPath()), next)
	}
	if nativeHostOS != "linux" {
		return withNext(fmt.Errorf("native host mutations: require Linux (current OS: %s)", nativeHostOS), "rootfiles fleet --help")
	}
	lock, err := state.Acquire()
	if err != nil {
		var held *state.LockHeldError
		if errors.As(err, &held) && held.PID > 0 {
			return withNext(err, fmt.Sprintf("ps -p %d", held.PID))
		}
		return err
	}
	heldLock = lock
	auditf(cmd, "start", "args", strings.Join(redactArgs(os.Args[1:]), " "))
	return nil
}

// auditLogger appends JSON records to the audit log; nil when unavailable
// (e.g. not root). Opened lazily once per process.
var auditLogger *slog.Logger

func openAudit() *slog.Logger {
	if auditLogger != nil {
		return auditLogger
	}
	f, err := os.OpenFile(state.LogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return nil
	}
	auditLogger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("pid", os.Getpid(), "version", buildVersion)
	return auditLogger
}

func auditf(cmd *cobra.Command, msg string, args ...any) {
	if l := openAudit(); l != nil {
		l.Info(msg, append([]any{"cmd", cmd.CommandPath()}, args...)...)
	}
}

// diagnosticVerbose reads only the root diagnostic flag. Check's local
// --verbose/-v controls report details and must not enable library INFO logs.
func diagnosticVerbose(cmd *cobra.Command) bool {
	verbose, _ := cmd.Root().PersistentFlags().GetBool("verbose")
	return verbose
}

// newLogger returns the operator-facing logger (stderr text) teed into the
// audit log for real (non-dry-run) mutating runs.
func newLogger(cmd *cobra.Command, dryRun bool, level slog.Level) *slog.Logger {
	verbose := diagnosticVerbose(cmd)
	preview, _ := cmd.Flags().GetBool("dry-run")
	if !verbose && !preview && level < slog.LevelWarn {
		level = slog.LevelWarn
	}
	console := slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: level})
	if dryRun || readOnlyFlagSet(cmd) || cmd.Annotations[annotMutates] != "true" {
		return slog.New(console)
	}
	audit := openAudit()
	if audit == nil {
		return slog.New(console)
	}
	return slog.New(teeHandler{console, audit.Handler().WithAttrs([]slog.Attr{slog.String("cmd", cmd.CommandPath())})})
}

// newRunner builds a Runner for cmd; real mutating runs get a backup
// session so overwritten files can be rolled back.
func newRunner(cmd *cobra.Command, dryRun bool) *exec.Runner {
	r := exec.NewRunner(dryRun, newLogger(cmd, dryRun, slog.LevelInfo))
	if !dryRun && !readOnlyFlagSet(cmd) && cmd.Annotations[annotMutates] == "true" && geteuid() == 0 {
		r.Backup = exec.NewBackup(state.BackupsDir(), strings.Join(append([]string{os.Args[0]}, redactArgs(os.Args[1:])...), " "))
	}
	return r
}

// teeHandler fans records out to several handlers.
type teeHandler []slog.Handler

func (t teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range t {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (t teeHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range t {
		if h.Enabled(ctx, r.Level) {
			_ = h.Handle(ctx, r.Clone())
		}
	}
	return nil
}

func (t teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (t teeHandler) WithGroup(name string) slog.Handler {
	out := make(teeHandler, len(t))
	for i, h := range t {
		out[i] = h.WithGroup(name)
	}
	return out
}
