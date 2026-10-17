package module

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/STAIxBWLB/rootfiles/internal/ui"
)

// GPUAllocation represents a per-user GPU assignment.
type GPUAllocation struct {
	Username  string `json:"username"`
	GPUs      []int  `json:"gpus"`
	Method    string `json:"method"`
	UpdatedAt string `json:"updated_at"`
}

// GPUAllocationsDB is the persistent store for GPU allocations.
type GPUAllocationsDB struct {
	Version     int             `json:"version"`
	TotalGPUs   int             `json:"total_gpus"`
	GPUModel    string          `json:"gpu_model"`
	Allocations []GPUAllocation `json:"allocations"`
}

type GPUModule struct{}

func NewGPUModule() *GPUModule    { return &GPUModule{} }
func (m *GPUModule) Name() string { return "gpu" }

func (m *GPUModule) Check(_ context.Context, rc *RunContext) (*CheckResult, error) {
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}
	db, err := loadGPUDB(rc)
	if err != nil || len(db.Allocations) == 0 {
		return &CheckResult{Satisfied: true}, nil
	}

	var changes []Change
	for _, alloc := range db.Allocations {
		method := effectiveMethod(rc, alloc.Method)

		if method == "env" || method == "both" {
			scriptPath := profileDScriptPath(alloc.Username)
			expected := buildProfileDScript(alloc.Username, alloc.GPUs)
			data, _ := rc.Runner.ReadFile(scriptPath)
			if string(data) != expected {
				changes = append(changes, Change{
					Description: fmt.Sprintf("Write GPU env script for %s (GPUs: %s)", alloc.Username, gpuListStr(alloc.GPUs)),
					Command:     fmt.Sprintf("write %s", scriptPath),
				})
			}
		}

		if method == "cgroup" || method == "both" {
			u, err := user.Lookup(alloc.Username)
			if err != nil {
				continue
			}
			slicePath := cgroupSlicePath(u.Uid)
			expected := buildCgroupConf(alloc.GPUs)
			data, _ := rc.Runner.ReadFile(slicePath)
			if string(data) != expected {
				changes = append(changes, Change{
					Description: fmt.Sprintf("Write cgroup GPU slice for %s (GPUs: %s)", alloc.Username, gpuListStr(alloc.GPUs)),
					Command:     fmt.Sprintf("write %s", slicePath),
				})
			}
		}
	}

	// Check Docker wrapper
	expectedWrapper := buildDockerWrapper(gpuDBPath(rc))
	wrapperData, _ := rc.Runner.ReadFile(dockerWrapperPath)
	if string(wrapperData) != expectedWrapper {
		changes = append(changes, Change{
			Description: "Install Docker GPU enforcement wrapper",
			Command:     fmt.Sprintf("write %s", dockerWrapperPath),
		})
	}

	return &CheckResult{
		Satisfied: len(changes) == 0,
		Changes:   changes,
	}, nil
}

func (m *GPUModule) Apply(ctx context.Context, rc *RunContext) (*ApplyResult, error) {
	if err := requireTrustedMetadata(rc); err != nil {
		return nil, err
	}
	db, err := loadGPUDB(rc)
	if err != nil || len(db.Allocations) == 0 {
		// Remove wrapper if it exists but no allocations remain
		wrapperData, _ := rc.Runner.ReadFile(dockerWrapperPath)
		if len(wrapperData) > 0 {
			removeDockerWrapper(ctx, rc)
			return &ApplyResult{Changed: true, Messages: []string{"Removed Docker GPU enforcement wrapper (no allocations)"}}, nil
		}
		return &ApplyResult{Changed: false}, nil
	}

	var messages, warnings []string
	changed := false

	for _, alloc := range db.Allocations {
		method := effectiveMethod(rc, alloc.Method)

		if method == "env" || method == "both" {
			scriptPath := profileDScriptPath(alloc.Username)
			expected := buildProfileDScript(alloc.Username, alloc.GPUs)
			data, _ := rc.Runner.ReadFile(scriptPath)
			if string(data) != expected {
				if err := rc.Runner.WriteFile(scriptPath, []byte(expected), 0644); err != nil {
					return nil, fmt.Errorf("writing profile.d script for %s: %w", alloc.Username, err)
				}
				messages = append(messages, fmt.Sprintf("GPU env script for %s (GPUs: %s)", alloc.Username, gpuListStr(alloc.GPUs)))
				changed = true
			}
		}

		if method == "cgroup" || method == "both" {
			u, err := user.Lookup(alloc.Username)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("cgroup GPU limit for %s skipped: %v", alloc.Username, err))
				continue
			}
			slicePath := cgroupSlicePath(u.Uid)
			expected := buildCgroupConf(alloc.GPUs)
			data, _ := rc.Runner.ReadFile(slicePath)
			if string(data) != expected {
				sliceDir := filepath.Dir(slicePath)
				if err := rc.Runner.MkdirAll(sliceDir, 0755); err != nil {
					return nil, fmt.Errorf("creating slice dir for %s: %w", alloc.Username, err)
				}
				if err := rc.Runner.WriteFile(slicePath, []byte(expected), 0644); err != nil {
					return nil, fmt.Errorf("writing cgroup conf for %s: %w", alloc.Username, err)
				}
				if w := systemdReload(ctx, rc); w != "" {
					warnings = append(warnings, w)
				}
				messages = append(messages, fmt.Sprintf("cgroup GPU slice for %s (GPUs: %s)", alloc.Username, gpuListStr(alloc.GPUs)))
				changed = true
			}
		}
	}

	// Apply Docker wrapper
	expectedWrapper := buildDockerWrapper(gpuDBPath(rc))
	wrapperData, _ := rc.Runner.ReadFile(dockerWrapperPath)
	if string(wrapperData) != expectedWrapper {
		if err := rc.Runner.WriteFile(dockerWrapperPath, []byte(expectedWrapper), 0755); err != nil {
			return nil, fmt.Errorf("writing docker wrapper: %w", err)
		}
		messages = append(messages, "Docker GPU enforcement wrapper installed")
		changed = true
	}

	return &ApplyResult{Changed: changed, Messages: messages, Warnings: warnings}, nil
}

// systemdReload runs daemon-reload and returns a warning (not an error):
// hosts without systemd as PID 1 still get the files, applied on boot.
func systemdReload(ctx context.Context, rc *RunContext) string {
	if _, err := rc.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return "systemctl daemon-reload failed: " + firstLine(err.Error())
	}
	return ""
}

// AssignGPUs assigns GPUs to a user and applies immediately.
func AssignGPUs(ctx context.Context, rc *RunContext, username string, gpus []int, method string) error {
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	if _, err := user.Lookup(username); err != nil {
		return fmt.Errorf("user %s not found", username)
	}

	if method == "" {
		method = effectiveMethod(rc, "")
	}

	// Read-modify-write the DB under an exclusive lock. Without this, two
	// concurrent `rootfiles gpu assign` calls can load the same state and each
	// overwrite the other's allocation, losing one.
	if err := withGPUDBLock(rc, func(db *GPUAllocationsDB) error {
		totalGPUs, gpuModel := detectGPUInfo(ctx, rc)
		if totalGPUs > 0 {
			db.TotalGPUs = totalGPUs
			db.GPUModel = gpuModel
		}
		if err := validateGPURequest(db, username, gpus); err != nil {
			return err
		}
		upsertAllocation(db, GPUAllocation{
			Username:  username,
			GPUs:      gpus,
			Method:    method,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}); err != nil {
		return err
	}

	if err := applyAllocationMethods(ctx, rc, username, gpus, method); err != nil {
		return err
	}

	fmt.Printf("Assigned GPUs %s to %s (method: %s)\n", gpuListStr(gpus), username, method)
	return nil
}

// RevokeGPUs removes GPU assignment for a user.
func RevokeGPUs(ctx context.Context, rc *RunContext, username string) error {
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	var method string
	var allocationsRemaining int
	if err := withGPUDBLock(rc, func(db *GPUAllocationsDB) error {
		found := false
		var remaining []GPUAllocation
		for _, a := range db.Allocations {
			if a.Username == username {
				found = true
				method = a.Method
			} else {
				remaining = append(remaining, a)
			}
		}
		if !found {
			return fmt.Errorf("no GPU allocation found for %s", username)
		}
		db.Allocations = remaining
		allocationsRemaining = len(remaining)
		return nil
	}); err != nil {
		return err
	}

	if method == "" {
		method = effectiveMethod(rc, "")
	}

	// Clean up env script
	if method == "env" || method == "both" {
		scriptPath := profileDScriptPath(username)
		if rc.DryRun {
			fmt.Printf("[dry-run] would remove %s\n", scriptPath)
		} else {
			if err := rc.Runner.Remove(scriptPath); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing %s: %w", scriptPath, err)
			}
		}
	}

	// Clean up cgroup conf
	if method == "cgroup" || method == "both" {
		u, err := user.Lookup(username)
		if err == nil {
			slicePath := cgroupSlicePath(u.Uid)
			sliceDir := filepath.Dir(slicePath)
			if rc.DryRun {
				fmt.Printf("[dry-run] would remove %s\n", sliceDir)
			} else {
				// Remove only our drop-in; the slice dir may hold others.
				if err := rc.Runner.Remove(slicePath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("removing %s: %w", slicePath, err)
				}
				_ = rc.Runner.Remove(sliceDir) // only succeeds when empty
				if w := systemdReload(ctx, rc); w != "" {
					fmt.Println("  " + ui.MarkWarn + " " + w)
				}
			}
		}
	}

	// Remove Docker wrapper if no allocations remain
	if allocationsRemaining == 0 {
		removeDockerWrapper(ctx, rc)
	} else {
		// Reinstall wrapper (DB path unchanged, but content may differ)
		if err := installDockerWrapper(rc); err != nil {
			return fmt.Errorf("updating docker wrapper: %w", err)
		}
	}

	fmt.Printf("Revoked GPU allocation for %s\n", username)
	return nil
}

// ListGPUAllocations prints the current GPU allocation table.
func ListGPUAllocations(rc *RunContext) error {
	WarnUntrustedMetadata(rc)
	db, err := loadGPUDB(rc)
	if err != nil || len(db.Allocations) == 0 {
		ui.WriteSection(os.Stdout, "GPU Allocations")
		ui.WriteHint(os.Stdout, "No GPU allocations configured.")
		return nil
	}

	ui.WriteSection(os.Stdout, "GPU Allocations")
	if db.TotalGPUs > 0 {
		totalLabel := fmt.Sprintf("%d", db.TotalGPUs)
		if db.GPUModel != "" {
			totalLabel = fmt.Sprintf("%d (%s)", db.TotalGPUs, db.GPUModel)
		}
		ui.WriteKV(os.Stdout, "Total GPUs", totalLabel)
	}

	// Preserve the fixed-width table so integration scenarios can still grep
	// for usernames and GPU indices. Header is coloured; value rows are plain
	// to keep column alignment predictable.
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "  "+ui.StyleSection.Render(fmt.Sprintf("%-15s %-15s %-10s %s", "USER", "GPUs", "METHOD", "UPDATED")))
	fmt.Fprintln(os.Stdout, "  "+ui.StyleHint.Render(fmt.Sprintf("%-15s %-15s %-10s %s", "----", "----", "------", "-------")))
	for _, a := range db.Allocations {
		fmt.Fprintf(os.Stdout, "  %-15s %-15s %-10s %s\n",
			a.Username, gpuListStr(a.GPUs), a.Method, a.UpdatedAt)
	}
	return nil
}

// gpuSMIInfo holds per-GPU nvidia-smi data.
type gpuSMIInfo struct {
	Name     string
	MemUsed  string
	MemTotal string
	Util     string
}

// ShowGPUStatus shows nvidia-smi output cross-referenced with allocations.
func ShowGPUStatus(ctx context.Context, rc *RunContext) error {
	WarnUntrustedMetadata(rc)
	totalGPUs := countGPUDevices()
	if totalGPUs == 0 {
		return fmt.Errorf("no NVIDIA GPU devices found in /dev")
	}

	// Query nvidia-smi — may only see a subset due to cgroup restrictions.
	// We parse what's visible and leave the rest as "N/A".
	smiData := make(map[int]*gpuSMIInfo)
	result, err := rc.Runner.Query(ctx, "nvidia-smi",
		"--query-gpu=index,name,memory.used,memory.total,utilization.gpu",
		"--format=csv,noheader,nounits")
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			fields := strings.Split(line, ", ")
			if len(fields) < 5 {
				continue
			}
			idx, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
			smiData[idx] = &gpuSMIInfo{
				Name:     strings.TrimSpace(fields[1]),
				MemUsed:  strings.TrimSpace(fields[2]),
				MemTotal: strings.TrimSpace(fields[3]),
				Util:     strings.TrimSpace(fields[4]),
			}
		}
	}

	db, _ := loadGPUDB(rc)

	// Build assignment map
	assignMap := make(map[int]string)
	for _, a := range db.Allocations {
		for _, g := range a.GPUs {
			assignMap[g] = a.Username
		}
	}

	// Use model from DB if available (visible GPUs may be restricted)
	defaultModel := db.GPUModel
	if defaultModel == "" {
		for _, info := range smiData {
			defaultModel = info.Name
			break
		}
	}

	ui.WriteSection(os.Stdout, "GPU Status")
	fmt.Fprintln(os.Stdout, "  "+ui.StyleSection.Render(fmt.Sprintf("%-5s %-25s %-15s %-6s %s", "GPU", "MODEL", "MEMORY", "UTIL", "ASSIGNED TO")))
	fmt.Fprintln(os.Stdout, "  "+ui.StyleHint.Render(fmt.Sprintf("%-5s %-25s %-15s %-6s %s", "---", "-----", "------", "----", "-----------")))

	for i := 0; i < totalGPUs; i++ {
		assignedLabel := ui.StyleHint.Render("(unassigned)")
		if u, ok := assignMap[i]; ok {
			assignedLabel = ui.StyleValue.Render(u)
		}

		if info, ok := smiData[i]; ok {
			name := info.Name
			if len(name) > 24 {
				name = name[:24]
			}
			fmt.Fprintf(os.Stdout, "  %-5d %-25s %5s/%-5s MB %4s%%  %s\n",
				i, name, info.MemUsed, info.MemTotal, info.Util, assignedLabel)
		} else {
			// GPU exists but not visible to nvidia-smi (cgroup restricted)
			name := defaultModel
			if name == "" {
				name = "NVIDIA GPU"
			}
			if len(name) > 24 {
				name = name[:24]
			}
			fmt.Fprintf(os.Stdout, "  %-5d %-25s %s     ---   %s\n",
				i, name, ui.StyleWarning.Render("(restricted)"), assignedLabel)
		}
	}

	return nil
}

// Docker wrapper constants
const (
	dockerWrapperPath = "/usr/local/bin/docker"
	dockerRealPath    = "/usr/bin/docker"
)

// buildDockerWrapper generates a bash wrapper script that enforces per-user GPU allocations for Docker commands.
func buildDockerWrapper(gpuDBPath string) string {
	return `#!/bin/bash
# Managed by rootfiles-v2 — do not edit
# Docker wrapper that enforces per-user GPU allocations.
set -euo pipefail

REAL_DOCKER="` + dockerRealPath + `"
GPU_DB="` + gpuDBPath + `"

# Root bypass — no GPU restrictions
if [ "$(id -u)" = "0" ]; then
    exec "$REAL_DOCKER" "$@"
fi

# Read user's GPU allocation from the JSON database via python3
GPUS=""
if [ -f "$GPU_DB" ]; then
    GPUS=$(python3 -c "
import json, os
try:
    with open('$GPU_DB') as f:
        db = json.load(f)
    user = os.environ.get('USER', '')
    for a in db.get('allocations', []):
        if a.get('username') == user:
            print(','.join(str(g) for g in a.get('gpus', [])))
            break
except Exception:
    pass
" 2>/dev/null || true)
fi

# No allocation — pass through without restriction
if [ -z "$GPUS" ]; then
    exec "$REAL_DOCKER" "$@"
fi

# Find the subcommand by skipping global flags
args=("$@")
subcmd_idx=-1
i=0
while [ $i -lt ${#args[@]} ]; do
    arg="${args[$i]}"
    case "$arg" in
        -H|--host|--config|--context|-c|-l|--log-level)
            i=$((i + 2))
            ;;
        -H=*|--host=*|--config=*|--context=*|-l=*|--log-level=*)
            i=$((i + 1))
            ;;
        -D|--debug|--tls|--tlsverify)
            i=$((i + 1))
            ;;
        -*)
            i=$((i + 1))
            ;;
        *)
            subcmd_idx=$i
            break
            ;;
    esac
done

if [ $subcmd_idx -lt 0 ]; then
    exec "$REAL_DOCKER" "$@"
fi

subcmd="${args[$subcmd_idx]}"

case "$subcmd" in
    run|create)
        # Rebuild args: strip --gpus flag (with or without = value), inject NVIDIA_VISIBLE_DEVICES
        new_args=()
        j=0
        while [ $j -lt ${#args[@]} ]; do
            if [ $j -eq $((subcmd_idx + 1)) ] || [ ${#new_args[@]} -eq $((subcmd_idx + 1)) ] && [ $j -eq $((subcmd_idx)) ]; then
                : # handled below
            fi
            arg="${args[$j]}"
            if [ $j -gt $subcmd_idx ]; then
                case "$arg" in
                    --gpus=*)
                        j=$((j + 1))
                        continue
                        ;;
                    --gpus)
                        j=$((j + 2))
                        continue
                        ;;
                esac
            fi
            new_args+=("$arg")
            j=$((j + 1))
        done
        # Inject -e NVIDIA_VISIBLE_DEVICES right after the subcommand
        final_args=()
        k=0
        while [ $k -lt ${#new_args[@]} ]; do
            final_args+=("${new_args[$k]}")
            if [ $k -eq $subcmd_idx ]; then
                final_args+=("-e" "NVIDIA_VISIBLE_DEVICES=$GPUS")
                final_args+=("-e" "CUDA_VISIBLE_DEVICES=$GPUS")
            fi
            k=$((k + 1))
        done
        exec "$REAL_DOCKER" "${final_args[@]}"
        ;;
    compose)
        export NVIDIA_VISIBLE_DEVICES="$GPUS"
        export CUDA_VISIBLE_DEVICES="$GPUS"
        exec "$REAL_DOCKER" "$@"
        ;;
    *)
        exec "$REAL_DOCKER" "$@"
        ;;
esac
`
}

// installDockerWrapper writes the Docker wrapper script if it is missing or outdated.
func installDockerWrapper(rc *RunContext) error {
	dbPath := gpuDBPath(rc)
	expected := buildDockerWrapper(dbPath)
	data, _ := rc.Runner.ReadFile(dockerWrapperPath)
	if string(data) == expected {
		return nil
	}
	if rc.DryRun {
		fmt.Printf("[dry-run] would write %s\n", dockerWrapperPath)
		return nil
	}
	return rc.Runner.WriteFile(dockerWrapperPath, []byte(expected), 0755)
}

// removeDockerWrapper removes the Docker wrapper script if it exists.
func removeDockerWrapper(ctx context.Context, rc *RunContext) {
	if rc.DryRun {
		fmt.Printf("[dry-run] would remove %s\n", dockerWrapperPath)
		return
	}
	if err := rc.Runner.Remove(dockerWrapperPath); err != nil && !os.IsNotExist(err) {
		fmt.Printf("  %s removing %s: %v\n", ui.MarkWarn, dockerWrapperPath, err)
	}
}

// --- helpers ---

// validateGPURequest checks that every requested GPU index is in range and not
// already assigned to another user. Self-reassignment is allowed.
func validateGPURequest(db *GPUAllocationsDB, username string, gpus []int) error {
	if db.TotalGPUs > 0 {
		for _, g := range gpus {
			if g < 0 || g >= db.TotalGPUs {
				return fmt.Errorf("GPU index %d out of range (0-%d)", g, db.TotalGPUs-1)
			}
		}
	}
	for _, alloc := range db.Allocations {
		if alloc.Username == username {
			continue
		}
		for _, existing := range alloc.GPUs {
			for _, requested := range gpus {
				if existing == requested {
					return fmt.Errorf("GPU %d is already assigned to %s", requested, alloc.Username)
				}
			}
		}
	}
	return nil
}

// upsertAllocation replaces an existing allocation for the user or appends a new one.
func upsertAllocation(db *GPUAllocationsDB, alloc GPUAllocation) {
	for i, a := range db.Allocations {
		if a.Username == alloc.Username {
			db.Allocations[i] = alloc
			return
		}
	}
	db.Allocations = append(db.Allocations, alloc)
}

// applyAllocationMethods writes the env script, cgroup slice, and Docker
// wrapper side effects for a single user's GPU allocation. Each block is
// gated on the method ("env", "cgroup", or "both") and on rc.DryRun.
func applyAllocationMethods(ctx context.Context, rc *RunContext, username string, gpus []int, method string) error {
	if method == "env" || method == "both" {
		scriptPath := profileDScriptPath(username)
		content := buildProfileDScript(username, gpus)
		if rc.DryRun {
			fmt.Printf("[dry-run] would write %s\n", scriptPath)
		} else {
			if err := rc.Runner.WriteFile(scriptPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("writing profile.d script: %w", err)
			}
		}
	}

	if method == "cgroup" || method == "both" {
		u, err := user.Lookup(username)
		if err != nil {
			return fmt.Errorf("looking up %s for cgroup limit: %w", username, err)
		}
		slicePath := cgroupSlicePath(u.Uid)
		content := buildCgroupConf(gpus)
		if rc.DryRun {
			fmt.Printf("[dry-run] would write %s\n", slicePath)
		} else {
			sliceDir := filepath.Dir(slicePath)
			if err := rc.Runner.MkdirAll(sliceDir, 0755); err != nil {
				return fmt.Errorf("creating %s: %w", sliceDir, err)
			}
			if err := rc.Runner.WriteFile(slicePath, []byte(content), 0644); err != nil {
				return fmt.Errorf("writing cgroup conf: %w", err)
			}
			if w := systemdReload(ctx, rc); w != "" {
				fmt.Println("  " + ui.MarkWarn + " " + w)
			}
		}
	}

	if err := installDockerWrapper(rc); err != nil {
		return fmt.Errorf("installing docker wrapper: %w", err)
	}
	return nil
}

func gpuDBPath(rc *RunContext) string {
	homeBase := rc.Config.Users.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}
	return filepath.Join(homeBase, ".rootfiles", "gpu-allocations.json")
}

// LoadGPUDB is the exported wrapper over loadGPUDB used by the `status`
// command in internal/cli. The internal package uses loadGPUDB directly
// inside the flock critical section; external read-only callers go through
// this wrapper so the API surface stays explicit.
func LoadGPUDB(rc *RunContext) (*GPUAllocationsDB, error) {
	return loadGPUDB(rc)
}

func loadGPUDB(rc *RunContext) (*GPUAllocationsDB, error) {
	var db GPUAllocationsDB
	data, err := rc.Runner.ReadFile(gpuDBPath(rc))
	if err != nil {
		return &db, err
	}
	if err := json.Unmarshal(data, &db); err != nil {
		return &db, fmt.Errorf("parsing GPU database: %w", err)
	}
	return &db, nil
}

func saveGPUDB(rc *RunContext, db *GPUAllocationsDB) error {
	dbPath := gpuDBPath(rc)
	homeBase := rc.Config.Users.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}
	if err := ensureMetaDir(rc.Runner, homeBase); err != nil {
		return fmt.Errorf("ensuring metadata dir: %w", err)
	}
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling GPU database: %w", err)
	}

	if rc.Runner.DryRun {
		return rc.Runner.WriteFile(dbPath, data, 0644)
	}

	// Atomic write: create sibling temp file then rename. This guarantees readers
	// never observe a partial JSON document, which could crash the Docker wrapper
	// (buildDockerWrapper's embedded python3 json.load).
	tmp := dbPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing temp gpu db: %w", err)
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming gpu db: %w", err)
	}
	return nil
}

// withGPUDBLock runs fn with exclusive access to the GPU allocation DB.
// It acquires an advisory flock on a sibling lock file, loads the current DB
// under the lock, runs fn (which may mutate db), and atomically writes the
// result. This prevents concurrent `rootfiles gpu assign`/`revoke` calls from
// racing on read-modify-write and losing allocations.
//
// When the Runner is in dry-run mode the lock file is not created (it would
// be a real side effect); fn still runs so validation errors surface and
// saveGPUDB routes through Runner.WriteFile which no-ops.
func withGPUDBLock(rc *RunContext, fn func(*GPUAllocationsDB) error) error {
	if rc.Runner.DryRun {
		db, _ := loadGPUDB(rc)
		if db.Version == 0 {
			db.Version = 1
		}
		if err := fn(db); err != nil {
			return err
		}
		return saveGPUDB(rc, db)
	}

	homeBase := rc.Config.Users.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}
	if err := ensureMetaDir(rc.Runner, homeBase); err != nil {
		return fmt.Errorf("ensuring metadata dir: %w", err)
	}

	lockPath := gpuDBPath(rc) + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("opening gpu db lock: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquiring gpu db lock: %w", err)
	}

	db, _ := loadGPUDB(rc)
	if db.Version == 0 {
		db.Version = 1
	}
	if err := fn(db); err != nil {
		return err
	}
	return saveGPUDB(rc, db)
}

func effectiveMethod(rc *RunContext, method string) string {
	if method != "" {
		return method
	}
	m := rc.Config.Modules.Nvidia.GPUAllocation.Method
	if m == "" {
		return "env"
	}
	return m
}

func profileDScriptPath(username string) string {
	return fmt.Sprintf("/etc/profile.d/rootfiles-gpu-%s.sh", username)
}

func buildProfileDScript(username string, gpus []int) string {
	gpuStr := gpuListStr(gpus)
	return fmt.Sprintf(`# Managed by rootfiles-v2 — do not edit
if [ "$(id -un)" = "%s" ]; then
    export CUDA_VISIBLE_DEVICES=%s
    export NVIDIA_VISIBLE_DEVICES=%s
fi
`, username, gpuStr, gpuStr)
}

func cgroupSlicePath(uid string) string {
	return fmt.Sprintf("/etc/systemd/system/user-%s.slice.d/gpu-access.conf", uid)
}

func buildCgroupConf(gpus []int) string {
	var lines []string
	lines = append(lines, "# Managed by rootfiles-v2 — do not edit")
	lines = append(lines, "[Slice]")
	lines = append(lines, "DevicePolicy=strict")
	// Standard character devices needed for basic user operation
	lines = append(lines, "DeviceAllow=/dev/null rwm")
	lines = append(lines, "DeviceAllow=/dev/zero rwm")
	lines = append(lines, "DeviceAllow=/dev/full rwm")
	lines = append(lines, "DeviceAllow=/dev/random rwm")
	lines = append(lines, "DeviceAllow=/dev/urandom rwm")
	lines = append(lines, "DeviceAllow=/dev/tty rwm")
	lines = append(lines, "DeviceAllow=/dev/ptmx rwm")
	lines = append(lines, "DeviceAllow=char-pts rwm")
	lines = append(lines, "DeviceAllow=/dev/fuse rwm")
	// Assigned NVIDIA GPUs only
	for _, g := range gpus {
		lines = append(lines, fmt.Sprintf("DeviceAllow=/dev/nvidia%d rwm", g))
	}
	lines = append(lines, "DeviceAllow=/dev/nvidiactl rwm")
	lines = append(lines, "DeviceAllow=/dev/nvidia-uvm rwm")
	lines = append(lines, "DeviceAllow=/dev/nvidia-uvm-tools rwm")
	lines = append(lines, "DeviceAllow=/dev/nvidia-caps/* rwm")
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

func gpuListStr(gpus []int) string {
	sorted := make([]int, len(gpus))
	copy(sorted, gpus)
	sort.Ints(sorted)
	parts := make([]string, len(sorted))
	for i, g := range sorted {
		parts[i] = strconv.Itoa(g)
	}
	return strings.Join(parts, ",")
}

func detectGPUInfo(ctx context.Context, rc *RunContext) (int, string) {
	// Count GPU devices from /dev/nvidia* to avoid cgroup visibility restrictions.
	// nvidia-smi only sees GPUs allowed by the calling user's cgroup, but
	// /dev/nvidia[0-9]* device files always exist for all physical GPUs.
	count := countGPUDevices()
	model := ""

	// Try nvidia-smi for the model name (may see fewer GPUs if cgroup-restricted)
	result, err := rc.Runner.Query(ctx, "nvidia-smi",
		"--query-gpu=name", "--format=csv,noheader")
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				model = line
				break
			}
		}
	}

	return count, model
}

// countGPUDevices counts /dev/nvidia[0-9]+ device files.
func countGPUDevices() int {
	entries, err := os.ReadDir("/dev")
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "nvidia") {
			rest := name[len("nvidia"):]
			if rest != "" {
				if _, err := strconv.Atoi(rest); err == nil {
					count++
				}
			}
		}
	}
	return count
}
