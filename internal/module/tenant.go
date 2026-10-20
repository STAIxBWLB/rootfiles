package module

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/STAIxBWLB/rootfiles/internal/tenant"
)

// TenantModule prepares a host for `rootfiles tenant` containers: the GPU
// device guard, the daily password sync, fstrim, the operator sudoers rule
// and the volume mount units of existing tenants.
type TenantModule struct{}

func NewTenantModule() *TenantModule { return &TenantModule{} }
func (m *TenantModule) Name() string { return "tenant" }

// Overridable in tests.
var (
	tenantUnitDir   = "/etc/systemd/system"
	tenantGuardPath = tenant.GuardPath
)

const (
	tenantGuardUnit = "rootfiles-gpu-guard"
	tenantSyncUnit  = "rootfiles-tenant-sync"
)

var tenantTimers = []string{tenantGuardUnit + ".timer", tenantSyncUnit + ".timer", "fstrim.timer"}

// NewTenantManager returns a tenant manager wired to rc's runner, config and
// the shared GPU ledger.
func NewTenantManager(rc *RunContext, out io.Writer) *tenant.Manager {
	return &tenant.Manager{
		Runner:  rc.Runner,
		Root:    rc.Config.Modules.Tenant.DataRoot(),
		SpecDir: tenant.SpecDir(metaHomeBase(rc)),
		UnitDir: tenantUnitDir,
		Out:     out,
		ReserveGPUs: func(ctx context.Context, t string, gpus []int) error {
			return ReserveTenantGPUs(ctx, rc, t, gpus)
		},
	}
}

// RequireTrustedMetadata refuses rootfiles metadata, tenant specs included,
// that a user other than root could control.
func RequireTrustedMetadata(rc *RunContext) error { return requireTrustedMetadata(rc) }

type tenantFile struct {
	path, content string
	mode          os.FileMode
}

func tenantFiles() []tenantFile {
	unit := func(name string) string { return filepath.Join(tenantUnitDir, name) }
	return []tenantFile{
		{tenantGuardPath, tenantGuardScript, 0o755},
		{unit(tenantGuardUnit + ".service"), `# Managed by rootfiles
[Unit]
Description=rootfiles: restore GPU access of running containers after a systemd reload
After=docker.service

[Service]
Type=oneshot
ExecStart=` + tenantGuardPath + `
`, 0o644},
		{unit(tenantGuardUnit + ".timer"), `# Managed by rootfiles
[Unit]
Description=rootfiles GPU device guard

[Timer]
OnBootSec=2min
OnUnitActiveSec=1min

[Install]
WantedBy=timers.target
`, 0o644},
		{unit(tenantSyncUnit + ".service"), `# Managed by rootfiles
[Unit]
Description=rootfiles: save tenant password hashes
After=docker.service

[Service]
Type=oneshot
ExecStart=` + fleetSudoBinaryDefault + ` tenant sync
`, 0o644},
		{unit(tenantSyncUnit + ".timer"), `# Managed by rootfiles
[Unit]
Description=rootfiles daily tenant password sync

[Timer]
OnCalendar=daily
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
`, 0o644},
	}
}

func tenantSudoersPath() string { return filepath.Join(sudoersDir, "rootfiles-tenant-ops") }

func tenantSudoersContent(group string) []byte {
	return []byte("# Managed by rootfiles: operators run `sudo rootfiles tenant ops TENANT ACTION`\n" +
		"%" + group + " ALL=(root) NOPASSWD: " + fleetSudoBinaryDefault + " tenant ops *\n")
}

func (m *TenantModule) Check(ctx context.Context, rc *RunContext) (*CheckResult, error) {
	changes, err := tenantChanges(ctx, rc)
	if err != nil {
		return nil, err
	}
	return &CheckResult{Satisfied: len(changes) == 0, Changes: changes}, nil
}

func unitOn(ctx context.Context, rc *RunContext, unit string) bool {
	res, err := rc.Runner.Query(ctx, "systemctl", "is-enabled", unit)
	return err == nil && strings.TrimSpace(res.Stdout) == "enabled"
}

func tenantChanges(ctx context.Context, rc *RunContext) ([]Change, error) {
	var changes []Change
	add := func(desc, cmd string) { changes = append(changes, Change{Description: desc, Command: cmd}) }
	for _, f := range tenantFiles() {
		if cur, err := rc.Runner.ReadFile(f.path); err != nil || string(cur) != f.content {
			add("Write "+filepath.Base(f.path), "write "+f.path)
		}
	}
	for _, t := range tenantTimers {
		if !unitOn(ctx, rc, t) {
			add("Enable "+t, "systemctl enable --now "+t)
		}
	}
	group := rc.Config.Modules.Tenant.Group()
	if _, err := rc.Runner.Query(ctx, "getent", "group", group); err != nil {
		add("Create operator group "+group, "groupadd --system "+group)
	}
	if err := validateFleetSudoBinary(); err != nil {
		return nil, err
	}
	cur, err := rc.Runner.ReadFile(tenantSudoersPath())
	info, statErr := os.Lstat(tenantSudoersPath())
	if err != nil || string(cur) != string(tenantSudoersContent(group)) || statErr != nil || !fleetSudoDropInSafe(info) {
		add("Install tenant operator sudoers rule", "write "+tenantSudoersPath())
	}
	specs, err := loadTenantSpecs(rc)
	if err != nil {
		return nil, err
	}
	mgr := NewTenantManager(rc, io.Discard)
	for _, s := range specs {
		if mgr.UnitDrift(s) || !unitOn(ctx, rc, mgr.UnitFor(s.Name)) {
			add("Install volume mount unit of tenant "+s.Name, "write "+mgr.UnitFor(s.Name))
		}
	}
	return changes, nil
}

func loadTenantSpecs(rc *RunContext) ([]*tenant.Spec, error) {
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}
	dir := tenant.SpecDir(metaHomeBase(rc))
	names, err := tenant.ListSpecs(dir)
	if err != nil {
		return nil, err
	}
	var specs []*tenant.Spec
	for _, n := range names {
		s, err := tenant.LoadSpec(dir, n)
		if err != nil {
			return nil, err
		}
		specs = append(specs, s)
	}
	return specs, nil
}

func (m *TenantModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	res := &ApplyResult{}
	note := func(msg string) { res.Changed, res.Messages = true, append(res.Messages, msg) }
	reload := false
	for _, f := range tenantFiles() {
		if cur, err := rc.Runner.ReadFile(f.path); err == nil && string(cur) == f.content {
			continue
		}
		if err := rc.Runner.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return nil, err
		}
		if err := rc.Runner.WriteFile(f.path, []byte(f.content), f.mode); err != nil {
			return nil, fmt.Errorf("writing %s: %w", f.path, err)
		}
		reload = reload || strings.HasPrefix(f.path, tenantUnitDir)
		note("wrote " + f.path)
	}
	specs, err := loadTenantSpecs(rc)
	if err != nil {
		return nil, err
	}
	mgr := NewTenantManager(rc, io.Discard)
	for _, s := range specs {
		if mgr.UnitDrift(s) {
			if err := mgr.WriteUnit(s); err != nil {
				return nil, err
			}
			reload = true
			note("wrote volume mount unit of tenant " + s.Name)
		}
	}
	if reload {
		if w := systemdReload(ctx, rc); w != "" {
			res.Warnings = append(res.Warnings, w)
		}
	}
	for _, t := range tenantTimers {
		if unitOn(ctx, rc, t) {
			continue
		}
		if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", t); err != nil {
			res.Warnings = append(res.Warnings, "enabling "+t+": "+firstLine(err.Error()))
			continue
		}
		note("enabled " + t)
	}
	for _, s := range specs {
		u := mgr.UnitFor(s.Name)
		if unitOn(ctx, rc, u) {
			continue
		}
		if _, err := rc.Runner.Run(ctx, "systemctl", "enable", "--now", u); err != nil {
			return nil, fmt.Errorf("enabling %s: %w", u, err)
		}
		note("enabled " + u)
	}
	group := rc.Config.Modules.Tenant.Group()
	if _, err := rc.Runner.Query(ctx, "getent", "group", group); err != nil {
		if _, err := rc.Runner.Run(ctx, "groupadd", "--system", group); err != nil {
			return nil, fmt.Errorf("creating group %s: %w", group, err)
		}
		note("created group " + group)
	}
	if err := validateFleetSudoBinary(); err != nil {
		return nil, err
	}
	want := tenantSudoersContent(group)
	cur, err := rc.Runner.ReadFile(tenantSudoersPath())
	info, statErr := os.Lstat(tenantSudoersPath())
	if err != nil || string(cur) != string(want) || statErr != nil || !fleetSudoDropInSafe(info) {
		if err := installSudoersFile(ctx, rc, tenantSudoersPath(), want, "tenant operator"); err != nil {
			return nil, err
		}
		note("installed tenant operator sudoers rule for %" + group)
	}
	if rc.DryRun {
		res.Changed = false
	}
	return res, nil
}

// tenantGuardScript restores GPU access of running containers whose device
// filter a systemd daemon-reload rebuilt without their /dev/nvidia* nodes
// (containers started with --gpus on runc). Verified on dgx02 2026-10-20.
const tenantGuardScript = `#!/bin/bash
# Managed by rootfiles: GPU device guard. Do not edit.
# Usage: rootfiles-gpu-guard [--dry-run] [container ...]  (no args: all running GPU containers)
set -u
DRY=0
[ "${1:-}" = "--dry-run" ] && { DRY=1; shift; }
log() { echo "$*"; [ -n "${INVOCATION_ID:-}" ] || logger -t rootfiles-gpu-guard -- "$*" 2>/dev/null || true; }

targets=("$@")
[ ${#targets[@]} -eq 0 ] && mapfile -t targets < <(docker ps -q)

rc=0
for c in "${targets[@]}"; do
  info=$(docker inspect -f '{{.Name}}|{{.Id}}|{{.HostConfig.Runtime}}|{{json .HostConfig.DeviceRequests}}' "$c" 2>/dev/null) || continue
  IFS='|' read -r name id runtime req <<<"$info"
  name=${name#/}
  case "$req" in *'"gpu"'*) ;; *) continue ;; esac
  if docker exec "$c" sh -c ': </dev/nvidiactl' 2>/dev/null; then
    continue
  fi
  if [ "$runtime" = "nvidia" ]; then
    log "WARN $name: GPU access lost on runtime=nvidia (unexpected), not touching"
    rc=1; continue
  fi
  mapfile -t devs < <(docker exec "$c" sh -c 'for d in /dev/nvidia*; do [ -c "$d" ] && echo "$d"; done' 2>/dev/null)
  if [ ${#devs[@]} -eq 0 ]; then
    log "WARN $name: no /dev/nvidia* nodes inside, needs restart"; rc=1; continue
  fi
  args=(); for d in "${devs[@]}"; do args+=("DeviceAllow=$d rw"); done
  if [ $DRY -eq 1 ]; then
    log "DRY $name: GPU access lost, would allow: ${devs[*]}"; rc=1; continue
  fi
  systemctl set-property --runtime "docker-$id.scope" "${args[@]}"
  if docker exec "$c" sh -c ': </dev/nvidiactl' 2>/dev/null; then
    log "HEALED $name: ${devs[*]}"
  else
    log "FAIL $name: still no GPU access, restart needed (docker restart $name)"; rc=1
  fi
done
exit $rc
`
