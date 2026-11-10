package config

import "testing"

func TestUsersConfigBasePreservesExplicitValue(t *testing.T) {
	for _, tc := range []struct{ home, want string }{
		{"", "/home"},
		{"/home", "/home"},
		{"/raid/home", "/raid/home"},
		{"/srv/users/../home/", "/srv/users/../home/"},
		{"relative/home", "relative/home"},
		{" ", " "},
		{"/srv/한라/users/", "/srv/한라/users/"},
	} {
		t.Run(tc.home, func(t *testing.T) {
			users := UsersConfig{HomeBase: tc.home}
			if got := users.Base(); got != tc.want {
				t.Fatalf("Base()=%q, want %q", got, tc.want)
			}
			if users.HomeBase != tc.home {
				t.Fatalf("Base changed the configured value: %q", users.HomeBase)
			}
		})
	}
}
