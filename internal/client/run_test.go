package client

import (
	"reflect"
	"testing"

	"github.com/asbassan/barge/internal/network"
)

func TestSplitWindowsVolume(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		// Standard Linux-style paths (no drive letters)
		{"src:dst", []string{"src", "dst"}},
		{"src:dst:ro", []string{"src", "dst", "ro"}},

		// Windows drive letter on source
		{`C:\host:C:\container`, []string{`C:\host`, `C:\container`}},
		{`C:\host:C:\container:ro`, []string{`C:\host`, `C:\container`, "ro"}},

		// Forward-slash Windows paths
		{`C:/host:C:/container`, []string{`C:/host`, `C:/container`}},

		// Drive letter only on source
		{`C:\src:/dst`, []string{`C:\src`, "/dst"}},
	}

	for _, tc := range cases {
		got := splitWindowsVolume(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitWindowsVolume(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseMappedDirectories(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		wantLen int
		wantRO  bool
		wantErr bool
	}{
		{"simple", []string{`C:\src:C:\dst`}, 1, false, false},
		{"readonly", []string{`C:\src:C:\dst:ro`}, 1, true, false},
		{"multiple", []string{`C:\a:C:\b`, `C:\c:C:\d`}, 2, false, false},
		{"invalid option", []string{`C:\src:C:\dst:rw`}, 0, false, true},
		{"too few parts", []string{`C:\only`}, 0, false, true},
		{"too many parts", []string{`a:b:c:d`}, 0, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, err := parseMappedDirectories(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(dirs) != tc.wantLen {
				t.Errorf("got %d dirs, want %d", len(dirs), tc.wantLen)
			}
			if tc.wantLen > 0 && dirs[0].Readonly != tc.wantRO {
				t.Errorf("readonly: got %v, want %v", dirs[0].Readonly, tc.wantRO)
			}
		})
	}
}

func TestParsePortMapping(t *testing.T) {
	cases := []struct {
		in      string
		want    network.PortMapping
		wantErr bool
	}{
		{"8080:80", network.PortMapping{HostPort: 8080, ContainerPort: 80, Proto: "tcp"}, false},
		{"443:443/tcp", network.PortMapping{HostPort: 443, ContainerPort: 443, Proto: "tcp"}, false},
		{"53:53/udp", network.PortMapping{HostPort: 53, ContainerPort: 53, Proto: "udp"}, false},
		{"notaport:80", network.PortMapping{}, true},
		{"8080:notaport", network.PortMapping{}, true},
		{"nocolon", network.PortMapping{}, true},
	}

	for _, tc := range cases {
		got, err := network.ParsePortMapping(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePortMapping(%q): expected error, got nil", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePortMapping(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParsePortMapping(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}
