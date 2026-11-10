package tenant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var (
	onlineCPUsPath  = "/sys/devices/system/cpu/online"
	memoryNodesPath = "/sys/devices/system/node/has_memory"
)

type cpuRange struct{ first, last int }

// parseCPUList uses intervals rather than expanding potentially huge ranges.
// Empty values represent unmanaged settings. Non-empty results are merged and sorted.
func parseCPUList(value string) ([]cpuRange, error) {
	if value == "" {
		return nil, nil
	}
	var ranges []cpuRange
	for _, item := range strings.Split(value, ",") {
		parts := strings.Split(item, "-")
		if len(parts) > 2 {
			return nil, fmt.Errorf("invalid range")
		}
		nums := make([]int, len(parts))
		for i, part := range parts {
			if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
				return nil, fmt.Errorf("invalid index")
			}
			n, err := strconv.ParseUint(part, 10, 31)
			if err != nil {
				return nil, err
			}
			nums[i] = int(n)
		}
		end := nums[len(nums)-1]
		if nums[0] > end {
			return nil, fmt.Errorf("descending range")
		}
		ranges = append(ranges, cpuRange{nums[0], end})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].first < ranges[j].first })
	merged := ranges[:0]
	for _, r := range ranges {
		if len(merged) > 0 && int64(r.first) <= int64(merged[len(merged)-1].last)+1 {
			merged[len(merged)-1].last = max(merged[len(merged)-1].last, r.last)
		} else {
			merged = append(merged, r)
		}
	}
	return merged, nil
}

func sameCPUList(a, b string) bool {
	x, err := parseCPUList(a)
	if err != nil {
		return false
	}
	y, err := parseCPUList(b)
	return err == nil && slices.Equal(x, y)
}

func readHostList(file, field string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("cannot read host %s list %s: %w; check sysfs is mounted and readable before retrying", field, file, err)
	}
	value := strings.TrimSpace(string(b))
	if _, err := parseCPUList(value); err != nil || value == "" {
		return "", fmt.Errorf("host %s list %s is empty or invalid (%q); check sysfs before retrying", field, file, value)
	}
	return value, nil
}

func validateHostCpuset(pin *Cpuset) error {
	if pin == nil {
		return nil
	}
	for _, field := range []struct{ name, value, file string }{
		{"cpuset.cpus", pin.CPUs, onlineCPUsPath}, {"cpuset.mems", pin.Mems, memoryNodesPath},
	} {
		if field.value == "" {
			continue
		}
		want, err := parseCPUList(field.value)
		if err != nil {
			return fmt.Errorf("%s: invalid list %q; use comma-separated N or N-M with N <= M and no spaces", field.name, field.value)
		}
		available, err := readHostList(field.file, field.name)
		if err != nil {
			return err
		}
		host, _ := parseCPUList(available)
		for _, r := range want {
			contained := false
			for _, h := range host {
				if h.first <= r.first && h.last >= r.last {
					contained = true
					break
				}
			}
			if !contained {
				return fmt.Errorf("%s %q is outside the host's available list %q; choose only values from that list and retry", field.name, field.value, available)
			}
		}
	}
	return nil
}

// cpusetArgs emits only managed, differing settings. It also supplies run flags
// when the current settings are empty.
func cpusetArgs(pin *Cpuset, current Cpuset) []string {
	if pin == nil {
		return nil
	}
	var args []string
	if pin.CPUs != "" && !sameCPUList(pin.CPUs, current.CPUs) {
		args = append(args, "--cpuset-cpus", pin.CPUs)
	}
	if pin.Mems != "" && !sameCPUList(pin.Mems, current.Mems) {
		args = append(args, "--cpuset-mems", pin.Mems)
	}
	return args
}

func (m *Manager) updateCpuset(ctx context.Context, s *Spec, st containerState) error {
	args := cpusetArgs(s.Cpuset, st.Cpuset)
	if len(args) == 0 {
		return nil
	}
	args = append(append([]string{"update"}, args...), ContainerName(s.Name))
	if err := m.run(ctx, "docker", args...); err != nil {
		return fmt.Errorf("updating cpuset for %s failed: %w; check `docker inspect %s` and the host CPU/node lists, then retry `rootfiles tenant cpuset %s` with the desired flags", s.Name, err, ContainerName(s.Name), s.Name)
	}
	return nil
}

// UpdateCpuset applies only live settings, even if a recreate is pending.
func (m *Manager) UpdateCpuset(ctx context.Context, s *Spec) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := validateHostCpuset(s.Cpuset); err != nil {
		return err
	}
	st := m.inspect(ctx, ContainerName(s.Name))
	if !st.Exists {
		return fmt.Errorf("container %s is missing or cannot be inspected; check `docker inspect %s` and run `rootfiles tenant apply %s` to create a missing container before retrying", ContainerName(s.Name), ContainerName(s.Name), s.Name)
	}
	if err := m.updateCpuset(ctx, s, st); err != nil {
		return err
	}
	if st.Digest != s.Digest() {
		m.printf("Container settings are still pending; `rootfiles tenant apply %s` is needed and will recreate the tenant to apply them. Cpuset was updated without a restart.\n", s.Name)
	}
	return nil
}

// ChangeCpuset saves the desired spec and runs only the live update. On failure
// it restores the exact previous spec, including comments. Clear is explicitly
// different from an empty (unmanaged) key: it resets both keys to full host lists.
func (m *Manager) ChangeCpuset(ctx context.Context, s *Spec, clear bool) error {
	if err := s.Validate(); err != nil {
		return err
	}
	live := *s
	if clear {
		cpus, err := readHostList(onlineCPUsPath, "cpuset.cpus")
		if err != nil {
			return err
		}
		mems, err := readHostList(memoryNodesPath, "cpuset.mems")
		if err != nil {
			return err
		}
		live.Cpuset = &Cpuset{CPUs: cpus, Mems: mems}
		s.Cpuset = nil
	}
	if err := validateHostCpuset(live.Cpuset); err != nil {
		return err
	}
	file := filepath.Join(m.SpecDir, s.Name+".yaml")
	before, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("cannot preserve spec %s before cpuset change: %w; check the spec is readable and retry", file, err)
	}
	if err := m.SaveSpec(s); err != nil {
		return err
	}
	if err := m.UpdateCpuset(ctx, &live); err != nil {
		if rerr := m.Runner.WriteFile(file, before, 0o600); rerr != nil {
			return fmt.Errorf("%w; restoring spec %s also failed: %v; restore it from the rootfiles backup before retrying", err, file, rerr)
		}
		return fmt.Errorf("%w; previous spec restored", err)
	}
	return nil
}
