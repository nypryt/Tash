package tailscale

import "testing"

func TestPeerDisplayName(t *testing.T) {
	cases := []struct {
		name string
		peer Peer
		want string
	}{
		{"combined", Peer{TailscaleName: "hp", Name: "DESKTOP-TEE67"}, "hp (DESKTOP-TEE67)"},
		{"same", Peer{TailscaleName: "hp", Name: "hp"}, "hp"},
		{"real only", Peer{Name: "DESKTOP-TEE67"}, "DESKTOP-TEE67"},
		{"tailscale only", Peer{TailscaleName: "hp"}, "hp"},
		{"empty", Peer{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.peer.DisplayName(); got != tc.want {
				t.Fatalf("DisplayName() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConvertPeerDerivesTailscaleName(t *testing.T) {
	peer := convertPeer(rawPeer{
		HostName: "DESKTOP-TEE67",
		DNSName:  "hp.tailnet-example.ts.net.",
	})
	if peer.TailscaleName != "hp" {
		t.Fatalf("TailscaleName = %q", peer.TailscaleName)
	}
	if peer.DNSName != "hp.tailnet-example.ts.net" {
		t.Fatalf("DNSName = %q", peer.DNSName)
	}
	if peer.DisplayName() != "hp (DESKTOP-TEE67)" {
		t.Fatalf("DisplayName = %q", peer.DisplayName())
	}
}
