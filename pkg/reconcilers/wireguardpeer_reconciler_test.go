// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

package reconcilers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	v1 "github.com/OpenCHAMI/metadata-service/apis/cloud-init.openchami.io/v1"
	"github.com/openchami/fabrica/pkg/fabrica"
)

type mockReconcileClient struct {
	mu    sync.RWMutex
	peers map[string]*v1.WireGuardPeer
}

func newMockReconcileClient(peers ...*v1.WireGuardPeer) *mockReconcileClient {
	data := make(map[string]*v1.WireGuardPeer, len(peers))
	for _, peer := range peers {
		data[peer.Metadata.UID] = clonePeer(peer)
	}
	return &mockReconcileClient{peers: data}
}

func (m *mockReconcileClient) Get(_ context.Context, kind, uid string) (interface{}, error) {
	if kind != "WireGuardPeer" {
		return nil, fmt.Errorf("unsupported kind %s", kind)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	peer, ok := m.peers[uid]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return clonePeer(peer), nil
}

func (m *mockReconcileClient) List(_ context.Context, kind string) ([]interface{}, error) {
	if kind != "WireGuardPeer" {
		return nil, fmt.Errorf("unsupported kind %s", kind)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]interface{}, 0, len(m.peers))
	for _, peer := range m.peers {
		items = append(items, clonePeer(peer))
	}
	return items, nil
}

func (m *mockReconcileClient) Update(_ context.Context, resource interface{}) error {
	peer, ok := resource.(*v1.WireGuardPeer)
	if !ok {
		return fmt.Errorf("unsupported resource type %T", resource)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.peers[peer.Metadata.UID] = clonePeer(peer)
	return nil
}

func (m *mockReconcileClient) Create(ctx context.Context, resource interface{}) error {
	return m.Update(ctx, resource)
}

func (m *mockReconcileClient) Delete(_ context.Context, kind, uid string) error {
	if kind != "WireGuardPeer" {
		return fmt.Errorf("unsupported kind %s", kind)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.peers, uid)
	return nil
}

type mockWireGuardController struct {
	upsertCalls []upsertCall
	removeCalls []string
	upsertErr   error
	removeErr   error
}

type upsertCall struct {
	peerID    string
	publicKey string
	allowedIP string
}

func (m *mockWireGuardController) UpsertPeer(peerID, publicKey, allowedIP string) error {
	m.upsertCalls = append(m.upsertCalls, upsertCall{peerID: peerID, publicKey: publicKey, allowedIP: allowedIP})
	return m.upsertErr
}

func (m *mockWireGuardController) RemovePeerByID(peerID string) error {
	m.removeCalls = append(m.removeCalls, peerID)
	return m.removeErr
}

func TestWireGuardPeerReconcilerReconcileUpsertSuccess(t *testing.T) {
	t.Parallel()

	peer := newWireGuardPeerFixture("wireguardpeer-a", "8du8A89mlo7m1r8q4ScfGn6Af8Vx8gfX3E2qhW2C5VQ=", "100.97.0.2/32")
	client := newMockReconcileClient(peer)
	controller := &mockWireGuardController{}
	reconciler := NewWireGuardPeerReconciler(client, controller)

	if _, err := reconciler.Reconcile(context.Background(), peer); err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	if len(controller.upsertCalls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(controller.upsertCalls))
	}
	if controller.upsertCalls[0].peerID != peer.Metadata.UID {
		t.Fatalf("expected upsert peerID %q, got %q", peer.Metadata.UID, controller.upsertCalls[0].peerID)
	}

	storedRaw, err := client.Get(context.Background(), "WireGuardPeer", peer.Metadata.UID)
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	stored := storedRaw.(*v1.WireGuardPeer)
	if stored.Status.Phase != wireGuardPeerPhaseReady {
		t.Fatalf("expected status phase %q, got %q", wireGuardPeerPhaseReady, stored.Status.Phase)
	}
	if !stored.Status.Ready {
		t.Fatalf("expected ready status, got %+v", stored.Status)
	}
}

func TestWireGuardPeerReconcilerReconcileDegradedWhenControllerMissing(t *testing.T) {
	t.Parallel()

	peer := newWireGuardPeerFixture("wireguardpeer-b", "8du8A89mlo7m1r8q4ScfGn6Af8Vx8gfX3E2qhW2C5VQ=", "100.97.0.3/32")
	client := newMockReconcileClient(peer)
	reconciler := NewWireGuardPeerReconciler(client, nil)

	if _, err := reconciler.Reconcile(context.Background(), peer); err != nil {
		t.Fatalf("Reconcile() returned error: %v", err)
	}

	storedRaw, err := client.Get(context.Background(), "WireGuardPeer", peer.Metadata.UID)
	if err != nil {
		t.Fatalf("Get() failed: %v", err)
	}
	stored := storedRaw.(*v1.WireGuardPeer)
	if stored.Status.Phase != wireGuardPeerPhaseDegraded {
		t.Fatalf("expected status phase %q, got %q", wireGuardPeerPhaseDegraded, stored.Status.Phase)
	}
	if stored.Status.Ready {
		t.Fatalf("expected non-ready status, got %+v", stored.Status)
	}
}

func TestWireGuardPeerReconcilerDeleteRemovesPeer(t *testing.T) {
	t.Parallel()

	controller := &mockWireGuardController{}
	reconciler := NewWireGuardPeerReconciler(newMockReconcileClient(), controller)

	if err := reconciler.Delete(context.Background(), "wireguardpeer-delete"); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	if len(controller.removeCalls) != 1 {
		t.Fatalf("expected one remove call, got %d", len(controller.removeCalls))
	}
	if controller.removeCalls[0] != "wireguardpeer-delete" {
		t.Fatalf("expected remove call for wireguardpeer-delete, got %q", controller.removeCalls[0])
	}
}

func TestWireGuardPeerReconcilerDeleteToleratesMissingPeer(t *testing.T) {
	t.Parallel()

	controller := &mockWireGuardController{removeErr: fmt.Errorf("peer not found: wireguardpeer-missing")}
	reconciler := NewWireGuardPeerReconciler(newMockReconcileClient(), controller)

	if err := reconciler.Delete(context.Background(), "wireguardpeer-missing"); err != nil {
		t.Fatalf("expected nil error for missing peer, got %v", err)
	}
}

func newWireGuardPeerFixture(uid, publicKey, allowedIP string) *v1.WireGuardPeer {
	now := time.Now().UTC()
	return &v1.WireGuardPeer{
		APIVersion: "v1",
		Kind:       "WireGuardPeer",
		Metadata: fabrica.Metadata{
			UID:       uid,
			Name:      uid,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Spec: v1.WireGuardPeerSpec{
			PublicKey: publicKey,
			AllowedIP: allowedIP,
		},
	}
}

func clonePeer(peer *v1.WireGuardPeer) *v1.WireGuardPeer {
	data, _ := json.Marshal(peer)
	var out v1.WireGuardPeer
	_ = json.Unmarshal(data, &out)
	return &out
}
