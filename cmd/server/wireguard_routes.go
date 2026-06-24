// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2025 OpenCHAMI Contributors

package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	v1 "github.com/OpenCHAMI/metadata-service/apis/cloud-init.openchami.io/v1"
	"github.com/OpenCHAMI/metadata-service/internal/storage"
	"github.com/OpenCHAMI/metadata-service/pkg/smdclient"
	"github.com/OpenCHAMI/metadata-service/pkg/wireguard"
	"github.com/go-chi/chi/v5"
	"github.com/openchami/fabrica/pkg/events"
	"github.com/openchami/fabrica/pkg/fabrica"
	fabricaStorage "github.com/openchami/fabrica/pkg/storage"
)

type wgInitRequest struct {
	PublicKey string `json:"public_key"`
}

type wgInitResponse struct {
	Message      string `json:"message"`
	PeerUID      string `json:"peer-uid"`
	ClientVPNIP  string `json:"client-vpn-ip"`
	ServerPubKey string `json:"server-public-key"`
	ServerIP     string `json:"server-ip"`
	ServerPort   string `json:"server-port"`
}

func getClientIPFromRequest(r *http.Request) string {
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		return xff
	}
	fwd := r.Header.Get("Forwarded")
	if fwd != "" {
		parts := strings.Split(fwd, ";")
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if strings.HasPrefix(p, "for=") {
				return strings.Trim(strings.Split(p, "=")[1], "\"")
			}
		}
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	return host
}

func registerWireGuardRoutes(r chi.Router, controller *wireguard.Controller, smd smdclient.SMDClient) {
	r.Post("/wg-init", func(w http.ResponseWriter, r *http.Request) {
		if controller == nil {
			http.Error(w, "wireguard disabled", http.StatusServiceUnavailable)
			return
		}

		var req wgInitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		normalizedPublicKey, err := normalizeWireGuardPublicKey(req.PublicKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		peerUID, err := wireGuardPeerUIDFromPublicKey(normalizedPublicKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if existing, err := storage.LoadWireGuardPeer(r.Context(), peerUID); err == nil {
			vpnIP, parseErr := vpnIPFromAllowedCIDR(existing.Spec.AllowedIP)
			if parseErr != nil {
				http.Error(w, parseErr.Error(), http.StatusInternalServerError)
				return
			}
			writeWGInitResponse(w, controller, peerUID, vpnIP)
			return
		} else if !errors.Is(err, fabricaStorage.ErrNotFound) {
			http.Error(w, fmt.Sprintf("failed to check existing peer: %v", err), http.StatusInternalServerError)
			return
		}

		clientIP := getClientIPFromRequest(r)
		requesterID := resolveRequesterIdentity(smd, clientIP)

		allowedCIDR, vpnIP, err := allocateWireGuardPeerCIDR(controller)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to allocate WireGuard IP: %v", err), http.StatusInternalServerError)
			return
		}

		now := time.Now().UTC()
		peer := &v1.WireGuardPeer{
			APIVersion: "v1",
			Kind:       "WireGuardPeer",
			Metadata: fabrica.Metadata{
				Name:      requesterID,
				UID:       peerUID,
				CreatedAt: now,
				UpdatedAt: now,
			},
			Spec: v1.WireGuardPeerSpec{
				Description: fmt.Sprintf("WireGuard peer for %s", requesterID),
				PublicKey:   normalizedPublicKey,
				AllowedIP:   allowedCIDR,
			},
		}

		if err := peer.Validate(r.Context()); err != nil {
			releaseAllocatedIP(controller, vpnIP)
			http.Error(w, fmt.Sprintf("failed to validate WireGuard peer intent: %v", err), http.StatusBadRequest)
			return
		}

		if err := storage.SaveWireGuardPeer(r.Context(), peer); err != nil {
			releaseAllocatedIP(controller, vpnIP)
			http.Error(w, fmt.Sprintf("failed to save WireGuard peer intent: %v", err), http.StatusInternalServerError)
			return
		}

		if smd != nil && requesterID != "" && requesterID != clientIP {
			if err := smd.AddWGIP(requesterID, vpnIP); err != nil {
				log.Printf("Warning: failed to persist WGIP in SMD for %s: %v", requesterID, err)
			}
		}

		if err := events.PublishResourceCreated(r.Context(), "WireGuardPeer", peer.Metadata.UID, peer.Metadata.Name, peer); err != nil {
			log.Printf("Warning: failed to publish WireGuardPeer created event for %s: %v", peer.Metadata.UID, err)
		}

		writeWGInitResponse(w, controller, peerUID, vpnIP)
	})

	r.Post("/phone-home/{id}", func(w http.ResponseWriter, r *http.Request) {
		if controller == nil {
			http.Error(w, "wireguard disabled", http.StatusServiceUnavailable)
			return
		}

		targetID := chi.URLParam(r, "id")
		clientIP := getClientIPFromRequest(r)
		peerUID, err := resolvePhoneHomePeerUID(r.Context(), targetID, clientIP, smd)
		if err != nil {
			log.Printf("Warning: unable to resolve phone-home UID for id=%q clientIP=%q: %v", targetID, clientIP, err)
			peerUID = targetID
		}

		if peerUID != "" {
			existing, loadErr := storage.LoadWireGuardPeer(r.Context(), peerUID)
			if loadErr != nil && !errors.Is(loadErr, fabricaStorage.ErrNotFound) {
				http.Error(w, fmt.Sprintf("failed to load WireGuard peer intent: %v", loadErr), http.StatusInternalServerError)
				return
			}

			if delErr := storage.DeleteWireGuardPeer(r.Context(), peerUID); delErr != nil && !errors.Is(delErr, fabricaStorage.ErrNotFound) {
				http.Error(w, fmt.Sprintf("failed to delete WireGuard peer intent: %v", delErr), http.StatusInternalServerError)
				return
			}

			if existing != nil {
				deleteMetadata := map[string]interface{}{"phoneHomeID": targetID}
				if err := events.PublishResourceDeleted(r.Context(), "WireGuardPeer", existing.Metadata.UID, existing.Metadata.Name, deleteMetadata); err != nil {
					log.Printf("Warning: failed to publish WireGuardPeer deleted event for %s: %v", existing.Metadata.UID, err)
				}
			}
		}

		w.WriteHeader(http.StatusOK)
	})
}

func writeWGInitResponse(w http.ResponseWriter, controller *wireguard.Controller, peerUID, vpnIP string) {
	resp := wgInitResponse{
		Message:      "WireGuard peer allocation accepted",
		PeerUID:      peerUID,
		ClientVPNIP:  vpnIP,
		ServerPubKey: controller.PublicKey(),
		ServerIP:     controller.ServerIPString(),
		ServerPort:   fmt.Sprintf("%d", controller.ListenPort()),
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(resp)
}

func normalizeWireGuardPublicKey(publicKey string) (string, error) {
	trimmed := strings.TrimSpace(publicKey)
	if trimmed == "" {
		return "", fmt.Errorf("public_key is required")
	}
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return "", fmt.Errorf("public_key must be valid base64")
	}
	if len(decoded) != 32 {
		return "", fmt.Errorf("public_key must decode to 32 bytes")
	}
	return base64.StdEncoding.EncodeToString(decoded), nil
}

func wireGuardPeerUIDFromPublicKey(publicKey string) (string, error) {
	normalized, err := normalizeWireGuardPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(normalized))
	return "wireguardpeer-" + hex.EncodeToString(hash[:8]), nil
}

func allocateWireGuardPeerCIDR(controller *wireguard.Controller) (allowedCIDR string, vpnIP string, err error) {
	if controller == nil || controller.IPAllocator == nil {
		return "", "", fmt.Errorf("wireguard allocator unavailable")
	}

	allocated, err := controller.IPAllocator.NextAvailable()
	if err != nil {
		return "", "", err
	}
	vpnIP = allocated.IP.String()
	return vpnIP + "/32", vpnIP, nil
}

func releaseAllocatedIP(controller *wireguard.Controller, vpnIP string) {
	if controller == nil || controller.IPAllocator == nil {
		return
	}
	ip := net.ParseIP(vpnIP)
	if ip == nil {
		return
	}
	_ = controller.IPAllocator.Release(net.IPAddr{IP: ip})
}

func vpnIPFromAllowedCIDR(allowedCIDR string) (string, error) {
	ip, _, err := net.ParseCIDR(allowedCIDR)
	if err != nil {
		return "", fmt.Errorf("invalid allowed_ip %q: %w", allowedCIDR, err)
	}
	return ip.String(), nil
}

func resolveRequesterIdentity(smd smdclient.SMDClient, clientIP string) string {
	if smd == nil {
		return clientIP
	}
	if id, err := smdclient.ResolveComponentID(smd, clientIP); err == nil && id != "" {
		return id
	}
	return clientIP
}

func resolvePhoneHomePeerUID(ctx context.Context, routeID, clientIP string, smd smdclient.SMDClient) (string, error) {
	if routeID != "" {
		if _, err := storage.LoadWireGuardPeer(ctx, routeID); err == nil {
			return routeID, nil
		} else if !errors.Is(err, fabricaStorage.ErrNotFound) {
			return "", err
		}
	}

	if smd != nil && routeID != "" {
		if wgIP, err := smd.WGIPfromID(routeID); err == nil && wgIP != "" {
			if uid, found, err := findWireGuardPeerUIDByVPNIP(ctx, wgIP); err != nil {
				return "", err
			} else if found {
				return uid, nil
			}
		}
	}

	if smd != nil {
		if componentID, err := smdclient.ResolveComponentID(smd, clientIP); err == nil && componentID != "" {
			if wgIP, err := smd.WGIPfromID(componentID); err == nil && wgIP != "" {
				if uid, found, err := findWireGuardPeerUIDByVPNIP(ctx, wgIP); err != nil {
					return "", err
				} else if found {
					return uid, nil
				}
			}
		}
	}

	if routeID != "" {
		return routeID, nil
	}
	return "", fmt.Errorf("unable to resolve WireGuard peer UID")
}

func findWireGuardPeerUIDByVPNIP(ctx context.Context, vpnIP string) (string, bool, error) {
	resources, err := storage.LoadAllWireGuardPeers(ctx)
	if err != nil {
		return "", false, err
	}

	parsedTarget := net.ParseIP(vpnIP)
	if parsedTarget == nil {
		return "", false, fmt.Errorf("invalid vpn IP %q", vpnIP)
	}

	for _, resource := range resources {
		peerIP, err := vpnIPFromAllowedCIDR(resource.Spec.AllowedIP)
		if err != nil {
			continue
		}
		if parsedTarget.Equal(net.ParseIP(peerIP)) {
			return resource.Metadata.UID, true, nil
		}
	}

	return "", false, nil
}
