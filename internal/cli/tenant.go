package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/STAIxBWLB/rootfiles/internal/config"
	"github.com/STAIxBWLB/rootfiles/internal/module"
	"github.com/STAIxBWLB/rootfiles/internal/tenant"
)

func newTenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Manage long-lived tenant containers (volume quota, accounts, GPUs)",
		Long: `Each tenant gets one Docker container with SSH on its own host port, a
size-capped volume (/data, /home, /tmp) and accounts whose passwords and
host keys survive image changes. Specs live in <home_base>/.rootfiles/tenants.`,
	}
	cmd.AddCommand(
		newTenantListCmd(), newTenantStatusCmd(), newTenantCreateCmd(), newTenantApplyCmd(),
		newTenantUserCmd(), newTenantPasswdCmd("passwd"), newTenantPasswdCmd("reset"),
		newTenantGPUCmd(), newTenantImageCmd(), newTenantPortCmd(), newTenantVolumeCmd(),
		newTenantRestartCmd(), newTenantLogsCmd(), newTenantImportCmd(), newTenantOpsCmd(),
		newTenantSyncCmd(),
	)
	return cmd
}

// tenantEnv loads the run context and a manager after the tenant checks.
func tenantEnv(cmd *cobra.Command) (*module.RunContext, *tenant.Manager, error) {
	rc, err := buildRunContext(cmd)
	if err != nil {
		return nil, nil, err
	}
	if config.IsRocky(rc.Config.System) {
		return nil, nil, fmt.Errorf("tenant containers are not supported on Rocky Linux")
	}
	if err := module.RequireTrustedMetadata(rc); err != nil {
		return nil, nil, err
	}
	root := rc.Config.Modules.Tenant.DataRoot()
	if err := config.RootOnlyHomeBase("/", root, 0); err != nil {
		return nil, nil, fmt.Errorf("refusing tenant root %s: %w", root, err)
	}
	return rc, module.NewTenantManager(rc, cmd.OutOrStdout()), nil
}

// tenantDo runs fn for tenant t and records a real run in the audit log.
func tenantDo(cmd *cobra.Command, t, action string, fn func(ctx context.Context, rc *module.RunContext, m *tenant.Manager) error) error {
	rc, m, err := tenantEnv(cmd)
	if err != nil {
		return err
	}
	err = fn(cmd.Context(), rc, m)
	if !rc.DryRun {
		recordTenant(t, action, err)
	}
	return err
}

func recordTenant(t, action string, err error) {
	actor, _ := tenant.Actor()
	result := "ok"
	if err != nil {
		result = "error: " + strings.SplitN(err.Error(), "\n", 2)[0]
	}
	if aerr := tenant.Audit(actor, t, action, result); aerr != nil {
		fmt.Fprintf(os.Stderr, "⚠ tenant audit log: %v\n", aerr)
	}
}

// editTenant loads t, applies edit, saves and converges it.
func editTenant(cmd *cobra.Command, t, action string, edit func(*tenant.Spec) error) error {
	return tenantDo(cmd, t, action, func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
		s, err := m.Load(t)
		if err != nil {
			return err
		}
		if err := edit(s); err != nil {
			return err
		}
		if err := m.SaveSpec(s); err != nil {
			return err
		}
		return m.Apply(ctx, s)
	})
}

func issuePassword(ctx context.Context, cmd *cobra.Command, m *tenant.Manager, s *tenant.Spec, user string) error {
	pw, err := m.Passwd(ctx, s, user)
	if err != nil {
		return err
	}
	if pw == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] would issue a temporary password for %s@%s\n", user, s.Name)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Temporary password for %s@%s: %s (must be changed at first login; not stored or logged)\n", user, s.Name, pw)
	return nil
}

func newTenantListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tenant names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, m, err := tenantEnv(cmd)
			if err != nil {
				return err
			}
			names, err := tenant.ListSpecs(m.SpecDir)
			for _, n := range names {
				fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			return err
		},
	}
}

func newTenantStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [TENANT]",
		Short: "Show tenant containers, spec drift and volume use",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, m, err := tenantEnv(cmd)
			if err != nil {
				return err
			}
			names := args
			if len(names) == 0 {
				if names, err = tenant.ListSpecs(m.SpecDir); err != nil {
					return err
				}
			}
			return m.Status(cmd.Context(), names, len(args) == 1)
		},
	}
}

func newTenantCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create TENANT",
		Short: "Create a tenant: volume, container and temporary passwords",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := args[0]
			s := &tenant.Spec{Name: t}
			s.Image, _ = cmd.Flags().GetString("image")
			s.SSHPort, _ = cmd.Flags().GetInt("ssh-port")
			s.Volume.Size, _ = cmd.Flags().GetString("size")
			s.Volume.FS, _ = cmd.Flags().GetString("fs")
			s.Ports, _ = cmd.Flags().GetStringArray("port")
			s.Operators, _ = cmd.Flags().GetStringArray("operator")
			s.ExtraBinds, _ = cmd.Flags().GetStringArray("bind")
			if g, _ := cmd.Flags().GetString("gpus"); g != "" {
				gpus, err := parseGPUList(g)
				if err != nil {
					return err
				}
				s.GPUs = gpus
			}
			sudo, _ := cmd.Flags().GetBool("sudo")
			accounts, _ := cmd.Flags().GetStringArray("account")
			for i, a := range accounts {
				u, err := parseAccount(a, 1000+i, sudo)
				if err != nil {
					return err
				}
				s.Users = append(s.Users, u)
			}
			if err := s.Validate(); err != nil {
				return err
			}
			return tenantDo(cmd, t, "create", func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
				if _, err := os.Stat(m.SpecDir + "/" + t + ".yaml"); err == nil {
					return fmt.Errorf("tenant %s already exists; edit it and run `rootfiles tenant apply %s`", t, t)
				}
				if err := m.SaveSpec(s); err != nil {
					return err
				}
				if err := m.Apply(ctx, s); err != nil {
					return err
				}
				for _, u := range s.Users {
					if err := issuePassword(ctx, cmd, m, s, u.Name); err != nil {
						return err
					}
				}
				for _, r := range m.Routes(s) {
					fmt.Fprintln(cmd.OutOrStdout(), "Through sshpiper:", r)
				}
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.String("image", "", "Container image (needs openssh-server)")
	f.Int("ssh-port", 0, "Host port published to the container's sshd")
	f.String("size", "", "Volume size, e.g. 500G or 2T")
	f.String("fs", "ext4", "Volume filesystem: ext4 (can shrink offline) or xfs")
	f.String("gpus", "", "Comma-separated GPU indices")
	f.StringArray("account", nil, "Account NAME[:UID[:GID]] (repeatable; uid defaults to 1000, 1001, ...)")
	f.Bool("sudo", false, "Give the accounts sudo inside the container")
	f.StringArray("port", nil, "Extra published port HOST:CONTAINER[/udp] (repeatable)")
	f.StringArray("operator", nil, "Host account allowed `sudo rootfiles tenant ops` on this tenant (repeatable)")
	f.StringArray("bind", nil, "Extra bind SRC:DST[:ro|rw] outside the quota (repeatable)")
	return cmd
}

// parseAccount reads NAME[:UID[:GID]].
func parseAccount(s string, defUID int, sudo bool) (tenant.User, error) {
	parts := strings.Split(s, ":")
	u := tenant.User{Name: parts[0], UID: defUID, Sudo: sudo}
	var err error
	if len(parts) > 3 {
		return u, fmt.Errorf("account %q: want NAME[:UID[:GID]]", s)
	}
	if len(parts) > 1 {
		if u.UID, err = strconv.Atoi(parts[1]); err != nil {
			return u, fmt.Errorf("account %q: bad uid", s)
		}
	}
	u.GID = u.UID
	if len(parts) > 2 {
		if u.GID, err = strconv.Atoi(parts[2]); err != nil {
			return u, fmt.Errorf("account %q: bad gid", s)
		}
	}
	return u, nil
}

func newTenantApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply [TENANT]",
		Short: "Converge a tenant to its spec (recreates the container on image, GPU or port changes)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			if all == (len(args) == 1) {
				return fmt.Errorf("give one TENANT or --all")
			}
			rc, m, err := tenantEnv(cmd)
			if err != nil {
				return err
			}
			names := args
			if all {
				if names, err = tenant.ListSpecs(m.SpecDir); err != nil {
					return err
				}
			}
			var failed []string
			for _, t := range names {
				s, err := m.Load(t)
				if err == nil {
					err = m.Apply(cmd.Context(), s)
				}
				if !rc.DryRun {
					recordTenant(t, "apply", err)
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "✗ %s: %v\n", t, err)
					failed = append(failed, t)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("apply failed for: %s", strings.Join(failed, ", "))
			}
			return nil
		},
	}
	cmd.Flags().Bool("all", false, "Apply every tenant")
	return cmd
}

func newTenantUserCmd() *cobra.Command {
	userCmd := &cobra.Command{Use: "user", Short: "Manage tenant accounts"}
	add := &cobra.Command{
		Use:   "add TENANT NAME",
		Short: "Add an account and issue its temporary password",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, name := args[0], args[1]
			uid, _ := cmd.Flags().GetInt("uid")
			gid, _ := cmd.Flags().GetInt("gid")
			sudo, _ := cmd.Flags().GetBool("sudo")
			return tenantDo(cmd, t, "user add "+name, func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
				s, err := m.Load(t)
				if err != nil {
					return err
				}
				if _, ok := s.User(name); ok {
					return fmt.Errorf("tenant %s already has user %s", t, name)
				}
				if uid == 0 {
					uid = 1000
					for _, u := range s.Users {
						uid = max(uid, u.UID+1)
					}
				}
				if gid == 0 {
					gid = uid
				}
				s.Users = append(s.Users, tenant.User{Name: name, UID: uid, GID: gid, Sudo: sudo})
				if err := m.SaveSpec(s); err != nil {
					return err
				}
				if err := m.Apply(ctx, s); err != nil {
					return err
				}
				return issuePassword(ctx, cmd, m, s, name)
			})
		},
	}
	add.Flags().Int("uid", 0, "UID (default: next free from 1000)")
	add.Flags().Int("gid", 0, "GID (default: the uid)")
	add.Flags().Bool("sudo", false, "Give the account sudo inside the container")
	userCmd.AddCommand(add)
	return userCmd
}

func newTenantPasswdCmd(use string) *cobra.Command {
	return &cobra.Command{
		Use:   use + " TENANT USER",
		Short: "Issue a temporary password that must be changed at first login",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, user := args[0], args[1]
			return tenantDo(cmd, t, use+" "+user, func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
				s, err := m.Load(t)
				if err != nil {
					return err
				}
				return issuePassword(ctx, cmd, m, s, user)
			})
		},
	}
}

func newTenantGPUCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gpu TENANT --gpus 0,1|none",
		Short: "Change a tenant's GPUs (recreates the container)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, _ := cmd.Flags().GetString("gpus")
			var gpus []int
			if g != "none" {
				var err error
				if gpus, err = parseGPUList(g); err != nil {
					return err
				}
			}
			return editTenant(cmd, args[0], "gpu "+g, func(s *tenant.Spec) error {
				s.GPUs = gpus
				return nil
			})
		},
	}
	cmd.Flags().String("gpus", "", "Comma-separated GPU indices, or none")
	_ = cmd.MarkFlagRequired("gpus")
	return cmd
}

func newTenantImageCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "image TENANT IMAGE",
		Short: "Change a tenant's image (recreates the container)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return editTenant(cmd, args[0], "image "+args[1], func(s *tenant.Spec) error {
				s.Image = args[1]
				return nil
			})
		},
	}
}

func newTenantPortCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "port TENANT SSH_PORT",
		Short: "Change a tenant's SSH host port (recreates the container)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("invalid port %q", args[1])
			}
			return editTenant(cmd, args[0], "port "+args[1], func(s *tenant.Spec) error {
				s.SSHPort = port
				return nil
			})
		},
	}
}

func newTenantVolumeCmd() *cobra.Command {
	vol := &cobra.Command{Use: "volume", Short: "Manage tenant volumes"}
	resize := &cobra.Command{
		Use:   "resize TENANT SIZE",
		Short: "Grow a volume online, or shrink an ext4 volume with --offline",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			offline, _ := cmd.Flags().GetBool("offline")
			return tenantDo(cmd, args[0], "volume resize "+args[1], func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
				s, err := m.Load(args[0])
				if err != nil {
					return err
				}
				return m.Resize(ctx, s, args[1], offline)
			})
		},
	}
	resize.Flags().Bool("offline", false, "Stop the container to shrink an ext4 volume")
	vol.AddCommand(resize)
	return vol
}

func newTenantRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart TENANT",
		Short: "Restart a tenant container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return tenantDo(cmd, args[0], "restart", func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
				s, err := m.Load(args[0])
				if err != nil {
					return err
				}
				return m.Restart(ctx, s)
			})
		},
	}
}

func newTenantLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs TENANT",
		Short: "Show the container log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, m, err := tenantEnv(cmd)
			if err != nil {
				return err
			}
			s, err := m.Load(args[0])
			if err != nil {
				return err
			}
			tail, _ := cmd.Flags().GetInt("tail")
			return m.Logs(cmd.Context(), s, tail)
		},
	}
	cmd.Flags().Int("tail", 200, "Lines to show")
	return cmd
}

func newTenantImportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import TENANT --from CONTAINER --size SIZE",
		Short: "Move a hand-made container under rootfiles (the old one is kept stopped)",
		Long: `Writes the spec from the running container, copies its host keys, password
hashes, --data and --keep paths into a new volume, stops the old container and
starts the tenant container. Ask the tenant to stop writing first. Refuses
before any change when the data does not fit the volume or the free space;
--dry-run prints the plan.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := tenant.ImportOptions{Tenant: args[0]}
			o.From, _ = cmd.Flags().GetString("from")
			o.Size, _ = cmd.Flags().GetString("size")
			o.FS, _ = cmd.Flags().GetString("fs")
			o.Data, _ = cmd.Flags().GetString("data")
			o.Keep, _ = cmd.Flags().GetStringArray("keep")
			o.Operators, _ = cmd.Flags().GetStringArray("operator")
			if o.From == "" || o.Size == "" {
				return fmt.Errorf("--from and --size are required")
			}
			return tenantDo(cmd, o.Tenant, "import "+o.From, func(ctx context.Context, _ *module.RunContext, m *tenant.Manager) error {
				_, err := m.Import(ctx, o)
				return err
			})
		},
	}
	f := cmd.Flags()
	f.String("from", "", "Running legacy container")
	f.String("size", "", "New volume size")
	f.String("fs", "ext4", "Volume filesystem: ext4 or xfs")
	f.String("data", "", "Host directory copied into the new /data")
	f.StringArray("keep", nil, "Path under /home or /tmp in the old container to copy (repeatable)")
	f.StringArray("operator", nil, "Host account allowed `sudo rootfiles tenant ops` (repeatable)")
	return cmd
}

func newTenantOpsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ops TENANT ACTION [USER]",
		Short: "Operator actions through sudo: " + strings.Join(tenant.OpsActions, "|"),
		Long: `Members of the tenant operator group run this with sudo. It acts only for
tenants that list the caller in operators, takes no flags, and reads only the
host's applied configuration. Every attempt is audited.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, action := args[0], args[1]
			actor, viaSudo := tenant.Actor()
			refuse := func(err error) error {
				if aerr := tenant.Audit(actor, t, "ops "+action, "refused: "+err.Error()); aerr != nil {
					fmt.Fprintf(os.Stderr, "⚠ tenant audit log: %v\n", aerr)
				}
				return err
			}
			// Flags or environment could point the config at files the
			// caller controls; operators get the host's applied config only.
			var set []string
			cmd.Flags().Visit(func(f *pflag.Flag) { set = append(set, "--"+f.Name) })
			if len(set) > 0 {
				return refuse(fmt.Errorf("tenant ops takes no flags (got %s)", strings.Join(set, " ")))
			}
			for _, kv := range os.Environ() {
				if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "ROOTFILES_") {
					os.Unsetenv(k)
				}
			}
			_, m, err := tenantEnv(cmd)
			if err != nil {
				return refuse(err)
			}
			s, err := m.Load(t)
			if err == nil {
				err = tenant.AuthorizeOps(s, actor, viaSudo, action)
			}
			if err == nil && (action == "reset") != (len(args) == 3) {
				err = fmt.Errorf("reset takes a USER; other actions take none")
			}
			if err != nil {
				return refuse(err)
			}
			ctx := cmd.Context()
			switch action {
			case "status":
				err = m.Status(ctx, []string{t}, true)
			case "logs":
				err = m.Logs(ctx, s, 200)
			case "gpu-heal":
				err = m.HealGPU(ctx, s)
			case "restart":
				err = m.Restart(ctx, s)
			case "reset":
				err = issuePassword(ctx, cmd, m, s, args[2])
				action += " " + args[2]
			}
			recordTenant(t, "ops "+action, err)
			return err
		},
	}
}

func newTenantSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Save password hashes and refresh sshpiper keys of every tenant (2-minute timer)",
		Long: `Runs without the global lock, so the timer never fails because an apply is
running: it only rewrites each tenant's derived files. It still needs root.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if geteuid() != 0 {
				return fmt.Errorf("'%s' must run as root", cmd.CommandPath())
			}
			_, m, err := tenantEnv(cmd)
			if err != nil {
				return err
			}
			names, err := tenant.ListSpecs(m.SpecDir)
			if err != nil {
				return err
			}
			var failed []string
			for _, t := range names {
				s, err := m.Load(t)
				if err == nil {
					err = m.Sync(cmd.Context(), s)
				}
				if err != nil {
					fmt.Fprintf(os.Stderr, "✗ %s: %v\n", t, err)
					failed = append(failed, t)
				}
			}
			if len(failed) > 0 {
				return fmt.Errorf("password sync failed for: %s", strings.Join(failed, ", "))
			}
			return nil
		},
	}
}
