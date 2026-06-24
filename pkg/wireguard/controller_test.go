// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

package wireguard

import (
	"sync"
	"testing"
)

func TestControllerGetPeerVPNIPFound(t *testing.T) {
	t.Parallel()

	controller := &Controller{
		Peers: map[string]PeerState{
			"peer-1": {VPNIP: "100.97.0.2"},
		},
	}

	vpnIP, ok := controller.GetPeerVPNIP("peer-1")
	if !ok {
		t.Fatal("expected peer to be found")
	}
	if vpnIP != "100.97.0.2" {
		t.Fatalf("expected VPN IP 100.97.0.2, got %q", vpnIP)
	}
}

func TestControllerGetPeerVPNIPNotFound(t *testing.T) {
	t.Parallel()

	controller := &Controller{Peers: map[string]PeerState{}}

	vpnIP, ok := controller.GetPeerVPNIP("missing")
	if ok {
		t.Fatal("expected missing peer")
	}
	if vpnIP != "" {
		t.Fatalf("expected empty VPN IP, got %q", vpnIP)
	}
}

func TestControllerGetPeerVPNIPConcurrentReadSafety(t *testing.T) {
	t.Parallel()

	controller := &Controller{
		Peers: map[string]PeerState{
			"peer-1": {VPNIP: "100.97.0.2"},
		},
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if j%2 == 0 {
					controller.PeersMutex.Lock()
					controller.Peers["peer-1"] = PeerState{VPNIP: "100.97.0.2"}
					controller.PeersMutex.Unlock()
				}

				if _, ok := controller.GetPeerVPNIP("peer-1"); !ok {
					t.Errorf("reader %d failed to find peer", idx)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}
