// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

package v1

import (
	"context"
	"strings"
	"testing"
)

func TestWireGuardPeerValidateRequiresPublicKey(t *testing.T) {
	t.Parallel()

	peer := &WireGuardPeer{Spec: WireGuardPeerSpec{AllowedIP: "100.97.0.2/32"}}

	err := peer.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error for missing public_key")
	}
	if !strings.Contains(err.Error(), "public_key") {
		t.Fatalf("expected public_key validation error, got %v", err)
	}
}

func TestWireGuardPeerValidateRequiresAllowedIP(t *testing.T) {
	t.Parallel()

	peer := &WireGuardPeer{Spec: WireGuardPeerSpec{PublicKey: "Z4bcfQ4n7oqj6jAQtcdx0wTzvY4oF48a0H93lkQ8l3M="}}

	err := peer.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error for missing allowed_ip")
	}
	if !strings.Contains(err.Error(), "allowed_ip") {
		t.Fatalf("expected allowed_ip validation error, got %v", err)
	}
}

func TestWireGuardPeerValidateRejectsInvalidCIDR(t *testing.T) {
	t.Parallel()

	peer := &WireGuardPeer{Spec: WireGuardPeerSpec{
		PublicKey: "Z4bcfQ4n7oqj6jAQtcdx0wTzvY4oF48a0H93lkQ8l3M=",
		AllowedIP: "100.97.0.2",
	}}

	err := peer.Validate(context.Background())
	if err == nil {
		t.Fatal("expected validation error for invalid CIDR")
	}
	if !strings.Contains(err.Error(), "valid CIDR") {
		t.Fatalf("expected CIDR validation error, got %v", err)
	}
}

func TestWireGuardPeerValidateAcceptsValidCIDR(t *testing.T) {
	t.Parallel()

	peer := &WireGuardPeer{Spec: WireGuardPeerSpec{
		PublicKey: "Z4bcfQ4n7oqj6jAQtcdx0wTzvY4oF48a0H93lkQ8l3M=",
		AllowedIP: "100.97.0.2/32",
	}}

	if err := peer.Validate(context.Background()); err != nil {
		t.Fatalf("expected validation success, got %v", err)
	}
}
