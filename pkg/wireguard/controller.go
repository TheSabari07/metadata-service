// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2025 OpenCHAMI Contributors

package wireguard

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"sync"

	"golang.org/x/crypto/curve25519"
)

type contextKey string

// ControllerContextKey tags request contexts with an attached WireGuard controller.
const ControllerContextKey contextKey = "wireguard-controller"

// Controller manages peer lifecycle with userspace device
// Controller manages peers, allocations, and device configuration.
type Controller struct {
	Device      DeviceAPI
	Network     net.IPNet
	ServerIP    net.IP
	Peers       map[string]PeerState // key: peer identifier (clientIP or resource UID)
	PeersMutex  sync.RWMutex
	IPAllocator *IPAllocator
	Persistence *Persistence
}

// PeerState tracks the configured state for a peer.
type PeerState struct {
	PublicKey string
	VPNIP     string
	ClientIP  string
	AllowedIP string
}

// NewController initializes a WireGuard controller with userspace device and optional persistence.
func NewController(interfaceName string, serverIP net.IP, network *net.IPNet, listenPort int, persistenceFile string) (*Controller, error) {
	dev, err := NewDevice(interfaceName, listenPort)
	if err != nil {
		return nil, fmt.Errorf("failed to create userspace device: %w", err)
	}

	allocator, err := NewIPAllocator(network.String())
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("failed to create IP allocator: %w", err)
	}
	if err := allocator.Reserve(net.IPAddr{IP: serverIP}); err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("failed to reserve server IP: %w", err)
	}

	var persistence *Persistence
	var privKey, pubKey string
	var peers map[string]PeerState

	// Initialize persistence layer
	if persistenceFile != "" {
		p, err := NewPersistence(persistenceFile)
		if err != nil {
			_ = dev.Close()
			return nil, fmt.Errorf("failed to initialize persistence: %w", err)
		}
		persistence = p

		// Try to load persisted state
		state, err := p.Load()
		if err != nil {
			fmt.Printf("Warning: failed to load persisted state: %v; regenerating\n", err)
		} else if state.ServerPrivateKey != "" {
			// Use persisted keys
			privKey = state.ServerPrivateKey
			pubKey = state.ServerPublicKey
			peers = reconstructPeersFromState(state, allocator)
		}
	}

	// Generate new keys if not loaded from persistence
	if privKey == "" {
		priv, pub, err := generateKeypair()
		if err != nil {
			_ = dev.Close()
			return nil, fmt.Errorf("failed to generate wireguard keypair: %w", err)
		}
		privKey = priv
		pubKey = pub
	}

	if err := dev.SetPrivateKey(privKey); err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("failed to set private key: %w", err)
	}
	dev.SetPublicKeyValue(pubKey)

	// Restore persisted peers to the device
	for _, peer := range peers {
		if err := dev.AddPeer(peer.PublicKey, peer.AllowedIP); err != nil {
			fmt.Printf("Warning: failed to restore peer %s: %v\n", peer.PublicKey, err)
		}
	}

	if peers == nil {
		peers = make(map[string]PeerState)
	}

	return &Controller{
		Device:      dev,
		Network:     *network,
		ServerIP:    serverIP,
		Peers:       peers,
		PeersMutex:  sync.RWMutex{},
		IPAllocator: allocator,
		Persistence: persistence,
	}, nil
}

// AddPeer allocates a VPN IP and configures a peer keyed by client IP.
func (c *Controller) AddPeer(clientIP, publicKey string) (string, error) {
	c.PeersMutex.Lock()
	defer c.PeersMutex.Unlock()

	if peer, ok := c.Peers[clientIP]; ok {
		return peer.VPNIP, nil
	}

	ip, err := c.IPAllocator.NextAvailable()
	if err != nil {
		return "", fmt.Errorf("failed to allocate VPN IP: %w", err)
	}
	vpnIP := ip.IP.String()

	if err := c.Device.AddPeer(publicKey, vpnIP+"/32"); err != nil {
		_ = c.IPAllocator.Release(ip)
		return "", fmt.Errorf("failed to add peer: %w", err)
	}

	c.Peers[clientIP] = PeerState{PublicKey: publicKey, VPNIP: vpnIP, ClientIP: clientIP}

	// Persist state after adding peer
	if c.Persistence != nil {
		if err := c.persistState(); err != nil {
			fmt.Printf("Warning: failed to persist controller state: %v\n", err)
		}
	}

	return vpnIP, nil
}

// UpsertPeer configures a peer using a provided allowed IP (CIDR) and identifier.
// This is used by the WireGuardPeer resource reconciliation path.
// UpsertPeer configures a peer keyed by an identifier with an explicit allowed CIDR.
func (c *Controller) UpsertPeer(peerID, publicKey, allowedIP string) error {
	c.PeersMutex.Lock()
	defer c.PeersMutex.Unlock()

	if peer, ok := c.Peers[peerID]; ok {
		// If unchanged, no-op
		if peer.PublicKey == publicKey && peer.AllowedIP == allowedIP {
			return nil
		}
		// Update existing peer in device
		if err := c.Device.AddPeer(publicKey, allowedIP); err != nil {
			return fmt.Errorf("failed to update peer: %w", err)
		}
		c.Peers[peerID] = PeerState{PublicKey: publicKey, AllowedIP: allowedIP, VPNIP: peer.VPNIP, ClientIP: peer.ClientIP}

		// Persist state after updating peer
		if c.Persistence != nil {
			if err := c.persistState(); err != nil {
				fmt.Printf("Warning: failed to persist controller state: %v\n", err)
			}
		}
		return nil
	}

	if err := c.Device.AddPeer(publicKey, allowedIP); err != nil {
		return fmt.Errorf("failed to add peer: %w", err)
	}
	// When provided allowedIP already includes mask, store IP portion for display if parseable
	ip, _, _ := net.ParseCIDR(allowedIP)
	vpn := ""
	if ip != nil {
		vpn = ip.String()
	}
	c.Peers[peerID] = PeerState{PublicKey: publicKey, AllowedIP: allowedIP, VPNIP: vpn, ClientIP: peerID}

	// Persist state after adding peer
	if c.Persistence != nil {
		if err := c.persistState(); err != nil {
			fmt.Printf("Warning: failed to persist controller state: %v\n", err)
		}
	}
	return nil
}

// RemovePeerByID removes a peer tracked by resource ID.
func (c *Controller) RemovePeerByID(peerID string) error {
	c.PeersMutex.Lock()
	defer c.PeersMutex.Unlock()
	peer, ok := c.Peers[peerID]
	if !ok {
		return fmt.Errorf("peer not found: %s", peerID)
	}
	if err := c.Device.RemovePeer(peer.PublicKey); err != nil {
		return fmt.Errorf("failed to remove peer: %w", err)
	}
	delete(c.Peers, peerID)

	// Persist state after removing peer
	if c.Persistence != nil {
		if err := c.persistState(); err != nil {
			fmt.Printf("Warning: failed to persist controller state: %v\n", err)
		}
	}
	return nil
}

// GetPeerVPNIP returns the peer VPN IP for the provided peer identifier.
func (c *Controller) GetPeerVPNIP(peerID string) (string, bool) {
	c.PeersMutex.RLock()
	defer c.PeersMutex.RUnlock()

	peer, ok := c.Peers[peerID]
	if !ok || peer.VPNIP == "" {
		return "", false
	}
	return peer.VPNIP, true
}

// RemovePeer removes a peer tracked by client IP and releases its allocation.
func (c *Controller) RemovePeer(clientIP string) error {
	c.PeersMutex.Lock()
	defer c.PeersMutex.Unlock()
	peer, ok := c.Peers[clientIP]
	if !ok {
		return fmt.Errorf("peer not found: %s", clientIP)
	}
	if err := c.Device.RemovePeer(peer.PublicKey); err != nil {
		return fmt.Errorf("failed to remove peer: %w", err)
	}
	_ = c.IPAllocator.Release(net.IPAddr{IP: net.ParseIP(peer.VPNIP)})
	delete(c.Peers, clientIP)

	// Persist state after removing peer
	if c.Persistence != nil {
		if err := c.persistState(); err != nil {
			fmt.Printf("Warning: failed to persist controller state: %v\n", err)
		}
	}
	return nil
}

// PublicKey returns the controller's server public key.
func (c *Controller) PublicKey() string { return c.Device.PublicKeyValue() }

// ListenPort returns the configured WireGuard listen port.
func (c *Controller) ListenPort() int { return c.Device.ListenPortValue() }

// ServerIPString returns the server IP as a string.
func (c *Controller) ServerIPString() string { return c.ServerIP.String() }

// generateKeypair creates base64-encoded private and public keys compatible with wireguard-go IPC
func generateKeypair() (private string, public string, err error) {
	// WireGuard keys are 32 bytes; encode base64
	privBytes := make([]byte, 32)
	if _, err = rand.Read(privBytes); err != nil {
		return "", "", err
	}
	private = base64.StdEncoding.EncodeToString(privBytes)
	public, err = derivePublicKey(private)
	return
}

// derivePublicKey computes the base64-encoded Curve25519 public key from a base64 private key.
func derivePublicKey(private string) (string, error) {
	privBytes, err := base64.StdEncoding.DecodeString(private)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	if len(privBytes) != 32 {
		return "", fmt.Errorf("private key must be 32 bytes, got %d", len(privBytes))
	}
	pubBytes, err := curve25519.X25519(privBytes, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pubBytes), nil
}

// persistState serializes the current controller state to the persistence layer.
// Must be called with PeersMutex held.
func (c *Controller) persistState() error {
	if c.Persistence == nil {
		return nil
	}

	privKey, err := c.Device.PrivateKeyValue()
	if err != nil {
		return fmt.Errorf("get private key: %w", err)
	}

	// Collect all peers for serialization
	peers := make([]PersistentPeerState, 0, len(c.Peers))
	for _, peer := range c.Peers {
		peers = append(peers, PersistentPeerState{
			PeerID:    peer.ClientIP,
			PublicKey: peer.PublicKey,
			VPNIP:     peer.VPNIP,
			ClientIP:  peer.ClientIP,
			AllowedIP: peer.AllowedIP,
		})
	}

	state := &ControllerState{
		Version:          "1",
		ServerPrivateKey: privKey,
		ServerPublicKey:  c.Device.PublicKeyValue(),
		// AllocatedIPs derived from peer VPNIPs for portability
		AllocatedIPs: collectAllocatedIPsFromPeers(peers),
		Peers:        peers,
	}

	return c.Persistence.Save(state)
}

// reconstructPeersFromState restores peers from persisted state.
// Skips peers that cannot be restored to the allocator.
func reconstructPeersFromState(state *ControllerState, allocator *IPAllocator) map[string]PeerState {
	peers := make(map[string]PeerState)
	for _, pp := range state.Peers {
		// Re-reserve the VPN IP if present
		if pp.VPNIP != "" {
			if err := allocator.Reserve(net.IPAddr{IP: net.ParseIP(pp.VPNIP)}); err != nil {
				fmt.Printf("Warning: failed to reserve VPN IP %s: %v\n", pp.VPNIP, err)
			}
		}
		peers[pp.PeerID] = PeerState{
			PublicKey: pp.PublicKey,
			VPNIP:     pp.VPNIP,
			ClientIP:  pp.ClientIP,
			AllowedIP: pp.AllowedIP,
		}
	}
	return peers
}

// collectAllocatedIPsFromPeers extracts VPN IPs from peer states.
func collectAllocatedIPsFromPeers(peers []PersistentPeerState) []string {
	ips := make([]string, 0, len(peers))
	for _, p := range peers {
		if p.VPNIP != "" {
			ips = append(ips, p.VPNIP)
		}
	}
	return ips
}
