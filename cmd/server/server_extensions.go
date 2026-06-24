// SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/OpenCHAMI/metadata-service/pkg/wireguard"
	"github.com/go-chi/chi/v5"
	"github.com/spf13/viper"
)

// registerCustomServerIntegrations keeps custom metadata/wireguard wiring out of the generated
// scaffold flow so main.go remains close to Fabrica defaults.
func registerCustomServerIntegrations(serverCtx context.Context, r chi.Router) error {
	if err := registerResourcePrefixesSafely(); err != nil {
		log.Printf("Failed to register resource prefixes: %v", err)
	}

	wgController := setupWireGuardController()

	currentWGController = wgController
	if wgController != nil {
		r.Use(wireGuardControllerMiddleware(wgController))
		if viper.GetBool("wireguard_only") {
			r.Use(wireGuardOnlyMiddleware(wgController))
		}
	}

	// Register generated API routes.
	RegisterGeneratedRoutes(r)

	smdRuntime, err := initSMDRuntime()
	if err != nil {
		return err
	}
	smdClient := smdRuntime.client
	storeAdapter := NewStorageAdapter()

	// WireGuard routes need the SMD client — register them after generated routes.
	if wgController != nil {
		registerWireGuardRoutes(r, wgController, smdClient)
	}

	RegisterCloudInitRoutes(r, smdClient, storeAdapter)
	if smdRuntime.startWorkers != nil {
		smdRuntime.startWorkers(serverCtx)
	}
	return nil
}

func registerResourcePrefixesSafely() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			msg := fmt.Sprint(recovered)
			if strings.Contains(msg, "already registered") {
				err = nil
				return
			}
			err = fmt.Errorf("register resource prefixes panic: %v", recovered)
		}
	}()

	return registerResourcePrefixes()
}

func setupWireGuardController() *wireguard.Controller {
	wgCIDR := viper.GetString("wireguard_server")
	if wgCIDR == "" {
		return nil
	}

	serverIP, network, err := net.ParseCIDR(wgCIDR)
	if err != nil {
		log.Printf("Invalid WIREGUARD_SERVER value: %v", err)
		return nil
	}

	stateFile := viper.GetString("wireguard_state_file")
	controller, err := wireguard.NewController("wg0", serverIP, network, 51820, stateFile)
	if err != nil {
		log.Printf("Failed to initialize WireGuard controller: %v", err)
		return nil
	}

	log.Printf("WireGuard userspace controller enabled on %s", wgCIDR)
	if stateFile != "" {
		log.Printf("WireGuard state persistence enabled at %s", stateFile)
	}

	return controller
}
