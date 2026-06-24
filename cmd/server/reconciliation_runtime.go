// SPDX-License-Identifier: MIT
// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/OpenCHAMI/metadata-service/internal/storage"
	"github.com/OpenCHAMI/metadata-service/pkg/reconcilers"
	"github.com/OpenCHAMI/metadata-service/pkg/wireguard"
	"github.com/openchami/fabrica/pkg/events"
	fabricaStorage "github.com/openchami/fabrica/pkg/storage"
)

var newReconciliationRuntimeFn = initializeReconciliationRuntime

// reconciliationRuntime wires Fabrica eventing to resource reconcilers.
type reconciliationRuntime struct {
	eventBus                events.EventBus
	storageClient           *storage.StorageClient
	wireGuardPeerReconciler *reconcilers.WireGuardPeerReconciler
	wireGuardController     *wireguard.Controller
	subscriptionIDs         []events.SubscriptionID
	stopOnce                sync.Once
}

func initializeReconciliationRuntime(wgController *wireguard.Controller) (_ *reconciliationRuntime, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("reconciliation runtime panic: %v\n%s", recovered, debug.Stack())
		}
	}()

	runtime := &reconciliationRuntime{wireGuardController: wgController}
	if err := runtime.initialize(); err != nil {
		_ = runtime.Stop()
		return nil, err
	}
	return runtime, nil
}

func (r *reconciliationRuntime) initialize() error {
	if err := r.initializeEventing(); err != nil {
		return err
	}

	r.storageClient = storage.NewStorageClient()
	r.wireGuardPeerReconciler = reconcilers.NewWireGuardPeerReconciler(r.storageClient, r.wireGuardController)

	if err := r.registerEventHooks(); err != nil {
		return err
	}

	if err := r.reconcileExistingResources(context.Background()); err != nil {
		return err
	}

	log.Printf("Reconciliation runtime initialized")
	return nil
}

func (r *reconciliationRuntime) initializeEventing() error {
	config := events.GetEventConfig()
	config.Enabled = true
	config.LifecycleEventsEnabled = true
	if config.EventTypePrefix == "" {
		config.EventTypePrefix = "io.fabrica"
	}
	if config.Source == "" {
		config.Source = "metadata-service"
	}
	events.SetEventConfig(config)

	bus := events.NewInMemoryEventBus(1000, 5)
	bus.Start()
	events.SetGlobalEventBus(bus)
	r.eventBus = bus

	return nil
}

func (r *reconciliationRuntime) registerEventHooks() error {
	if r.eventBus == nil {
		return fmt.Errorf("event bus is not initialized")
	}

	subID, err := r.eventBus.Subscribe("**", func(ctx context.Context, event events.Event) error {
		return r.handleResourceEvent(ctx, event)
	})
	if err != nil {
		return fmt.Errorf("subscribe to reconciliation events: %w", err)
	}
	r.subscriptionIDs = append(r.subscriptionIDs, subID)

	return nil
}

func (r *reconciliationRuntime) reconcileExistingResources(ctx context.Context) error {
	items, err := r.storageClient.List(ctx, "WireGuardPeer")
	if err != nil {
		return fmt.Errorf("list WireGuardPeer resources: %w", err)
	}

	for _, item := range items {
		if _, err := r.wireGuardPeerReconciler.Reconcile(r.withWireGuardController(ctx), item); err != nil {
			return fmt.Errorf("reconcile existing WireGuardPeer: %w", err)
		}
	}

	return nil
}

func (r *reconciliationRuntime) handleResourceEvent(ctx context.Context, event events.Event) error {
	if event.ResourceKind() != "WireGuardPeer" {
		return nil
	}

	uid := event.ResourceUID()
	if uid == "" {
		return nil
	}

	action := eventAction(event)
	ctx = r.withWireGuardController(ctx)

	switch action {
	case "delete", "deleted":
		return r.wireGuardPeerReconciler.Delete(ctx, uid)
	default:
		resource, err := r.storageClient.Get(ctx, "WireGuardPeer", uid)
		if err != nil {
			if errors.Is(err, fabricaStorage.ErrNotFound) {
				return nil
			}
			return fmt.Errorf("load WireGuardPeer %s: %w", uid, err)
		}
		_, err = r.wireGuardPeerReconciler.Reconcile(ctx, resource)
		if err != nil {
			return fmt.Errorf("reconcile WireGuardPeer %s: %w", uid, err)
		}
		return nil
	}
}

func (r *reconciliationRuntime) withWireGuardController(ctx context.Context) context.Context {
	if r.wireGuardController == nil {
		return ctx
	}
	return context.WithValue(ctx, wireguard.ControllerContextKey, r.wireGuardController)
}

// Stop releases event subscriptions and closes the event bus.
func (r *reconciliationRuntime) Stop() error {
	var stopErr error

	r.stopOnce.Do(func() {
		for _, subID := range r.subscriptionIDs {
			if err := r.eventBus.Unsubscribe(subID); err != nil {
				stopErr = errors.Join(stopErr, fmt.Errorf("unsubscribe %s: %w", subID, err))
			}
		}
		r.subscriptionIDs = nil

		if r.eventBus != nil {
			if err := r.eventBus.Close(); err != nil {
				stopErr = errors.Join(stopErr, fmt.Errorf("close event bus: %w", err))
			}
		}

		events.SetGlobalEventBus(nil)
	})

	return stopErr
}

func eventAction(event events.Event) string {
	if rawAction, ok := event.Extensions()["action"]; ok {
		if action, ok := rawAction.(string); ok {
			return strings.ToLower(action)
		}
	}

	parts := strings.Split(strings.ToLower(event.Type()), ".")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
