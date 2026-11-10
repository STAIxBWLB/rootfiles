package cli

import (
	"strings"
	"testing"
)

func TestTenantCpusetFlagErrorsBeforeHostAccess(t *testing.T) {
	for _, args := range [][]string{nil, {"--clear", "--cpus", "0"}, {"--clear", "--mems", ""}} {
		cmd := newTenantCpusetCmd()
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		err := cmd.RunE(cmd, []string{"lab"})
		if err == nil || (!strings.Contains(err.Error(), "pass --cpus") && !strings.Contains(err.Error(), "use --clear alone")) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestTenantCpusetCreateAndCommandFlags(t *testing.T) {
	create := newTenantCreateCmd()
	cpuset := newTenantCpusetCmd()
	for _, flag := range []string{"cpus", "mems"} {
		if create.Flags().Lookup(flag) == nil || cpuset.Flags().Lookup(flag) == nil {
			t.Fatalf("missing %s", flag)
		}
	}
	if !strings.Contains(cpuset.Long, "migrates existing memory pages") {
		t.Fatal("help omits memory migration")
	}
}
