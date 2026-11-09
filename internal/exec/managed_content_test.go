package exec

import "testing"

func TestManagedContentEqual(t *testing.T) {
	for _, tc := range []struct {
		name, got, want string
		equal           bool
	}{
		{"hash header", "# Managed by rootfiles-v2\nPort 22\n", "# Managed by rootfiles\nPort 22\n", true},
		{"apt comment", "// Managed by rootfiles-v2\nvalue;\n", "// Managed by rootfiles\nvalue;\n", true},
		{"shebang", "#!/bin/bash\n# Managed by rootfiles-v2 — do not edit\necho safe\n", "#!/bin/bash\n# Managed by rootfiles — do not edit\necho safe\n", true},
		{"colon suffix", "# Managed by rootfiles-v2: purpose\n", "# Managed by rootfiles: purpose\n", true},
		{"header only", "# Managed by rootfiles-v2", "# Managed by rootfiles", true},
		{"payload drift", "# Managed by rootfiles-v2\nPort 22\n", "# Managed by rootfiles\nPort 2222\n", false},
		{"suffix drift", "# Managed by rootfiles-v2 old\n", "# Managed by rootfiles new\n", false},
		{"near prefix", "# Managed by rootfiles-v2x\n", "# Managed by rootfilesx\n", false},
		{"body marker", "first\n# Managed by rootfiles-v2\n", "first\n# Managed by rootfiles\n", false},
		{"credential", "TUNNEL_TOKEN='# Managed by rootfiles-v2'\n", "TUNNEL_TOKEN='# Managed by rootfiles'\n", false},
		{"shebang only", "#!/bin/sh", "#!/bin/bash", false},
		{"identical", "arbitrary", "arbitrary", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if equal := ManagedContentEqual(tc.got, tc.want); equal != tc.equal {
				t.Fatalf("got %t, want %t", equal, tc.equal)
			}
			if equal := ManagedContentEqual(tc.want, tc.got); equal != tc.equal {
				t.Fatalf("reverse got %t, want %t", equal, tc.equal)
			}
		})
	}
}
