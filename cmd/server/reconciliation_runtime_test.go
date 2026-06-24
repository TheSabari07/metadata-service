// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/OpenCHAMI/metadata-service/apis/cloud-init.openchami.io/v1"
	"github.com/OpenCHAMI/metadata-service/internal/storage"
	"github.com/OpenCHAMI/metadata-service/pkg/wireguard"
	"github.com/go-chi/chi/v5"
	"github.com/openchami/fabrica/pkg/events"
	"github.com/openchami/fabrica/pkg/fabrica"
)

func TestReconciliationRuntimeStartsAndStops(t *testing.T) {
	if err := storage.InitFileBackend(t.TempDir()); err != nil {
		t.Fatalf("InitFileBackend() failed: %v", err)
	}

	runtime, err := initializeReconciliationRuntime(nil)
	if err != nil {
		t.Fatalf("initializeReconciliationRuntime() failed: %v", err)
	}

	if err := runtime.Stop(); err != nil {
		t.Fatalf("runtime.Stop() failed: %v", err)
	}
	// Stop should be idempotent.
	if err := runtime.Stop(); err != nil {
		t.Fatalf("runtime.Stop() second call failed: %v", err)
	}
}

func TestReconciliationRuntimeReconcilesExistingWireGuardPeersWithoutController(t *testing.T) {
	if err := storage.InitFileBackend(t.TempDir()); err != nil {
		t.Fatalf("InitFileBackend() failed: %v", err)
	}

	now := time.Now().UTC()
	peer := &v1.WireGuardPeer{
		APIVersion: "v1",
		Kind:       "WireGuardPeer",
		Metadata: fabrica.Metadata{
			UID:       "wireguardpeer-existing",
			Name:      "existing",
			CreatedAt: now,
			UpdatedAt: now,
		},
		Spec: v1.WireGuardPeerSpec{
			PublicKey: "8du8A89mlo7m1r8q4ScfGn6Af8Vx8gfX3E2qhW2C5VQ=",
			AllowedIP: "100.97.0.9/32",
		},
	}
	if err := storage.SaveWireGuardPeer(context.Background(), peer); err != nil {
		t.Fatalf("SaveWireGuardPeer() failed: %v", err)
	}

	runtime, err := initializeReconciliationRuntime(nil)
	if err != nil {
		t.Fatalf("initializeReconciliationRuntime() failed: %v", err)
	}
	defer func() {
		if stopErr := runtime.Stop(); stopErr != nil {
			t.Fatalf("runtime.Stop() failed: %v", stopErr)
		}
	}()

	updated, err := storage.LoadWireGuardPeer(context.Background(), peer.Metadata.UID)
	if err != nil {
		t.Fatalf("LoadWireGuardPeer() failed: %v", err)
	}
	if updated.Status.Phase != "Degraded" {
		t.Fatalf("expected degraded phase, got %q", updated.Status.Phase)
	}
	if updated.Status.Ready {
		t.Fatalf("expected non-ready status, got %+v", updated.Status)
	}
}

func TestRunServerFailsWhenReconciliationRuntimeInitFails(t *testing.T) {
	origReconcileFn := newReconciliationRuntimeFn
	origRegisterFn := registerServerIntegrations
	newReconciliationRuntimeFn = func(_ *wireguard.Controller) (*reconciliationRuntime, error) {
		return nil, errors.New("forced runtime failure")
	}
	registerServerIntegrations = func(_ context.Context, _ chi.Router) error { return nil }
	defer func() {
		newReconciliationRuntimeFn = origReconcileFn
		registerServerIntegrations = origRegisterFn
		events.SetGlobalEventBus(nil)
	}()

	config = DefaultConfig()
	config.DataDir = t.TempDir()
	config.Host = "127.0.0.1"
	config.Port = 0

	err := runServer(nil, nil)
	if err == nil {
		t.Fatal("expected runServer to fail")
	}
	if !strings.Contains(err.Error(), "failed to initialize reconciliation runtime") {
		t.Fatalf("expected reconciliation runtime failure, got %v", err)
	}
}
