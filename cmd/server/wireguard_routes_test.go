// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2025 OpenCHAMI Contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/OpenCHAMI/metadata-service/apis/cloud-init.openchami.io/v1"
	"github.com/OpenCHAMI/metadata-service/internal/storage"
	"github.com/OpenCHAMI/metadata-service/pkg/smdclient"
	"github.com/OpenCHAMI/metadata-service/pkg/wireguard"
	"github.com/go-chi/chi/v5"
	"github.com/openchami/fabrica/pkg/fabrica"
	fabricaStorage "github.com/openchami/fabrica/pkg/storage"
)

const (
	testPublicKeyA = "8du8A89mlo7m1r8q4ScfGn6Af8Vx8gfX3E2qhW2C5VQ="
	testPublicKeyB = "zT1x0V8x5NuV8dtY0Uw8D91fVfM6kKnI4V54CY4GWWA="
)

// Only test nil-controller and input validation paths to avoid real device setup.

func TestWGInitNoController(t *testing.T) {
	router := chi.NewRouter()
	registerWireGuardRoutes(router, nil, nil)

	body := bytes.NewBufferString(`{"public_key":"` + testPublicKeyA + `"}`)
	req := httptest.NewRequest("POST", "/wg-init", body)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestPhoneHomeNoController(t *testing.T) {
	router := chi.NewRouter()
	registerWireGuardRoutes(router, nil, nil)

	req := httptest.NewRequest("POST", "/phone-home/node1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestWireGuardPeerUIDFromPublicKeyDeterministic(t *testing.T) {
	t.Parallel()

	uidA1, err := wireGuardPeerUIDFromPublicKey(testPublicKeyA)
	if err != nil {
		t.Fatalf("wireGuardPeerUIDFromPublicKey() failed: %v", err)
	}
	uidA2, err := wireGuardPeerUIDFromPublicKey(testPublicKeyA)
	if err != nil {
		t.Fatalf("wireGuardPeerUIDFromPublicKey() failed: %v", err)
	}
	uidB, err := wireGuardPeerUIDFromPublicKey(testPublicKeyB)
	if err != nil {
		t.Fatalf("wireGuardPeerUIDFromPublicKey() failed: %v", err)
	}

	if uidA1 != uidA2 {
		t.Fatalf("expected same UID for same key, got %q and %q", uidA1, uidA2)
	}
	if uidA1 == uidB {
		t.Fatalf("expected different UIDs for different keys, got %q", uidA1)
	}
}

func TestWireGuardPeerUIDFromPublicKeyRejectsInvalidKey(t *testing.T) {
	t.Parallel()

	if _, err := wireGuardPeerUIDFromPublicKey("not-base64"); err == nil {
		t.Fatal("expected invalid public key error")
	}
}

func TestWGInitIdempotentForSamePublicKey(t *testing.T) {
	initTestStorageBackend(t)

	router := chi.NewRouter()
	controller := newTestWireGuardController(t)
	registerWireGuardRoutes(router, controller, nil)

	first := callWGInit(t, router, testPublicKeyA, "10.0.0.8:1234")
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for first wg-init call, got %d", first.Code)
	}
	var firstResp wgInitResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResp); err != nil {
		t.Fatalf("failed to decode first response: %v", err)
	}

	second := callWGInit(t, router, testPublicKeyA, "10.0.0.9:1234")
	if second.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for second wg-init call, got %d", second.Code)
	}
	var secondResp wgInitResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondResp); err != nil {
		t.Fatalf("failed to decode second response: %v", err)
	}

	if firstResp.PeerUID != secondResp.PeerUID {
		t.Fatalf("expected same peer UID, got %q and %q", firstResp.PeerUID, secondResp.PeerUID)
	}
	if firstResp.ClientVPNIP != secondResp.ClientVPNIP {
		t.Fatalf("expected same allocated VPN IP, got %q and %q", firstResp.ClientVPNIP, secondResp.ClientVPNIP)
	}
}

func TestWGInitPersistsWireGuardPeer(t *testing.T) {
	initTestStorageBackend(t)

	router := chi.NewRouter()
	controller := newTestWireGuardController(t)
	registerWireGuardRoutes(router, controller, nil)

	response := callWGInit(t, router, testPublicKeyB, "10.0.0.10:1234")
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for wg-init call, got %d", response.Code)
	}
	var payload wgInitResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	peer, err := storage.LoadWireGuardPeer(context.Background(), payload.PeerUID)
	if err != nil {
		t.Fatalf("LoadWireGuardPeer() failed: %v", err)
	}
	if peer.Spec.PublicKey != testPublicKeyB {
		t.Fatalf("expected stored public key %q, got %q", testPublicKeyB, peer.Spec.PublicKey)
	}
	if peer.Spec.AllowedIP != payload.ClientVPNIP+"/32" {
		t.Fatalf("expected allowed IP %q, got %q", payload.ClientVPNIP+"/32", peer.Spec.AllowedIP)
	}
}

func TestPhoneHomeDeletesByUIDAndIsIdempotent(t *testing.T) {
	initTestStorageBackend(t)

	router := chi.NewRouter()
	controller := newTestWireGuardController(t)
	registerWireGuardRoutes(router, controller, nil)

	uid, err := wireGuardPeerUIDFromPublicKey(testPublicKeyA)
	if err != nil {
		t.Fatalf("wireGuardPeerUIDFromPublicKey() failed: %v", err)
	}
	saveWireGuardPeerFixture(t, uid, "node-a", testPublicKeyA, "100.97.0.8/32")

	request := httptest.NewRequest("POST", "/phone-home/"+uid, nil)
	request.RemoteAddr = "10.0.0.8:1234"
	first := httptest.NewRecorder()
	router.ServeHTTP(first, request)
	if first.Code != http.StatusOK {
		t.Fatalf("expected 200 for first phone-home call, got %d", first.Code)
	}

	if _, err := storage.LoadWireGuardPeer(context.Background(), uid); !errors.Is(err, fabricaStorage.ErrNotFound) {
		t.Fatalf("expected resource to be deleted, got err=%v", err)
	}

	secondReq := httptest.NewRequest("POST", "/phone-home/"+uid, nil)
	secondReq.RemoteAddr = "10.0.0.8:1234"
	second := httptest.NewRecorder()
	router.ServeHTTP(second, secondReq)
	if second.Code != http.StatusOK {
		t.Fatalf("expected 200 for second phone-home call, got %d", second.Code)
	}
}

func TestPhoneHomeResolvesUIDFromClientIPFallback(t *testing.T) {
	initTestStorageBackend(t)

	router := chi.NewRouter()
	controller := newTestWireGuardController(t)
	smd := smdclient.NewMockSMDClient()
	smd.AddComponent(&smdclient.Component{ID: "x1000c0s0b0n9", IP: "10.252.0.39"})
	_ = smd.AddWGIP("x1000c0s0b0n9", "100.97.0.39")
	registerWireGuardRoutes(router, controller, smd)

	uid, err := wireGuardPeerUIDFromPublicKey(testPublicKeyB)
	if err != nil {
		t.Fatalf("wireGuardPeerUIDFromPublicKey() failed: %v", err)
	}
	saveWireGuardPeerFixture(t, uid, "node-b", testPublicKeyB, "100.97.0.39/32")

	req := httptest.NewRequest("POST", "/phone-home/not-a-wireguardpeer-uid", nil)
	req.RemoteAddr = "10.252.0.39:2000"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for phone-home fallback path, got %d", w.Code)
	}

	if _, err := storage.LoadWireGuardPeer(context.Background(), uid); !errors.Is(err, fabricaStorage.ErrNotFound) {
		t.Fatalf("expected fallback path to delete resource, got err=%v", err)
	}
}

func callWGInit(t *testing.T, router chi.Router, publicKey, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()

	body := bytes.NewBufferString(fmt.Sprintf(`{"public_key":%q}`, publicKey))
	req := httptest.NewRequest("POST", "/wg-init", body)
	req.RemoteAddr = remoteAddr
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func saveWireGuardPeerFixture(t *testing.T, uid, name, publicKey, allowedIP string) {
	t.Helper()

	now := time.Now().UTC()
	peer := &v1.WireGuardPeer{
		APIVersion: "v1",
		Kind:       "WireGuardPeer",
		Metadata: fabrica.Metadata{
			UID:       uid,
			Name:      name,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Spec: v1.WireGuardPeerSpec{
			PublicKey: publicKey,
			AllowedIP: allowedIP,
		},
	}

	if err := storage.SaveWireGuardPeer(context.Background(), peer); err != nil {
		t.Fatalf("SaveWireGuardPeer() failed: %v", err)
	}
}

type fakeWireGuardDevice struct {
	publicKey   string
	listenPort  int
	addedPeers  map[string]string
	removedKeys []string
}

func (d *fakeWireGuardDevice) SetPrivateKey(string) error       { return nil }
func (d *fakeWireGuardDevice) Close() error                     { return nil }
func (d *fakeWireGuardDevice) PublicKeyValue() string           { return d.publicKey }
func (d *fakeWireGuardDevice) SetPublicKeyValue(pub string)     { d.publicKey = pub }
func (d *fakeWireGuardDevice) ListenPortValue() int             { return d.listenPort }
func (d *fakeWireGuardDevice) PrivateKeyValue() (string, error) { return "", nil }
func (d *fakeWireGuardDevice) AddPeer(pub, allowed string) error {
	d.addedPeers[pub] = allowed
	return nil
}
func (d *fakeWireGuardDevice) RemovePeer(publicKey string) error {
	d.removedKeys = append(d.removedKeys, publicKey)
	return nil
}

func newTestController(t *testing.T) *wireguard.Controller {
	t.Helper()
	_, network, err := net.ParseCIDR("100.97.0.0/16")
	if err != nil {
		t.Fatalf("ParseCIDR failed: %v", err)
	}
	allocator, err := wireguard.NewIPAllocator(network.String())
	if err != nil {
		t.Fatalf("NewIPAllocator failed: %v", err)
	}
	serverIP := net.ParseIP("100.97.0.1")
	if err := allocator.Reserve(net.IPAddr{IP: serverIP}); err != nil {
		t.Fatalf("reserve server IP failed: %v", err)
	}
	device := &fakeWireGuardDevice{
		publicKey:  "server-public-key",
		listenPort: 51820,
		addedPeers: make(map[string]string),
	}
	return &wireguard.Controller{
		Device:      device,
		Network:     *network,
		ServerIP:    serverIP,
		Peers:       make(map[string]wireguard.PeerState),
		IPAllocator: allocator,
	}
}

func newTestWireGuardController(t *testing.T) *wireguard.Controller {
	t.Helper()

	serverIP := net.ParseIP("100.97.0.1")
	_, network, err := net.ParseCIDR("100.97.0.0/24")
	if err != nil {
		t.Fatalf("ParseCIDR() failed: %v", err)
	}

	allocator, err := wireguard.NewIPAllocator(network.String())
	if err != nil {
		t.Fatalf("NewIPAllocator() failed: %v", err)
	}
	if err := allocator.Reserve(net.IPAddr{IP: serverIP}); err != nil {
		t.Fatalf("reserve server IP failed: %v", err)
	}

	return &wireguard.Controller{
		Device: &fakeWireGuardDevice{
			publicKey:  "server-pubkey",
			listenPort: 51820,
			addedPeers: make(map[string]string),
		},
		Network:     *network,
		ServerIP:    serverIP,
		Peers:       map[string]wireguard.PeerState{},
		IPAllocator: allocator,
	}
}

func TestWireGuardRoutesHappyPathWithSMDIntegration(t *testing.T) {
	controller := newTestController(t)
	mockSMD := smdclient.NewMockSMDClient()
	mockSMD.AddComponent(&smdclient.Component{
		ID:  "x1000c0s0b0n0",
		IP:  "10.0.0.7",
		NID: 1000,
	})

	router := chi.NewRouter()
	registerWireGuardRoutes(router, controller, mockSMD)

	req := httptest.NewRequest(http.MethodPost, "/wg-init", bytes.NewBufferString(`{"public_key":"`+testPublicKeyA+`"}`))
	req.Header.Set("X-Forwarded-For", "10.0.0.7")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}

	var resp wgInitResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode wg-init response: %v", err)
	}
	if resp.ClientVPNIP == "" {
		t.Fatal("expected allocated VPN IP in response")
	}
	if resp.ServerPubKey != "server-public-key" {
		t.Fatalf("expected server public key in response, got %q", resp.ServerPubKey)
	}
	if resp.PeerUID == "" {
		t.Fatal("expected peer UID in response")
	}

	storedWGIP, err := mockSMD.WGIPfromID("x1000c0s0b0n0")
	if err != nil {
		t.Fatalf("expected AddWGIP side effect, got error: %v", err)
	}
	if storedWGIP != resp.ClientVPNIP {
		t.Fatalf("expected WG IP %q in SMD store, got %q", resp.ClientVPNIP, storedWGIP)
	}

	phoneHome := httptest.NewRequest(http.MethodPost, "/phone-home/x1000c0s0b0n0", nil)
	phoneHome.Header.Set("X-Forwarded-For", "10.0.0.7")
	phoneHomeResp := httptest.NewRecorder()
	router.ServeHTTP(phoneHomeResp, phoneHome)
	if phoneHomeResp.Code != http.StatusOK {
		t.Fatalf("expected 200 from phone-home, got %d", phoneHomeResp.Code)
	}
}

func TestGetClientIPFromRequestParsesForwardingHeaders(t *testing.T) {
	t.Run("xff first", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wg-init", nil)
		req.Header.Set("X-Forwarded-For", "10.9.8.7")
		if got := getClientIPFromRequest(req); got != "10.9.8.7" {
			t.Fatalf("expected X-Forwarded-For IP, got %q", got)
		}
	})

	t.Run("forwarded fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wg-init", nil)
		req.Header.Set("Forwarded", `for="192.0.2.44";proto=https`)
		if got := getClientIPFromRequest(req); got != "192.0.2.44" {
			t.Fatalf("expected Forwarded IP, got %q", got)
		}
	})

	t.Run("remote addr fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/wg-init", nil)
		req.RemoteAddr = "203.0.113.9:12345"
		if got := getClientIPFromRequest(req); got != "203.0.113.9" {
			t.Fatalf("expected remote addr IP, got %q", got)
		}
	})
}
