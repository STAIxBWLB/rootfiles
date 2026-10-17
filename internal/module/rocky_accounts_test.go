package module

import (
	"errors"
	"os/user"
	"reflect"
	"testing"

	"github.com/STAIxBWLB/rootfiles/internal/config"
)

func TestAccountGroupsForSystemUsesNativeAdminGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		system *config.SystemInfo
		input  []string
		want   []string
	}{
		{
			name:   "Rocky 8.9",
			system: &config.SystemInfo{OS: "rocky", Version: "8.9"},
			input:  []string{"sudo", "docker", "wheel", "docker"},
			want:   []string{"wheel", "docker"},
		},
		{
			name:   "Rocky 8.10",
			system: &config.SystemInfo{OS: "rocky", Version: "8.10"},
			input:  []string{"sudo", "research"},
			want:   []string{"wheel", "research"},
		},
		{
			name:   "Rocky 9",
			system: &config.SystemInfo{OS: "rocky", Version: "9.6"},
			input:  []string{"sudo", "research"},
			want:   []string{"wheel", "research"},
		},
		{
			name:   "Ubuntu",
			system: &config.SystemInfo{OS: "ubuntu", Version: "24.04"},
			input:  []string{"sudo", "docker", "sudo"},
			want:   []string{"sudo", "docker"},
		},
		{
			name:   "unsupported system fallback",
			system: &config.SystemInfo{OS: "other", Version: "1"},
			input:  []string{"sudo", "research"},
			want:   []string{"sudo", "research"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.input...)
			if got := accountGroupsForSystem(tc.system, tc.input); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("accountGroupsForSystem() = %v, want %v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.input, before) {
				t.Fatalf("input groups were modified: got %v, want %v", tc.input, before)
			}
		})
	}
}

func TestRequireNativeAdminGroup(t *testing.T) {
	old := lookupAccountGroup
	t.Cleanup(func() { lookupAccountGroup = old })

	tests := []struct {
		name       string
		system     *config.SystemInfo
		groups     []string
		lookupErr  error
		wantLookup string
		wantErr    bool
	}{
		{
			name:       "Rocky sudo intent requires wheel",
			system:     &config.SystemInfo{OS: "rocky", Version: "8.9"},
			groups:     []string{"wheel"},
			wantLookup: "wheel",
		},
		{
			name:    "non-admin groups need no admin group",
			system:  &config.SystemInfo{OS: "rocky", Version: "8.10"},
			groups:  []string{"research", "docker"},
			wantErr: false,
		},
		{
			name:       "missing Rocky wheel is an error",
			system:     &config.SystemInfo{OS: "rocky", Version: "9.6"},
			groups:     []string{"wheel"},
			lookupErr:  errors.New("group not found"),
			wantLookup: "wheel",
			wantErr:    true,
		},
		{
			name:       "Ubuntu keeps sudo",
			system:     &config.SystemInfo{OS: "ubuntu", Version: "24.04"},
			groups:     []string{"sudo"},
			wantLookup: "sudo",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotLookup := ""
			lookupAccountGroup = func(name string) (*user.Group, error) {
				gotLookup = name
				if tc.lookupErr != nil {
					return nil, tc.lookupErr
				}
				return &user.Group{Name: name, Gid: "10"}, nil
			}
			err := requireNativeAdminGroup(tc.system, tc.groups)
			if (err != nil) != tc.wantErr {
				t.Fatalf("requireNativeAdminGroup() error = %v, wantErr %v", err, tc.wantErr)
			}
			if gotLookup != tc.wantLookup {
				t.Fatalf("looked up group %q, want %q", gotLookup, tc.wantLookup)
			}
		})
	}
}
