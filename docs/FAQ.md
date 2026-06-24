<!--
SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

SPDX-License-Identifier: MIT
-->

# Frequently Asked Questions

## Table of Contents

- [General](#general)
- [Getting Started](#getting-started)
- [Configuration](#configuration)
- [Cloud-Init](#cloud-init)
- [Templates](#templates)
- [SMD Integration](#smd-integration)
- [WireGuard](#wireguard)
- [Storage](#storage)
- [Performance](#performance)
- [Security](#security)

---

## General

### What is the OpenCHAMI metadata service?

The metadata service is a NoCloud-compatible cloud-init server designed for HPC environments. It provides dynamic node configuration during boot by serving cloud-init metadata, user-data, vendor-data, and network-config endpoints. It integrates with SMD for node identity and supports group-based templating for flexible configuration management.

### How is this different from cloud provider metadata services?

Traditional cloud provider metadata services (AWS, Azure, GCP) provide instance-specific data based on the cloud platform's knowledge. The OpenCHAMI metadata service:
- Works in bare-metal and on-premise HPC environments
- Uses SMD as the source of truth for node identity
- Supports group-based templating for HPC-specific workflows
- Provides WireGuard VPN bootstrap for secure management channels

### What is NoCloud?

NoCloud is a cloud-init datasource that doesn't require a network metadata service. It can read configuration from local files or network endpoints. The metadata service implements the NoCloud network endpoint pattern, making it compatible with standard cloud-init installations.

### Do I need to use cloud-init?

Yes, nodes must have cloud-init installed and configured to use the NoCloud datasource. Most modern Linux distributions include cloud-init by default.

---

## Getting Started

### How do I get started with the metadata service?

1. **Start with mock SMD:**
   ```bash
   go run ./cmd/server/main.go serve --port 8888 --mock-smd
   ```

2. **Test the endpoints:**
   ```bash
   curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/meta-data
   ```

3. **Create resources:**
   ```bash
   # See examples/demo.sh for complete setup
   ./examples/demo.sh
   ```

4. **Move to production:** Configure SMD_URL and authentication

See [README Quick Start](../README.md#quick-start) for detailed steps.

### When should I use mock SMD vs real SMD?

**Use Mock SMD when:**
- Developing templates locally
- Testing the service without infrastructure
- Learning the API
- Writing integration tests

**Use Real SMD when:**
- Deploying to production
- Testing with actual hardware
- Validating node identity resolution
- Testing group membership from SMD

### What are the minimum required resources?

To get meaningful cloud-init output, you need:
1. **ClusterDefaults** - Provides cluster-wide settings (SSH keys, naming convention)
2. **Groups** - Define templates for node groups

Optional:
- **InstanceInfo** - Per-node overrides
- **WireGuardPeer** - VPN bootstrap (if using WireGuard)

---

## Configuration

### What environment variables are required?

**Minimum for production:**
```bash
SMD_URL=http://smd.example.com:27779
SMD_JWT=your-token-here
```

**With TokenSmith:**
```bash
SMD_URL=https://smd.example.com
TOKENSMITH_URL=https://tokensmith.example.com
TOKENSMITH_SERVICE_IDENTITY_CERT=/path/to/cert.crt
TOKENSMITH_SERVICE_IDENTITY_KEY=/path/to/key.key
```

See [Configuration Reference](./DEPLOYMENT.md#configuration-reference) for complete list.

### How do I configure authentication?

Three modes:

1. **Mock mode** (development only):
   ```bash
   ./server serve --mock-smd
   ```

2. **Static token** (simple):
   ```bash
   export SMD_JWT="your-token"
   ./server serve
   ```

3. **Dynamic token** (recommended for production):
   ```bash
   export TOKENSMITH_URL=https://tokensmith.example.com
   export TOKENSMITH_SERVICE_IDENTITY_CERT=/path/to/cert.crt
   export TOKENSMITH_SERVICE_IDENTITY_KEY=/path/to/key.key
   ./server serve
   ```

See [TokenSmith Integration](./tokensmith-integration.md) for details.

### Where is data stored?

By default: `/data` (absolute path)

**Note:** The default changed from relative `./data` to absolute `/data` to avoid issues with read-only root filesystems in containers.

**Change with:**
```bash
./server serve --data-dir=/var/lib/metadata-service
```

**In containers:**
- Docker: Mount volume at `/data` or override with `--data-dir`
- Kubernetes: Use PersistentVolumeClaim mounted at `/data`

See [Storage](./DEPLOYMENT.md#backup-and-recovery) for backup strategies.

---

## Cloud-Init

### How does node identity resolution work?

1. Extract client IP from request (honors `X-Forwarded-For` header)
2. If IP is in WireGuard range, look up peer → component mapping
3. Otherwise, query SMD for component by IP
4. Fetch component details, Ethernet interfaces, and group memberships
5. Build metadata and render templates

See [Architecture - Request Flow](./ARCHITECTURE.md#request-flow) for diagram.

### What if a node has multiple network interfaces?

The service prefers HMN (Hardware Management Network) interfaces when available. If multiple interfaces exist, the one marked as HMN is used for identity and MAC address in metadata.

### How do I test cloud-init locally?

```bash
# Start server with mock SMD
./server serve --port 8888 --mock-smd

# Simulate node request
curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/meta-data

# Mock nodes available:
# x1000c0s0b0n0 - 10.252.0.26 (groups: compute, green)
# x1000c0s0b0n1 - 10.252.0.27 (groups: compute, blue)
# x1000c0s1b0n0 - 10.252.0.28 (groups: storage)
```

### What happens if a node is not found in SMD?

The `/meta-data` endpoint returns HTTP 404 with message "node not found". Check:
- Node is registered in SMD
- IP address matches SMD records
- SMD is reachable from the service
- `X-Forwarded-For` header is set correctly (if behind proxy)

See [Troubleshooting - Node Identity](./TROUBLESHOOTING.md#node-identity-resolution) for debug steps.

### Can I use this without SMD?

Not in production. SMD is the source of truth for node identity and group membership. For development, use `--mock-smd` which provides a built-in mock SMD client with sample data.

---

## Templates

### What templating language is used?

[Pongo2](https://github.com/flosch/pongo2) - a Django-syntax inspired template engine for Go. It's similar to Jinja2 but with Go-specific features.

**Example:**
```yaml
#cloud-config
hostname: {{ hostname }}
write_files:
  - path: /etc/config
    content: |
      NID={{ nid }}
      ROLE={{ role }}
      {% if scheduler %}
      SCHEDULER={{ scheduler }}
      {% endif %}
```

### What variables are available in templates?

**Flat keys:**
- `hostname`, `local_hostname`, `instance_id`
- `cluster_name`, `cloud_provider`, `region`
- `nid`, `role`, `mac`, `ip`
- `interfaces` (array of interface objects)
- `public_keys` (array of SSH public keys)

**Nested objects:**
- `vendor_data` - Full vendor_data structure from `/meta-data`
- `meta_data` - Full cloud-init metadata document
- Custom keys from `Group.Spec.MetaData`

See [Cloud-Init Template Context](../CLOUDINIT.md#template-context) for complete reference.

### How do I debug template rendering?

1. **Enable debug logging:**
   ```bash
   export LOG_LEVEL=debug
   ./server serve
   ```

2. **Check logs for template context:**
   ```
   level=debug msg="building template context" hostname="tc1000" nid=1000
   ```

3. **Test template validation:**
   ```bash
   # Create group with invalid template
   # Server will reject with validation error
   ```

4. **Fetch rendered output:**
   ```bash
   curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/compute.yaml
   ```

See [Troubleshooting - Template Errors](./TROUBLESHOOTING.md#template-rendering-errors) for common issues.

### Can I use conditionals and loops in templates?

Yes, Pongo2 supports:

**Conditionals:**
```yaml
{% if role == "compute" %}
scheduler: slurm
{% elif role == "storage" %}
filesystem: lustre
{% else %}
type: other
{% endif %}
```

**Loops:**
```yaml
interfaces:
{% for iface in interfaces %}
  - name: {{ iface.name }}
    mac: {{ iface.mac }}
    ip: {{ iface.ip }}
{% endfor %}
```

**Filters:**
```yaml
hostname: {{ hostname|upper }}
nid: {{ nid|default:"0000" }}
```

### How are templates validated?

At create/update time, the service:
1. Parses template with Pongo2
2. Renders against sample metadata
3. Validates output is valid YAML
4. Rejects if any step fails

This prevents deploying broken templates that would fail at boot time.

---

## SMD Integration

### How often does the service query SMD?

- **On-demand:** When a cloud-init request arrives (cached for 5 minutes)
- **Background sync:** Every 60 seconds (configurable with `--smd-sync-interval`)

Cache reduces SMD load and improves response times.

### What if SMD is unavailable?

- **During startup:** Service starts but health check shows `smd_client: "disconnected"`
- **During requests:** Cached data is used if available, otherwise requests fail with 500
- **Background sync:** Retries automatically on next interval

See [Troubleshooting - SMD Issues](./TROUBLESHOOTING.md#smd-integration-issues) for recovery steps.

### How do I reduce SMD query load?

1. **Enable background sync:**
   ```bash
   ./server serve --smd-sync-enabled --smd-sync-interval=60
   ```

2. **Increase cache TTL** (requires code change):
   ```go
   // pkg/smdclient/http_client.go
   const defaultCacheTTL = 10 * time.Minute
   ```

3. **Deploy multiple service instances** (requires shared cache - future enhancement)

### Can I use a different identity source besides SMD?

Not currently. SMD integration is deeply embedded in the architecture. Future enhancements could support pluggable identity providers.

---

## WireGuard

### What is WireGuard used for?

WireGuard provides a secure VPN channel for:
- Management traffic to nodes without physical network access
- Bootstrap channel before physical networking is configured
- Secure phone-home for de-registration

**Not for:** High-bandwidth data plane traffic (use physical networking)

### How do I enable WireGuard?

```bash
./server serve --wireguard-server=100.97.0.1/16
```

This:
- Starts userspace WireGuard controller
- Allocates IPs from 100.97.0.0/16 range
- Exposes `/wg-init` and `/phone-home/{id}` endpoints

### How does a node bootstrap WireGuard?

1. **Generate keypair:**
   ```bash
   wg genkey | tee private.key | wg pubkey > public.key
   ```

2. **Request allocation:**
   ```bash
   curl -X POST http://metadata-service:8080/wg-init \
     -H "Content-Type: application/json" \
     -d "{\"public_key\": \"$(cat public.key)\"}"
   ```

3. **Configure WireGuard:**
   ```ini
   [Interface]
   PrivateKey = <from private.key>
   Address = <client-vpn-ip from response>

   [Peer]
   PublicKey = <server-public-key from response>
   Endpoint = <server-ip>:<server-port>
   AllowedIPs = 100.97.0.0/16
   ```

See [WireGuard Testing Guide](./wireguard.md) for complete flow.

### What is phone-home?

Phone-home is the de-registration endpoint. When a node shuts down or is decommissioned, it should call:

```bash
POST /phone-home/{peer-uid}
```

This deletes the WireGuard peer and frees the IP for reuse.

### Can I use kernel WireGuard instead of userspace?

Not currently. The service uses [wireguard-go](https://git.zx2c4.com/wireguard-go/) (userspace implementation) for portability and dynamic management. Kernel WireGuard requires root privileges and is harder to manage programmatically.

---

## Storage

### How are resources stored?

Resources are stored as JSON files in the data directory:

```
/data/
├── clusterdefaults/
│   ├── clusterdefaults-abc123.json
│   └── clusterdefaults-def456.json
├── groups/
│   ├── group-111.json
│   └── group-222.json
├── instanceinfos/
└── wireguardpeers/
```

Each file contains a single resource with metadata and spec.

### How do I backup the data directory?

```bash
# Stop service (or use volume snapshot)
kubectl scale deployment/metadata-service --replicas=0 -n openchami

# Backup
tar -czf metadata-backup-$(date +%Y%m%d).tar.gz -C /data .

# Restart
kubectl scale deployment/metadata-service --replicas=1 -n openchami
```

See [Backup and Recovery](./DEPLOYMENT.md#backup-and-recovery) for platform-specific methods.

### What happens if I have multiple resources with the same name?

This is expected behavior. Resources are versioned by UID:
- Each create/update generates a new UID
- Queries return the "latest by name" (most recent `metadata.updated` timestamp)
- Old versions remain on disk until manually deleted

**Cleanup:**
```bash
# List all versions
curl http://localhost:8080/groups | jq '.items[] | select(.metadata.name == "compute")'

# Delete old versions
curl -X DELETE http://localhost:8080/groups/{old-uid}
```

### Can I use a database instead of file storage?

Not currently. The storage layer is designed for file-based persistence. Future enhancements may support PostgreSQL or other backends.

### What are the storage limits?

File-based storage is suitable for:
- Up to 10,000 resources
- Up to 1MB per resource
- Single-instance deployment

For larger deployments, consider:
- Regular cleanup of old resource versions
- Database backend (future)
- Horizontal scaling with shared storage (future)

---

## Performance

### How fast are cloud-init requests?

**Typical latencies:**
- With cache: 5-20ms
- Without cache: 50-200ms (depends on SMD latency)

**Optimization:**
- Enable background sync to warm cache
- Increase cache TTL
- Simplify templates
- Deploy service close to nodes (same datacenter)

### How many nodes can the service support?

**Single instance:**
- 1,000-10,000 nodes (depends on request rate)
- 100 requests/second

**Bottlenecks:**
- SMD query latency
- Template rendering (CPU-bound)
- Storage list operations (linear scan)

**Scaling strategies:**
- Deploy multiple instances behind load balancer
- Use shared cache (Redis - future)
- Database backend (future)

### How do I monitor performance?

**Current:**
- Check logs for request duration: `level=debug msg="request completed" duration="15ms"`
- Monitor health endpoint: `curl http://localhost:8080/health`

**Future:**
- Prometheus metrics (planned)
- Distributed tracing with OpenTelemetry (planned)

---

## Security

### How is authentication handled?

**Service → SMD:**
- Static JWT token (SMD_JWT)
- Or dynamic token via TokenSmith (recommended)

**Client → Service:**
- Cloud-init endpoints: IP-based identity (trusted network assumption)
- Resource API: Bearer token (future)

### Is TLS required?

**Recommended for production:**
- Terminate TLS at load balancer or ingress
- Use HTTPS for SMD and TokenSmith connections
- Encrypt WireGuard traffic (built-in)

**Development:**
- HTTP is acceptable
- Use `--mock-smd` for local testing

### What about secrets in templates?

**Don't store secrets in templates!** Instead:
- Use external secret management (Vault, Kubernetes Secrets)
- Reference secrets by path/URL in templates
- Fetch secrets at runtime via cloud-init scripts

**Example:**
```yaml
#cloud-config
runcmd:
  - curl https://vault.example.com/secret/node-key -o /etc/node-key
```

### How are WireGuard private keys managed?

- Server private key stored in WireGuard state file (consider encrypting)
- Client private keys generated on nodes (never sent to server)
- Only public keys are exchanged

### What are the network security recommendations?

1. **Restrict cloud-init endpoints to HMN:** Use firewall rules
2. **Use WireGuard-only mode:** `--wireguard-only` restricts access to VPN clients
3. **Deploy behind gateway:** Use API gateway for authentication/authorization
4. **Enable TLS:** Terminate TLS at ingress/load balancer
5. **Rotate tokens:** Use TokenSmith for automatic rotation

---

## Related Documentation

- [Architecture Overview](./ARCHITECTURE.md)
- [Deployment Guide](./DEPLOYMENT.md)
- [Troubleshooting](./TROUBLESHOOTING.md)
- [Client Usage](./CLIENT_USAGE.md)
- [Cloud-Init Reference](../CLOUDINIT.md)
