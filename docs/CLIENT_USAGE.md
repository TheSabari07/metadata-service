<!--
SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

SPDX-License-Identifier: MIT
-->

# Client Usage Guide

This guide covers how to use the generated client library and CLI to interact with the metadata service API.

## Table of Contents

- [CLI Client](#cli-client)
- [Go Client Library](#go-client-library)
- [Resource Management](#resource-management)
- [Error Handling](#error-handling)
- [Authentication](#authentication)
- [Advanced Usage](#advanced-usage)
- [Examples](#examples)

---

## CLI Client

### Installation

```bash
# Build from source
git clone https://github.com/OpenCHAMI/metadata-service.git
cd metadata-service
make build

# Client binary location
./bin/client

# Or run directly
go run ./cmd/client/main.go
```

### Basic Usage

```bash
# Show help
./bin/client --help

# Show help for specific resource
./bin/client group --help

# Set server URL (required)
export METADATA_SERVER=http://localhost:8080
# Or use --server flag
./bin/client --server http://localhost:8080 group list
```

### Global Flags

```bash
--server string       Metadata service URL (default: http://localhost:8080)
--token string        Bearer token for authentication
--version string      API version (default: v1)
--timeout duration    Request timeout (default: 30s)
--insecure            Skip TLS verification
--output string       Output format: json|yaml|table (default: table)
```

---

## Resource Types

The CLI supports four resource types:
- `clusterdefaults` - Cluster-wide configuration
- `group` - Group templates and metadata
- `instanceinfo` - Per-instance overrides
- `wireguardpeer` - WireGuard VPN peers

### Common Commands

Each resource type supports these commands:

```bash
# List resources
./bin/client <resource> list

# Get specific resource
./bin/client <resource> get <uid>

# Create resource
./bin/client <resource> create --spec <json-file>

# Update resource
./bin/client <resource> update <uid> --spec <json-file>

# Delete resource
./bin/client <resource> delete <uid>

# Watch for changes (streaming)
./bin/client <resource> watch
```

---

## Resource Management

### ClusterDefaults

Cluster-wide settings applied to all nodes.

**Create:**
```bash
cat > clusterdefaults.json <<'EOF'
{
  "metadata": {
    "name": "production-cluster"
  },
  "spec": {
    "description": "Production HPC cluster",
    "base_url": "https://metadata.example.com",
    "cloud_provider": "OpenCHAMI",
    "region": "us-west-2",
    "availability_zone": "us-west-2a",
    "cluster_name": "hpc-prod",
    "short_name": "hp",
    "nid_length": 4,
    "public_keys": [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey admin@example.com"
    ]
  }
}
EOF

./bin/client clusterdefaults create --spec clusterdefaults.json
```

**List:**
```bash
./bin/client clusterdefaults list
```

**Get latest:**
```bash
# The service returns the most recently updated ClusterDefaults
./bin/client clusterdefaults list | jq '.[0]'
```

**Update:**
```bash
# Fetch current, modify, and update
./bin/client clusterdefaults get <uid> > current.json
# Edit current.json
./bin/client clusterdefaults update <uid> --spec current.json
```

### Groups

Group templates and custom metadata.

**Create:**
```bash
cat > compute-group.json <<'EOF'
{
  "metadata": {
    "name": "compute"
  },
  "spec": {
    "description": "Compute nodes",
    "template": "#cloud-config\nhostname: {{ hostname }}\nwrite_files:\n  - path: /etc/node-role\n    content: |\n      ROLE={{ role }}\n      NID={{ nid }}\n      SCHEDULER={{ scheduler }}\n",
    "metaData": {
      "scheduler": "slurm",
      "partition": "compute"
    },
    "osVersion": "rocky-9.3"
  }
}
EOF

./bin/client group create --spec compute-group.json
```

**List:**
```bash
./bin/client group list

# Filter by name (requires jq)
./bin/client group list --output json | jq '.[] | select(.metadata.name == "compute")'
```

**Get by UID:**
```bash
./bin/client group get group-abc123
```

**Update template:**
```bash
# Fetch current group
./bin/client group get group-abc123 --output json > compute.json

# Edit template
jq '.spec.template = "#cloud-config\nhostname: {{ hostname }}\n# Updated template"' \
  compute.json > compute-updated.json

# Update
./bin/client group update group-abc123 --spec compute-updated.json
```

**Delete:**
```bash
./bin/client group delete group-abc123
```

### InstanceInfo

Per-instance overrides for specific nodes.

**Create:**
```bash
cat > instance-override.json <<'EOF'
{
  "metadata": {
    "name": "x1000c0s0b0n0"
  },
  "spec": {
    "instance_id": "x1000c0s0b0n0",
    "local_hostname": "login01",
    "hostname": "login01.example.com",
    "cloud_init_base_url": "https://metadata-internal.example.com",
    "public_keys": [
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICustomKey admin@example.com"
    ]
  }
}
EOF

./bin/client instanceinfo create --spec instance-override.json
```

**List:**
```bash
./bin/client instanceinfo list
```

**Get by name:**
```bash
# Service supports query by metadata.name
./bin/client instanceinfo list --output json | \
  jq '.[] | select(.metadata.name == "x1000c0s0b0n0")'
```

### WireGuardPeer

VPN peer allocations (typically managed by `/wg-init` endpoint).

**List peers:**
```bash
./bin/client wireguardpeer list
```

**Get peer:**
```bash
./bin/client wireguardpeer get wireguardpeer-abc123
```

**Check status:**
```bash
./bin/client wireguardpeer get wireguardpeer-abc123 --output json | jq '.status'
```

**Expected status:**
```json
{
  "phase": "Ready",
  "message": "Peer configured successfully",
  "lastReconciled": "2026-01-15T10:30:00Z"
}
```

**Delete peer:**
```bash
# Manual deletion (prefer /phone-home endpoint)
./bin/client wireguardpeer delete wireguardpeer-abc123
```

---

## Go Client Library

### Installation

```bash
go get github.com/OpenCHAMI/metadata-service/pkg/client
```

### Basic Client Setup

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    "time"

    "github.com/OpenCHAMI/metadata-service/pkg/client"
    "github.com/rs/zerolog"
)

func main() {
    // Create HTTP client with timeout
    httpClient := &http.Client{
        Timeout: 30 * time.Second,
    }

    // Create logger
    logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

    // Create client
    c, err := client.NewClient(
        "http://localhost:8080",
        httpClient,
        logger,
    )
    if err != nil {
        log.Fatal(err)
    }

    // Use client
    ctx := context.Background()
    health, err := c.GetHealth(ctx)
    if err != nil {
        log.Fatal(err)
    }
    fmt.Printf("Service status: %s\n", health.Status)
}
```

### Client with Bearer Token

```go
c, err := client.NewClientWithBearerToken(
    "http://localhost:8080",
    "your-bearer-token",
    httpClient,
    logger,
)
```

### API Version

```go
// Use specific API version
c = c.WithVersion("v1")

// Version is sent via X-API-Version header
```

---

## Resource Operations

### Create Resource

```go
import (
    "github.com/OpenCHAMI/metadata-service/pkg/client"
    v1 "github.com/OpenCHAMI/metadata-service/pkg/resources/v1"
)

// Create ClusterDefaults
req := client.CreateClusterDefaultsRequest{
    Metadata: v1.Metadata{
        Name: "production",
    },
    Spec: v1.ClusterDefaultsSpec{
        Description:      "Production cluster",
        BaseURL:          "https://metadata.example.com",
        CloudProvider:    "OpenCHAMI",
        Region:           "us-west-2",
        AvailabilityZone: "us-west-2a",
        ClusterName:      "hpc-prod",
        ShortName:        "hp",
        NIDLength:        4,
        PublicKeys: []string{
            "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKey admin@example.com",
        },
    },
}

result, err := c.CreateClusterDefaults(ctx, req)
if err != nil {
    log.Fatalf("create failed: %v", err)
}
fmt.Printf("Created: %s (UID: %s)\n", result.Metadata.Name, result.Metadata.UID)
```

### List Resources

```go
// List all groups
groups, err := c.GetGroups(ctx)
if err != nil {
    log.Fatalf("list failed: %v", err)
}

for _, group := range groups {
    fmt.Printf("Group: %s (UID: %s)\n", group.Metadata.Name, group.Metadata.UID)
}
```

### Get Resource by UID

```go
group, err := c.GetGroup(ctx, "group-abc123")
if err != nil {
    log.Fatalf("get failed: %v", err)
}
fmt.Printf("Group: %s\n", group.Metadata.Name)
fmt.Printf("Template: %s\n", group.Spec.Template)
```

### Update Resource

```go
// Fetch current resource
group, err := c.GetGroup(ctx, "group-abc123")
if err != nil {
    log.Fatal(err)
}

// Modify spec
group.Spec.Description = "Updated description"
group.Spec.MetaData["new_field"] = "new_value"

// Update
req := client.UpdateGroupRequest{
    Metadata: group.Metadata,
    Spec:     group.Spec,
}

updated, err := c.UpdateGroup(ctx, group.Metadata.UID, req)
if err != nil {
    log.Fatalf("update failed: %v", err)
}
fmt.Printf("Updated: %s\n", updated.Metadata.UID)
```

### Patch Resource

```go
import "encoding/json"

// JSON Patch (RFC 6902)
patch := []map[string]interface{}{
    {
        "op":    "replace",
        "path":  "/spec/description",
        "value": "Patched description",
    },
    {
        "op":    "add",
        "path":  "/spec/metaData/new_key",
        "value": "new_value",
    },
}

patchBytes, _ := json.Marshal(patch)

patched, err := c.PatchGroup(ctx, "group-abc123", patchBytes)
if err != nil {
    log.Fatalf("patch failed: %v", err)
}
```

### Delete Resource

```go
err := c.DeleteGroup(ctx, "group-abc123")
if err != nil {
    log.Fatalf("delete failed: %v", err)
}
fmt.Println("Deleted successfully")
```

---

## Error Handling

### Client Errors

```go
import (
    "errors"
    "net/http"
)

group, err := c.GetGroup(ctx, "group-nonexistent")
if err != nil {
    // Check for specific HTTP status
    var httpErr *client.HTTPError
    if errors.As(err, &httpErr) {
        switch httpErr.StatusCode {
        case http.StatusNotFound:
            fmt.Println("Group not found")
        case http.StatusUnauthorized:
            fmt.Println("Authentication required")
        case http.StatusForbidden:
            fmt.Println("Permission denied")
        case http.StatusInternalServerError:
            fmt.Println("Server error")
        default:
            fmt.Printf("HTTP error %d: %s\n", httpErr.StatusCode, httpErr.Message)
        }
    } else {
        // Network error, timeout, etc.
        fmt.Printf("Request failed: %v\n", err)
    }
    return
}
```

### Validation Errors

```go
req := client.CreateGroupRequest{
    Metadata: v1.Metadata{Name: "test"},
    Spec: v1.GroupSpec{
        Template: "{{ invalid syntax",  // Invalid Pongo2
    },
}

_, err := c.CreateGroup(ctx, req)
if err != nil {
    // Server returns 400 with validation error
    fmt.Printf("Validation failed: %v\n", err)
    // Output: Validation failed: template validation failed: unexpected token
}
```

### Timeout Handling

```go
// Create context with timeout
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

groups, err := c.GetGroups(ctx)
if err != nil {
    if errors.Is(err, context.DeadlineExceeded) {
        fmt.Println("Request timed out")
    } else {
        fmt.Printf("Error: %v\n", err)
    }
}
```

---

## Authentication

### Bearer Token

```go
// Option 1: Create client with token
c, err := client.NewClientWithBearerToken(
    "http://localhost:8080",
    "your-bearer-token",
    httpClient,
    logger,
)

// Option 2: Set token on existing client
c = c.WithBearerToken("your-bearer-token")
```

### Token Refresh

```go
// Refresh token periodically
func refreshToken(c *client.Client) {
    ticker := time.NewTicker(50 * time.Minute)
    defer ticker.Stop()

    for range ticker.C {
        newToken, err := fetchNewToken()  // Your token refresh logic
        if err != nil {
            log.Printf("token refresh failed: %v", err)
            continue
        }
        c = c.WithBearerToken(newToken)
    }
}

go refreshToken(c)
```

### TLS Configuration

```go
import (
    "crypto/tls"
    "crypto/x509"
    "os"
)

// Custom TLS config
caCert, _ := os.ReadFile("/path/to/ca.crt")
caCertPool := x509.NewCertPool()
caCertPool.AppendCertsFromPEM(caCert)

tlsConfig := &tls.Config{
    RootCAs:    caCertPool,
    MinVersion: tls.VersionTLS12,
}

httpClient := &http.Client{
    Transport: &http.Transport{
        TLSClientConfig: tlsConfig,
    },
    Timeout: 30 * time.Second,
}

c, err := client.NewClient("https://metadata.example.com", httpClient, logger)
```

---

## Advanced Usage

### Custom HTTP Client

```go
import "golang.org/x/net/http2"

// HTTP/2 client
httpClient := &http.Client{
    Transport: &http2.Transport{},
    Timeout:   30 * time.Second,
}

c, err := client.NewClient(baseURL, httpClient, logger)
```

### Retry Logic

```go
import "github.com/hashicorp/go-retryablehttp"

// Retryable HTTP client
retryClient := retryablehttp.NewClient()
retryClient.RetryMax = 3
retryClient.RetryWaitMin = 1 * time.Second
retryClient.RetryWaitMax = 5 * time.Second

httpClient := retryClient.StandardClient()

c, err := client.NewClient(baseURL, httpClient, logger)
```

### Request Tracing

```go
import "go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

// HTTP client with OpenTelemetry tracing
httpClient := &http.Client{
    Transport: otelhttp.NewTransport(http.DefaultTransport),
    Timeout:   30 * time.Second,
}

c, err := client.NewClient(baseURL, httpClient, logger)
```

### Structured Logging

```go
import "github.com/rs/zerolog"

// Customize logger
logger := zerolog.New(os.Stdout).
    With().
    Timestamp().
    Str("service", "metadata-client").
    Str("environment", "production").
    Logger()

c, err := client.NewClient(baseURL, httpClient, logger)
```

---

## Examples

### Example 1: Create Complete Environment

```go
func setupCluster(c *client.Client) error {
    ctx := context.Background()

    // 1. Create ClusterDefaults
    _, err := c.CreateClusterDefaults(ctx, client.CreateClusterDefaultsRequest{
        Metadata: v1.Metadata{Name: "production"},
        Spec: v1.ClusterDefaultsSpec{
            ClusterName:   "hpc-prod",
            ShortName:     "hp",
            BaseURL:       "https://metadata.example.com",
            CloudProvider: "OpenCHAMI",
            PublicKeys:    []string{"ssh-ed25519 AAAA..."},
        },
    })
    if err != nil {
        return fmt.Errorf("create cluster defaults: %w", err)
    }

    // 2. Create compute group
    _, err = c.CreateGroup(ctx, client.CreateGroupRequest{
        Metadata: v1.Metadata{Name: "compute"},
        Spec: v1.GroupSpec{
            Description: "Compute nodes",
            Template:    "#cloud-config\nhostname: {{ hostname }}\n",
            MetaData:    map[string]interface{}{"scheduler": "slurm"},
        },
    })
    if err != nil {
        return fmt.Errorf("create compute group: %w", err)
    }

    // 3. Create login node override
    _, err = c.CreateInstanceInfo(ctx, client.CreateInstanceInfoRequest{
        Metadata: v1.Metadata{Name: "x1000c0s0b0n0"},
        Spec: v1.InstanceInfoSpec{
            InstanceID:    "x1000c0s0b0n0",
            LocalHostname: "login01",
            Hostname:      "login01.example.com",
        },
    })
    if err != nil {
        return fmt.Errorf("create instance info: %w", err)
    }

    return nil
}
```

### Example 2: Bulk Group Updates

```go
func updateGroupTemplates(c *client.Client, newTemplate string) error {
    ctx := context.Background()

    // List all groups
    groups, err := c.GetGroups(ctx)
    if err != nil {
        return err
    }

    // Update each group
    for _, group := range groups {
        group.Spec.Template = newTemplate

        _, err := c.UpdateGroup(ctx, group.Metadata.UID, client.UpdateGroupRequest{
            Metadata: group.Metadata,
            Spec:     group.Spec,
        })
        if err != nil {
            log.Printf("failed to update %s: %v", group.Metadata.Name, err)
            continue
        }
        log.Printf("updated %s", group.Metadata.Name)
    }

    return nil
}
```

### Example 3: Monitor WireGuard Peers

```go
func monitorPeers(c *client.Client) {
    ctx := context.Background()
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for range ticker.C {
        peers, err := c.GetWireGuardPeers(ctx)
        if err != nil {
            log.Printf("failed to list peers: %v", err)
            continue
        }

        for _, peer := range peers {
            if peer.Status.Phase != "Ready" {
                log.Printf("peer %s is %s: %s",
                    peer.Metadata.UID,
                    peer.Status.Phase,
                    peer.Status.Message,
                )
            }
        }
    }
}
```

### Example 4: Template Validation

```go
func validateTemplate(template string) error {
    // This would use the same validation logic as the server
    // For now, check basic syntax
    if !strings.HasPrefix(template, "#cloud-config") {
        return fmt.Errorf("template must start with #cloud-config")
    }

    // Check for balanced braces
    openBraces := strings.Count(template, "{{")
    closeBraces := strings.Count(template, "}}")
    if openBraces != closeBraces {
        return fmt.Errorf("unbalanced template braces")
    }

    return nil
}

func createGroupWithValidation(c *client.Client, name, template string) error {
    if err := validateTemplate(template); err != nil {
        return fmt.Errorf("template validation: %w", err)
    }

    ctx := context.Background()
    _, err := c.CreateGroup(ctx, client.CreateGroupRequest{
        Metadata: v1.Metadata{Name: name},
        Spec: v1.GroupSpec{
            Template: template,
        },
    })
    return err
}
```

---

## Related Documentation

- [Architecture Overview](./ARCHITECTURE.md)
- [Cloud-Init Endpoints](../CLOUDINIT.md)
- [Deployment Guide](./DEPLOYMENT.md)
- [Troubleshooting](./TROUBLESHOOTING.md)
- [API Reference](../README.md#api-surface)
