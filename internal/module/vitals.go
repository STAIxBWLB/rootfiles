package module

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Overridable in tests so vitals collection stays host-free.
var (
	procRoot = "/proc"
	statfsFn = syscall.Statfs
	statFn   = syscall.Stat
)

// MountUsage reports capacity and doctor threshold status for one filesystem.
type MountUsage struct {
	Path      string  `json:"path"`
	UsedPct   float64 `json:"used_pct"`
	FreeBytes uint64  `json:"free_bytes"`
	TotalGiB  float64 `json:"total_gib"`
	FreeGiB   float64 `json:"free_gib"`
	Level     string  `json:"level"`
}

// HostVitals holds a read-only snapshot of live Linux /proc and disk vitals.
type HostVitals struct {
	Load1        float64       `json:"load_1"`
	Load5        float64       `json:"load_5"`
	Load15       float64       `json:"load_15"`
	LoadKnown    bool          `json:"load_known"`
	MemTotalMiB  uint64        `json:"mem_total_mib"`
	MemUsedMiB   uint64        `json:"mem_used_mib"`
	MemUsedPct   float64       `json:"mem_used_pct"`
	SwapTotalMiB uint64        `json:"swap_total_mib"`
	SwapUsedMiB  uint64        `json:"swap_used_mib"`
	Uptime       time.Duration `json:"uptime"`
	Mounts       []MountUsage  `json:"mounts"`
}

// CollectHostVitals reads /proc/loadavg, /proc/meminfo, /proc/uptime and
// configured filesystem mounts without executing external commands.
func CollectHostVitals(rc *RunContext) HostVitals {
	var v HostVitals
	if data, err := os.ReadFile(filepath.Join(procRoot, "loadavg")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			l1, err1 := strconv.ParseFloat(fields[0], 64)
			l5, err5 := strconv.ParseFloat(fields[1], 64)
			l15, err15 := strconv.ParseFloat(fields[2], 64)
			if err1 == nil && err5 == nil && err15 == nil {
				v.Load1, v.Load5, v.Load15 = l1, l5, l15
				v.LoadKnown = true
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(procRoot, "uptime")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			if secs, err := strconv.ParseFloat(fields[0], 64); err == nil && secs >= 0 {
				v.Uptime = time.Duration(secs * float64(time.Second))
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(procRoot, "meminfo")); err == nil {
		vals := parseMeminfoKiB(string(data))
		totalKiB := vals["MemTotal"]
		availKiB, hasAvail := vals["MemAvailable"]
		if !hasAvail {
			availKiB = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
		}
		if totalKiB > 0 {
			usedKiB := uint64(0)
			if totalKiB > availKiB {
				usedKiB = totalKiB - availKiB
			}
			v.MemTotalMiB = totalKiB / 1024
			v.MemUsedMiB = usedKiB / 1024
			v.MemUsedPct = float64(usedKiB) / float64(totalKiB) * 100
		}
		swapTotalKiB := vals["SwapTotal"]
		swapFreeKiB := vals["SwapFree"]
		if swapTotalKiB > 0 {
			v.SwapTotalMiB = swapTotalKiB / 1024
			if swapTotalKiB > swapFreeKiB {
				v.SwapUsedMiB = (swapTotalKiB - swapFreeKiB) / 1024
			}
		}
	}
	v.Mounts = collectMountUsages(rc)
	return v
}

func parseMeminfoKiB(text string) map[string]uint64 {
	out := make(map[string]uint64)
	for _, line := range strings.Split(text, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if n, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
			out[strings.TrimSpace(key)] = n
		}
	}
	return out
}

func collectMountUsages(rc *RunContext) []MountUsage {
	if rc == nil || rc.Config == nil {
		return nil
	}
	paths := []string{"/"}
	for _, p := range []string{
		rc.Config.Users.HomeBase,
		rc.Config.Modules.Docker.StorageDir,
		rc.Config.Modules.Storage.DataDir,
	} {
		if p != "" {
			paths = append(paths, p)
		}
	}
	seenDev := map[uint64]bool{}
	var out []MountUsage
	for _, p := range paths {
		var st syscall.Statfs_t
		if err := statfsFn(p, &st); err != nil || st.Blocks == 0 {
			continue
		}
		var fi syscall.Stat_t
		if statFn(p, &fi) == nil {
			if seenDev[uint64(fi.Dev)] {
				continue
			}
			seenDev[uint64(fi.Dev)] = true
		}
		denom := st.Blocks - st.Bfree + st.Bavail
		if denom == 0 {
			continue
		}
		usedPct := float64(st.Blocks-st.Bfree) / float64(denom) * 100
		freeBytes := st.Bavail * uint64(st.Bsize)
		totalBytes := st.Blocks * uint64(st.Bsize)
		out = append(out, MountUsage{
			Path:      p,
			UsedPct:   usedPct,
			FreeBytes: freeBytes,
			TotalGiB:  float64(totalBytes) / (1 << 30),
			FreeGiB:   float64(freeBytes) / (1 << 30),
			Level:     diskLevel(rc.Config.Doctor.Disk, usedPct, freeBytes),
		})
	}
	return out
}
