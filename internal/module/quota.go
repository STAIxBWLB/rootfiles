package module

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Per-user disk quotas on the home_base filesystem. XFS uses project
// quotas keyed to each home directory (so files count wherever the owner
// is); ext4 uses classic user quotas. The filesystem must already be
// mounted with quota support — changing mount options needs a remount or
// reboot and is left to the operator.

// procMountsPath is overridable in tests.
var procMountsPath = "/proc/mounts"

type mountInfo struct {
	Point, FSType string
	Options       []string
}

func (m mountInfo) has(opts ...string) bool {
	for _, o := range m.Options {
		for _, want := range opts {
			if o == want {
				return true
			}
		}
	}
	return false
}

// findMount returns the mount containing path (longest matching prefix).
func findMount(rc *RunContext, path string) (mountInfo, error) {
	data, err := rc.Runner.ReadFile(procMountsPath)
	if err != nil {
		return mountInfo{}, err
	}
	path = filepath.Clean(path)
	var best mountInfo
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		mp := f[1]
		if path == mp || strings.HasPrefix(path, strings.TrimSuffix(mp, "/")+"/") || mp == "/" {
			if len(mp) >= len(best.Point) {
				best = mountInfo{Point: mp, FSType: f[2], Options: strings.Split(f[3], ",")}
			}
		}
	}
	if best.Point == "" {
		return best, fmt.Errorf("no mount found for %s", path)
	}
	return best, nil
}

// parseSize converts "500G"/"512M"/"2T" to KiB.
func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "0" {
		return 0, nil
	}
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid size %q (want e.g. 500G)", s)
	}
	n, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q (want e.g. 500G)", s)
	}
	mult := map[byte]int64{'K': 1, 'M': 1 << 10, 'G': 1 << 20, 'T': 1 << 30}[s[len(s)-1]]
	if mult == 0 {
		return 0, fmt.Errorf("invalid size unit in %q (K, M, G or T)", s)
	}
	return n * mult, nil
}

// SetQuota sets (size "0" removes) a hard block limit for username's home.
func SetQuota(ctx context.Context, rc *RunContext, username, size string) error {
	if err := requireTrustedMetadata(rc); err != nil {
		return err
	}
	u, err := lookupManaged(username)
	if err != nil {
		return err
	}
	kib, err := parseSize(size)
	if err != nil {
		return err
	}
	mnt, err := findMount(rc, u.HomeDir)
	if err != nil {
		return err
	}

	switch mnt.FSType {
	case "xfs":
		if !mnt.has("prjquota", "pquota", "pqnoenforce") {
			return fmt.Errorf("%s (xfs) is not mounted with prjquota; add it to /etc/fstab and remount/reboot", mnt.Point)
		}
		if !rc.Runner.CommandExists("xfs_quota") {
			return fmt.Errorf("xfs_quota not found (apt-get install xfsprogs)")
		}
		if _, err := rc.Runner.Run(ctx, "xfs_quota", "-x", "-c", fmt.Sprintf("project -s -p %s %s", u.HomeDir, u.Uid), mnt.Point); err != nil {
			return fmt.Errorf("assigning project %s to %s: %w", u.Uid, u.HomeDir, err)
		}
		if _, err := rc.Runner.Run(ctx, "xfs_quota", "-x", "-c", fmt.Sprintf("limit -p bhard=%dk %s", kib, u.Uid), mnt.Point); err != nil {
			return fmt.Errorf("setting xfs project quota: %w", err)
		}
	case "ext4", "ext3":
		if !mnt.has("usrquota", "quota", "usrjquota=aquota.user") {
			return fmt.Errorf("%s (%s) is not mounted with usrquota; add it to /etc/fstab, remount, then run quotacheck -cum %s && quotaon %s",
				mnt.Point, mnt.FSType, mnt.Point, mnt.Point)
		}
		if !rc.Runner.CommandExists("setquota") {
			return fmt.Errorf("setquota not found (apt-get install quota)")
		}
		if _, err := rc.Runner.Run(ctx, "setquota", "-u", username, "0", strconv.FormatInt(kib, 10), "0", "0", mnt.Point); err != nil {
			return fmt.Errorf("setting user quota: %w", err)
		}
	default:
		return fmt.Errorf("quotas are supported on xfs and ext4; %s is %s", mnt.Point, mnt.FSType)
	}

	if err := updateUserMeta(rc, username, func(m *UserMeta) bool {
		m.Quota = size
		if kib == 0 {
			m.Quota = ""
		}
		return true
	}); err != nil {
		return err
	}
	if kib == 0 {
		fmt.Printf("quota removed for %s\n", username)
	} else {
		fmt.Printf("quota for %s set to %s on %s (%s)\n", username, size, mnt.Point, mnt.FSType)
	}
	return nil
}

// ShowQuotas prints the quota report for the home_base filesystem.
func ShowQuotas(ctx context.Context, rc *RunContext) error {
	homeBase := rc.Config.Users.HomeBase
	if homeBase == "" {
		homeBase = "/home"
	}
	mnt, err := findMount(rc, homeBase)
	if err != nil {
		return err
	}
	var out string
	switch mnt.FSType {
	case "xfs":
		r, err := rc.Runner.Query(ctx, "xfs_quota", "-x", "-c", "report -p -h", mnt.Point)
		if err != nil {
			return fmt.Errorf("xfs_quota report: %w", err)
		}
		out = r.Stdout
	case "ext4", "ext3":
		r, err := rc.Runner.Query(ctx, "repquota", "-s", mnt.Point)
		if err != nil {
			return fmt.Errorf("repquota: %w", err)
		}
		out = r.Stdout
	default:
		return fmt.Errorf("quotas are supported on xfs and ext4; %s is %s", mnt.Point, mnt.FSType)
	}
	fmt.Print(out)
	return nil
}

// reapplyQuota restores a recorded quota after a user is recreated.
func reapplyQuota(ctx context.Context, rc *RunContext, m UserMeta) {
	if m.Quota == "" {
		return
	}
	if _, err := lookupAccount(m.Name); err != nil {
		return
	}
	if err := SetQuota(ctx, rc, m.Name, m.Quota); err != nil {
		fmt.Printf("  ⚠ %s: quota %s not restored: %v\n", m.Name, m.Quota, err)
	}
}
