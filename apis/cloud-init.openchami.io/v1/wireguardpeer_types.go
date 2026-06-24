// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2025 OpenCHAMI Contributors

package v1

import (
	"context"
	"fmt"
	"net"

	"github.com/openchami/fabrica/pkg/fabrica"
)

// WireGuardPeer defines a userspace WireGuard peer managed via Fabrica (hub/storage version).
type WireGuardPeer struct {
	APIVersion string              `json:"apiVersion" yaml:"apiVersion"`
	Kind       string              `json:"kind" yaml:"kind"`
	Metadata   fabrica.Metadata    `json:"metadata" yaml:"metadata"`
	Spec       WireGuardPeerSpec   `json:"spec" yaml:"spec"`
	Status     WireGuardPeerStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

// WireGuardPeerSpec captures desired peer configuration.
type WireGuardPeerSpec struct { //nolint: revive
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	PublicKey   string `json:"public_key" yaml:"public_key" validate:"required"`
	AllowedIP   string `json:"allowed_ip" yaml:"allowed_ip" validate:"required"`
}

// WireGuardPeerStatus reports reconciliation outcome.
type WireGuardPeerStatus struct { //nolint: revive
	Phase   string `json:"phase,omitempty" yaml:"phase,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
	Ready   bool   `json:"ready,omitempty" yaml:"ready,omitempty"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
}

// IsHub marks this as the hub/storage version.
func (WireGuardPeer) IsHub() {}

// Validate performs lightweight validation; reconciliation into the controller occurs elsewhere.
func (r *WireGuardPeer) Validate(ctx context.Context) error { //nolint: revive
	if r.Spec.AllowedIP == "" {
		return fmt.Errorf("allowed_ip is required (CIDR)")
	}
	if r.Spec.PublicKey == "" {
		return fmt.Errorf("public_key is required")
	}
	if _, _, err := net.ParseCIDR(r.Spec.AllowedIP); err != nil {
		return fmt.Errorf("allowed_ip must be a valid CIDR: %w", err)
	}
	return nil
}

// GetKind returns the kind of the resource.
func (r *WireGuardPeer) GetKind() string { //nolint: revive
	return "WireGuardPeer"
}

// GetName returns the name of the resource.
func (r *WireGuardPeer) GetName() string { //nolint: revive
	return r.Metadata.Name
}

// GetUID returns the UID of the resource.
func (r *WireGuardPeer) GetUID() string { //nolint: revive
	return r.Metadata.UID
}
