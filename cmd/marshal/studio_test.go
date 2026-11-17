package main

import "testing"

// TestDaemonStudioAddr pins what daemon.studio_addr accepts. The daemon runs
// host processes with the user's own authority and authenticates the browser
// only with a token file, so an address that is not loopback must be refused
// at install time rather than published.
func TestDaemonStudioAddr(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting string
		want    string
		wantErr bool
	}{
		{name: "default", setting: "127.0.0.1:7788", want: "127.0.0.1:7788"},
		{name: "another loopback port", setting: "127.0.0.1:9000", want: "127.0.0.1:9000"},
		{name: "ipv6 loopback", setting: "[::1]:7788", want: "[::1]:7788"},
		{name: "hostname loopback", setting: "localhost:7788", want: "localhost:7788"},
		{name: "unset serves nothing", setting: "", want: ""},
		{name: "off serves nothing", setting: "off", want: ""},
		{name: "none serves nothing", setting: "none", want: ""},
		{name: "padded off", setting: "  off  ", want: ""},
		{name: "wildcard is refused", setting: "0.0.0.0:7788", wantErr: true},
		{name: "routable address is refused", setting: "192.168.1.5:7788", wantErr: true},
		{name: "blank host is refused", setting: ":7788", wantErr: true},
		{name: "no port is refused", setting: "127.0.0.1", wantErr: true},
		{name: "nonsense is refused", setting: "not an address", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := daemonStudioAddr(tc.setting)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("daemonStudioAddr(%q) = %q, want an error", tc.setting, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("daemonStudioAddr(%q): %v", tc.setting, err)
			}
			if got != tc.want {
				t.Errorf("daemonStudioAddr(%q) = %q, want %q", tc.setting, got, tc.want)
			}
		})
	}
}
