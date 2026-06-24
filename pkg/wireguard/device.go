// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2025 OpenCHAMI Contributors

package wireguard

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// DeviceAPI abstracts wireguard-go operations for testing and controller use.
type DeviceAPI interface {
	SetPrivateKey(privateKey string) error
	AddPeer(publicKey, allowedIP string) error
	RemovePeer(publicKey string) error
	Close() error
	PublicKeyValue() string
	SetPublicKeyValue(pub string)
	ListenPortValue() int
	PrivateKeyValue() (string, error)
}

// Device wraps wireguard-go for userspace operation
type Device struct {
	TUN        tun.Device
	WG         *device.Device
	Logger     *device.Logger
	listenPort int
	privateKey string
	publicKey  string
}

// NewDevice creates a userspace WireGuard device bound to a TUN interface and configures listen port.
func NewDevice(name string, listenPort int) (*Device, error) {
	tunDev, err := tun.CreateTUN(name, device.DefaultMTU)
	if err != nil {
		return nil, fmt.Errorf("failed to create TUN device: %w", err)
	}
	realName, err := tunDev.Name()
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("failed to get TUN name: %w", err)
	}

	// Use verbose logging level for development; adjust in production as needed
	logger := device.NewLogger(0, fmt.Sprintf("[%s] ", realName))
	wgDev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)

	// Configure listen port; private key will be set by controller
	cfg := fmt.Sprintf("listen_port=%d\n", listenPort)
	if err := wgDev.IpcSet(cfg); err != nil {
		wgDev.Close()
		return nil, fmt.Errorf("failed to configure device: %w", err)
	}
	if err := wgDev.Up(); err != nil {
		wgDev.Close()
		return nil, fmt.Errorf("failed to bring up device: %w", err)
	}

	return &Device{
		TUN:        tunDev,
		WG:         wgDev,
		Logger:     logger,
		listenPort: listenPort,
	}, nil
}

// SetPrivateKey configures the device private key via IPC.
// wireguard-go's IpcSet expects hex-encoded keys.
func (d *Device) SetPrivateKey(privateKey string) error {
	d.privateKey = privateKey
	raw, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		return fmt.Errorf("decode private key: %w", err)
	}
	cfg := fmt.Sprintf("private_key=%s\n", hex.EncodeToString(raw))
	return d.WG.IpcSet(cfg)
}

// PrivateKeyValue returns the currently configured private key.
func (d *Device) PrivateKeyValue() (string, error) {
	return d.privateKey, nil
}

// AddPeer adds or updates a peer with an allowed IP and keepalive.
// wireguard-go's IpcSet expects hex-encoded keys.
func (d *Device) AddPeer(publicKey, allowedIP string) error {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	cfg := fmt.Sprintf("public_key=%s\nallowed_ip=%s\npersistent_keepalive_interval=25\n", hex.EncodeToString(raw), allowedIP)
	return d.WG.IpcSet(cfg)
}

// RemovePeer removes a peer by public key.
// wireguard-go's IpcSet expects hex-encoded keys.
func (d *Device) RemovePeer(publicKey string) error {
	raw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	cfg := fmt.Sprintf("public_key=%s\nremove=true\n", hex.EncodeToString(raw))
	return d.WG.IpcSet(cfg)
}

// PublicKeyValue returns the currently configured public key.
func (d *Device) PublicKeyValue() string { return d.publicKey }

// SetPublicKeyValue sets the cached public key value (after derivation).
func (d *Device) SetPublicKeyValue(pub string) { d.publicKey = pub }

// ListenPortValue returns the configured listen port.
func (d *Device) ListenPortValue() int { return d.listenPort }

// Close closes the device and TUN interface.
func (d *Device) Close() error {
	downErr := d.WG.Down()
	d.WG.Close()
	tunErr := d.TUN.Close()
	if downErr != nil {
		return downErr
	}
	return tunErr
}
