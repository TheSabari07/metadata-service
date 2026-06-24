// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

package reconcilers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"

	v1 "github.com/OpenCHAMI/metadata-service/apis/cloud-init.openchami.io/v1"
	"github.com/openchami/fabrica/pkg/reconcile"
)

const (
	wireGuardPeerPhaseReady    = "Ready"
	wireGuardPeerPhaseError    = "Error"
	wireGuardPeerPhaseDegraded = "Degraded"
)

// WireGuardPeerController captures the controller methods used by the reconciler.
type WireGuardPeerController interface {
	UpsertPeer(peerID, publicKey, allowedIP string) error
	RemovePeerByID(peerID string) error
}

// WireGuardPeerReconciler reconciles WireGuardPeer resources into WireGuard device state.
type WireGuardPeerReconciler struct {
	reconcile.BaseReconciler
	controller WireGuardPeerController
}

// NewWireGuardPeerReconciler builds a WireGuardPeer reconciler instance.
func NewWireGuardPeerReconciler(client reconcile.ClientInterface, controller WireGuardPeerController) *WireGuardPeerReconciler {
	return &WireGuardPeerReconciler{
		BaseReconciler: reconcile.BaseReconciler{Client: client},
		controller:     controller,
	}
}

// GetResourceKind returns the resource kind this reconciler handles.
func (r *WireGuardPeerReconciler) GetResourceKind() string {
	return "WireGuardPeer"
}

// Reconcile applies a WireGuardPeer intent to the WireGuard controller.
func (r *WireGuardPeerReconciler) Reconcile(ctx context.Context, resource interface{}) (reconcile.Result, error) {
	peer, err := decodeWireGuardPeer(resource)
	if err != nil {
		return reconcile.Result{}, fmt.Errorf("decode WireGuardPeer: %w", err)
	}

	if strings.TrimSpace(peer.Spec.PublicKey) == "" {
		return reconcile.Result{}, r.updateStatus(ctx, peer, wireGuardPeerPhaseError, "public_key is required", false)
	}
	if _, _, err := net.ParseCIDR(peer.Spec.AllowedIP); err != nil {
		return reconcile.Result{}, r.updateStatus(ctx, peer, wireGuardPeerPhaseError, fmt.Sprintf("invalid allowed_ip %q: %v", peer.Spec.AllowedIP, err), false)
	}

	if !r.controllerConfigured() {
		if err := r.updateStatus(ctx, peer, wireGuardPeerPhaseDegraded, "wireguard controller not configured; reconcile skipped", false); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}

	if err := r.controller.UpsertPeer(peer.Metadata.UID, peer.Spec.PublicKey, peer.Spec.AllowedIP); err != nil {
		statusErr := r.updateStatus(ctx, peer, wireGuardPeerPhaseError, fmt.Sprintf("failed to upsert peer: %v", err), false)
		if statusErr != nil {
			return reconcile.Result{}, fmt.Errorf("upsert peer: %w (status update failed: %v)", err, statusErr)
		}
		return reconcile.Result{}, fmt.Errorf("upsert peer: %w", err)
	}

	if err := r.updateStatus(ctx, peer, wireGuardPeerPhaseReady, "wireguard peer reconciled", true); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

// Delete removes a WireGuard peer from controller state.
func (r *WireGuardPeerReconciler) Delete(_ context.Context, uid string) error {
	if !r.controllerConfigured() || uid == "" {
		return nil
	}

	if err := r.controller.RemovePeerByID(uid); err != nil {
		if isPeerNotFound(err) {
			return nil
		}
		return fmt.Errorf("remove peer %q: %w", uid, err)
	}
	return nil
}

func (r *WireGuardPeerReconciler) updateStatus(ctx context.Context, peer *v1.WireGuardPeer, phase, message string, ready bool) error {
	peer.Status.Phase = phase
	peer.Status.Message = message
	peer.Status.Ready = ready
	if peer.APIVersion != "" {
		peer.Status.Version = peer.APIVersion
	} else {
		peer.Status.Version = "v1"
	}

	if err := r.UpdateStatus(ctx, peer); err != nil {
		return fmt.Errorf("update status for WireGuardPeer %s: %w", peer.Metadata.UID, err)
	}
	return nil
}

func decodeWireGuardPeer(resource interface{}) (*v1.WireGuardPeer, error) {
	switch typed := resource.(type) {
	case *v1.WireGuardPeer:
		return typed, nil
	case v1.WireGuardPeer:
		peer := typed
		return &peer, nil
	case []byte:
		var peer v1.WireGuardPeer
		if err := json.Unmarshal(typed, &peer); err != nil {
			return nil, err
		}
		return &peer, nil
	default:
		return nil, fmt.Errorf("unsupported resource type %T", resource)
	}
}

func isPeerNotFound(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "peer not found")
}

func (r *WireGuardPeerReconciler) controllerConfigured() bool {
	if r.controller == nil {
		return false
	}

	value := reflect.ValueOf(r.controller)
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		return !value.IsNil()
	default:
		return true
	}
}
