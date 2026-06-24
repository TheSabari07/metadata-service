<!--
SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

SPDX-License-Identifier: MIT
-->

# Architecture Overview

This document describes the high-level architecture of the OpenCHAMI metadata service, how components interact, and key design decisions.

## Table of Contents

- [System Overview](#system-overview)
- [Component Architecture](#component-architecture)
- [Request Flow](#request-flow)
- [Generated vs Hand-Written Code](#generated-vs-hand-written-code)
- [Storage Layer](#storage-layer)
- [Event Bus and Reconciliation](#event-bus-and-reconciliation)
- [Integration Points](#integration-points)
- [Design Decisions](#design-decisions)

---

## System Overview

The metadata service is a NoCloud-compatible cloud-init server that provides dynamic configuration to HPC compute nodes. It bridges three key systems:

1. **SMD (State Management Database)** - Source of truth for node identity, networking, and group membership
2. **File-based Storage** - Persistent storage for cluster defaults, groups, instance overrides, and WireGuard peers
3. **Compute Nodes** - Consumers of cloud-init metadata during boot

```
┌─────────────────────────────────────────────────────────────────┐
│                     Metadata Service                            │
│                                                                 │
│  ┌──────────────┐      ┌──────────────┐      ┌──────────────┐ │
│  │   Cloud-Init │      │   Resource   │      │  WireGuard   │ │
│  │   Endpoints  │      │     API      │      │  Controller  │ │
│  └──────┬───────┘      └──────┬───────┘      └──────┬───────┘ │
│         │                     │                     │          │
│         └─────────────────────┼─────────────────────┘          │
│                               │                                │
│                    ┌──────────▼──────────┐                     │
│                    │   Request Handlers  │                     │
│                    └──────────┬──────────┘                     │
│                               │                                │
│         ┌─────────────────────┼─────────────────────┐          │
│         │                     │                     │          │
│    ┌────▼────┐         ┌──────▼──────┐      ┌──────▼──────┐   │
│    │   SMD   │         │   Storage   │      │ Event Bus & │   │
│    │ Client  │         │   Adapter   │      │ Reconcilers │   │
│    └────┬────┘         └──────┬──────┘      └──────┬──────┘   │
│         │                     │                     │          │
└─────────┼─────────────────────┼─────────────────────┼──────────┘
          │                     │                     │
     ┌────▼────┐           ┌────▼────┐           ┌────▼────┐
     │   SMD   │           │  Disk   │           │ WireGuard│
     │ Service │           │ Storage │           │  State   │
     └─────────┘           └─────────┘           └─────────┘
```

---

## Component Architecture

### Core Components

#### 1. Cloud-Init Endpoints (`pkg/handlers`)

**Purpose:** Serve NoCloud-compatible metadata to booting nodes

**Key Endpoints:**
- `/meta-data` - Instance metadata, hostname, IPs, group membership
- `/user-data` - User-provided configuration (currently empty placeholder)
- `/vendor-data` - Include list of group templates
- `/network-config` - Network configuration derived from SMD
- `/{group}.yaml` - Rendered group templates

**Identity Resolution Flow:**
1. Extract client IP from request (honors `X-Forwarded-For`)
2. Check WireGuard IP mapping if client is on VPN
3. Query SMD for component by IP
4. Fetch component details, Ethernet interfaces, and group memberships
5. Build template context and render response

#### 2. Resource API (`cmd/server/*_handlers_generated.go`)

**Purpose:** CRUD operations for cluster configuration resources

**Generated Resources:**
- `ClusterDefaults` - Cluster-wide settings (SSH keys, naming, region)
- `Group` - Group templates and metadata
- `InstanceInfo` - Per-instance overrides
- `WireGuardPeer` - VPN peer allocations

**Key Features:**
- Fabrica-generated CRUD handlers
- Versioned API (X-API-Version header support)
- Status subresources for reconciliation state
- JSON Patch support

#### 3. SMD Client (`pkg/smdclient`)

**Purpose:** Query node identity, networking, and group membership from SMD

**Capabilities:**
- Component lookup by IP
- Ethernet interface discovery
- Group membership resolution
- Response caching with TTL
- Background sync for cache warming
- Mock implementation for development

**Cache Strategy:**
- Components cached by ID (default 5 min TTL)
- Ethernet interfaces cached by component ID
- Group membership cached per component
- Background sync refreshes cache before expiry

#### 4. Storage Layer (`internal/storage`)

**Purpose:** File-based persistence for resources

**Storage Adapter Pattern:**
- Wraps Fabrica-generated storage interface
- Implements "latest by name" query semantics
- Handles resource versioning via metadata.uid
- Publishes resource events to event bus

**File Layout:**
```
/data/
├── clusterdefaults/
│   └── <uid>.json
├── groups/
│   └── <uid>.json
├── instanceinfos/
│   └── <uid>.json
└── wireguardpeers/
    └── <uid>.json
```

#### 5. WireGuard Controller (`pkg/wireguard`)

**Purpose:** Userspace WireGuard VPN management

**Components:**
- **Controller** - Manages WireGuard device and peer lifecycle
- **Allocator** - IP address allocation from CIDR
- **State Manager** - Persistence and recovery

**Lifecycle:**
1. Bootstrap (`/wg-init`) - Allocate IP, generate config
2. Reconciliation - Sync peers to WireGuard device
3. Phone-home (`/phone-home/{id}`) - De-register and cleanup

See [WireGuard Architecture](./wireguard-architecture.md) for details.

#### 6. Reconciliation Runtime (`cmd/server/reconciliation_runtime.go`)

**Purpose:** Event-driven resource reconciliation

**Architecture:**
- Event bus receives resource create/update/delete events
- Reconcilers subscribe to resource types
- Reconcilers update resource status based on desired state
- Status updates trigger further reconciliation if needed

**Reconcilers:**
- **WireGuardPeerReconciler** - Syncs peers to controller, updates status

**Event Flow:**
```
Storage Adapter
    ↓ (publish event)
Event Bus
    ↓ (route by resource type)
Reconciler
    ↓ (reconcile)
Update Status
    ↓ (publish status event)
Event Bus
    ↓ (route)
Storage Adapter
```

#### 7. Middleware (`internal/middleware`)

**Purpose:** Request preprocessing and access control

**Middleware Chain:**
1. **Logging** - Request/response logging with duration
2. **Versioning** - API version negotiation via header or URL
3. **WireGuard-Only** - Restrict access to VPN clients (optional)
4. **CORS** - Cross-origin support (if enabled)

---

## Request Flow

### Cloud-Init Request Flow

```
Compute Node
    ↓ (GET /meta-data)
Middleware Chain
    ↓ (extract IP, version)
MetaDataHandler
    ↓
Identity Resolution
    ├─→ Check WireGuard IP mapping
    └─→ Query SMD by IP
    ↓
Fetch Resources
    ├─→ ClusterDefaults (latest)
    ├─→ InstanceInfo (by component ID)
    └─→ Groups (by membership)
    ↓
Build Template Context
    ├─→ Flatten keys (hostname, nid, ip, etc.)
    ├─→ Merge group metadata
    └─→ Build vendor_data structure
    ↓
Render Response (YAML)
    ↓
Return to Node
```

### Resource Create Flow

```
Client (CLI/API)
    ↓ (POST /groups)
Middleware Chain
    ↓
CreateGroupHandler (generated)
    ↓
Validate Request
    ├─→ Check required fields
    ├─→ Validate template syntax (Pongo2)
    └─→ Validate template output (YAML)
    ↓
Storage Adapter
    ├─→ Generate UID
    ├─→ Write to disk
    └─→ Publish CREATE event
    ↓
Event Bus
    ↓
(No reconciler for Group)
    ↓
Return Response
```

### WireGuard Bootstrap Flow

```
Node
    ↓ (POST /wg-init with public_key)
WireGuardInitHandler
    ↓
Check for existing peer by public key
    ├─→ Found: Return existing config (idempotent)
    └─→ Not found: Continue
    ↓
Allocate IP from Controller
    ↓
Create WireGuardPeer resource
    ↓
Storage Adapter
    ├─→ Write to disk
    └─→ Publish CREATE event
    ↓
Event Bus → WireGuardPeerReconciler
    ├─→ Add peer to controller
    ├─→ Update status.phase = Ready
    └─→ Persist controller state
    ↓
Return config to node
    ├─→ client-vpn-ip
    ├─→ server-public-key
    ├─→ server-ip
    └─→ server-port
```

---

## Generated vs Hand-Written Code

### Generated Code (via Fabrica)

**Location:** `cmd/server/*_generated.go`

**Generated Components:**
- Resource type definitions
- CRUD handler functions
- OpenAPI spec fragments
- Storage interface
- Client library

**Generation Command:**
```bash
make generate
```

**Source:** `apis.yaml` and `.fabrica.yaml`

### Hand-Written Code

**Extensions:**
- `cmd/server/server_extensions.go` - Custom route registration
- `cmd/server/openapi_extensions.go` - OpenAPI customizations
- `cmd/server/*_routes.go` - Cloud-init and WireGuard endpoints
- `pkg/handlers/` - Request handlers
- `pkg/wireguard/` - WireGuard controller
- `pkg/reconcilers/` - Reconciliation logic

**Integration Pattern:**
```go
// server_extensions.go
func registerCustomServerIntegrations(router chi.Router, backend storage.Backend) {
    // Register generated routes first
    RegisterGeneratedRoutes(router, backend)
    
    // Add custom cloud-init routes
    registerCloudInitRoutes(router, backend)
    
    // Add WireGuard routes if enabled
    if wireguardEnabled {
        registerWireGuardRoutes(router, backend, controller)
    }
}
```

---

## Storage Layer

### Storage Adapter Pattern

The storage adapter wraps the Fabrica-generated storage interface and adds:
- Event publishing for resource changes
- "Latest by name" query semantics
- Resource validation hooks

**Interface:**
```go
type StorageAdapter interface {
    // Fabrica-generated CRUD
    SaveClusterDefaults(ctx context.Context, resource *v1.ClusterDefaults) error
    LoadClusterDefaults(ctx context.Context, uid string) (*v1.ClusterDefaults, error)
    LoadAllClusterDefaults(ctx context.Context) ([]*v1.ClusterDefaults, error)
    DeleteClusterDefaults(ctx context.Context, uid string) error
    
    // Custom queries
    GetClusterDefaults(ctx context.Context) (*v1.ClusterDefaults, error)  // latest
    GetGroupData(ctx context.Context, name string) (*v1.Group, error)     // latest by name
    GetInstanceInfoByName(ctx context.Context, name string) (*v1.InstanceInfo, error)
}
```

### Query Semantics

**Latest by Name:**
- Multiple resources can exist with the same `metadata.name`
- Queries return the resource with the most recent `metadata.updated` timestamp
- Enables versioning without breaking existing references

**Example:**
```json
// First version
{"metadata": {"name": "compute", "uid": "group-001", "updated": "2026-01-01T00:00:00Z"}}

// Updated version (latest)
{"metadata": {"name": "compute", "uid": "group-002", "updated": "2026-01-02T00:00:00Z"}}

// GetGroupData("compute") returns group-002
```

---

## Event Bus and Reconciliation

### Event Bus Architecture

**Purpose:** Decouple resource storage from reconciliation logic

**Event Types:**
```go
type ResourceEventAction string

const (
    ResourceCreated ResourceEventAction = "CREATED"
    ResourceUpdated ResourceEventAction = "UPDATED"
    ResourceDeleted ResourceEventAction = "DELETED"
)

type ResourceEvent struct {
    Action       ResourceEventAction
    ResourceType string  // "WireGuardPeer", "Group", etc.
    UID          string
    Name         string
}
```

**Event Flow:**
1. Storage adapter publishes event after disk write
2. Event bus routes to subscribers by resource type
3. Reconcilers process events asynchronously
4. Status updates create new events

### Reconciler Pattern

**Interface:**
```go
type Reconciler interface {
    Reconcile(ctx context.Context, event ResourceEvent) error
    ResourceType() string
}
```

**Reconciliation Loop:**
```
1. Receive event
2. Load resource from storage
3. Compare desired state (spec) vs actual state (status)
4. Take action to converge actual → desired
5. Update status
6. Repeat if status changed
```

**Example: WireGuardPeerReconciler**
```
Event: WireGuardPeer CREATED
    ↓
Load peer from storage
    ↓
Check controller state
    ├─→ Peer exists: Skip
    └─→ Peer missing: Add to controller
    ↓
Update status.phase = "Ready"
    ↓
Save status → triggers UPDATE event
    ↓
(No further action needed)
```

---

## Integration Points

### SMD Integration

**Purpose:** Node identity, networking, and group membership

**Endpoints Used:**
- `GET /hsm/v2/Inventory/EthernetInterfaces` - IP to component lookup
- `GET /hsm/v2/State/Components/{id}` - Component details
- `GET /hsm/v2/groups/{id}/members` - Group membership

**Authentication:**
- Static mode: `SMD_JWT` environment variable
- Dynamic mode: TokenSmith service identity or bootstrap token

**Mock Mode:**
- Enabled with `--mock-smd` flag
- Provides 3 sample nodes with pre-configured groups
- Useful for development and testing

See [SMD Integration](./smd-integration.md) for details.

### TokenSmith Integration

**Purpose:** Dynamic OAuth2 token acquisition for SMD authentication

**Flow:**
1. Service starts with mTLS cert or bootstrap token
2. Exchange for short-lived access token
3. Use token for SMD API calls
4. Refresh before expiry (with configurable skew)
5. Retry on failure with exponential backoff

**Configuration:**
- `tokensmith_url` - TokenSmith service URL
- `tokensmith_service_identity_cert/key/ca` - mTLS credentials
- `tokensmith_bootstrap_token` - Fallback bootstrap token
- `tokensmith_target_service` - Target service (default: "smd")
- `tokensmith_refresh_skew_sec` - Refresh before expiry (default: 300s)

See [TokenSmith Integration](./tokensmith-integration.md) for details.

### Fabrica Integration

**Purpose:** Resource API generation and storage interface

**Generated Artifacts:**
- Resource type definitions with validation tags
- CRUD handler functions with OpenAPI annotations
- Storage interface and file-based implementation
- Client library with typed methods
- OpenAPI specification

**Customization:**
- `apis.yaml` - Resource definitions
- `.fabrica.yaml` - Generation configuration
- Extension hooks in `server_extensions.go`

---

## Design Decisions

### Why NoCloud?

NoCloud is a cloud-init datasource that works without network metadata services. It's ideal for HPC environments where:
- Nodes may not have internet access
- Custom metadata is required per node/group
- Template-based configuration is preferred over static files

### Why File-Based Storage?

File-based storage provides:
- Simple deployment (no database required)
- Easy backup/restore (copy directory)
- Human-readable resources (JSON files)
- Version control friendly
- Low operational overhead

Trade-offs:
- Not suitable for >10k resources
- No transactional guarantees
- Manual cleanup of old versions

### Why Event-Driven Reconciliation?

Event-driven reconciliation enables:
- Decoupling of storage and business logic
- Asynchronous processing (non-blocking)
- Status tracking (desired vs actual state)
- Extensibility (add reconcilers without changing storage)

Trade-offs:
- More complex than synchronous processing
- Requires careful event ordering
- Debugging is harder (async traces)

### Why Userspace WireGuard?

Userspace WireGuard allows:
- No kernel module dependency
- Portable across container runtimes
- Dynamic peer management via API
- State persistence and recovery

Trade-offs:
- Lower throughput than kernel WireGuard
- Higher CPU usage
- Not suitable for high-bandwidth scenarios

Use case: Secure bootstrap channel for cloud-init, not production data plane.

### Why Template Validation at Create Time?

Validating templates at create time (not render time) provides:
- Fail-fast feedback to operators
- Prevents deploying broken templates
- Reduces runtime errors on booting nodes

Validation strategy:
1. Parse template with Pongo2
2. Render against sample metadata
3. Validate output is valid YAML
4. Reject if any step fails

---

## Observability

### Logging

Structured logging with `zerolog`:
- Request/response logging with duration
- Identity resolution steps
- SMD cache hits/misses
- WireGuard peer lifecycle events
- Reconciliation actions

### Health Checks

`GET /health` returns:
```json
{
  "status": "healthy",
  "smd_client": "connected",
  "storage": "ready",
  "wireguard": "enabled"
}
```

### Metrics

Currently not implemented. Future consideration:
- Prometheus metrics for request rates, latency, errors
- SMD cache hit ratio
- WireGuard peer count
- Storage operation latency

---

## Security Considerations

### Authentication

- **Cloud-init endpoints:** IP-based identity (trusted network assumption)
- **Resource API:** Bearer token (future: integrate with TokenSmith)
- **WireGuard-only mode:** Restrict to VPN clients only

### Authorization

Currently not implemented. All authenticated requests have full access.

Future consideration:
- RBAC for resource API
- Group-based access control for templates

### Secrets Management

- SMD tokens stored in environment variables
- WireGuard private keys stored in state file (consider encryption)
- TokenSmith certificates mounted from external secrets

### Network Security

- WireGuard provides encrypted channel
- Recommend firewall rules to restrict cloud-init endpoints to HMN
- TLS termination at gateway/load balancer

---

## Performance Characteristics

### Request Latency

Typical latencies (uncached):
- `/meta-data`: 50-100ms (1 SMD query)
- `/network-config`: 50-100ms (1 SMD query)
- `/{group}.yaml`: 100-200ms (1 SMD query + template render)

With cache:
- `/meta-data`: 5-10ms
- `/network-config`: 5-10ms
- `/{group}.yaml`: 10-20ms

### Storage Performance

File-based storage:
- Create: 1-5ms (write + fsync)
- Read: 1-2ms (cached by OS)
- List: 10-50ms (depends on file count)
- Delete: 1-5ms

### Scalability

Current architecture suitable for:
- Up to 10,000 nodes
- Up to 1,000 groups
- Up to 100 requests/second

Bottlenecks:
- SMD query latency (mitigated by caching)
- Template rendering (CPU-bound)
- Storage list operations (linear scan)

---

## Future Enhancements

### Short Term
- [ ] Prometheus metrics
- [ ] Distributed tracing (OpenTelemetry)
- [ ] Resource API authentication
- [ ] Enhanced template validation (lint, security checks)

### Medium Term
- [ ] Database backend option (PostgreSQL)
- [ ] Multi-tenant isolation
- [ ] Template library and sharing
- [ ] Configuration drift detection

### Long Term
- [ ] Horizontal scaling (leader election, distributed cache)
- [ ] Advanced templating (Jinja2, CUE)
- [ ] Policy engine integration (OPA)
- [ ] Observability dashboard

---

## Related Documentation

- [Cloud-Init Endpoints](../CLOUDINIT.md)
- [WireGuard Architecture](./wireguard-architecture.md)
- [TokenSmith Integration](./tokensmith-integration.md)
- [SMD Integration](./smd-integration.md)
- [Deployment Guide](./DEPLOYMENT.md)
- [Client Usage](./CLIENT_USAGE.md)
- [Troubleshooting](./TROUBLESHOOTING.md)
