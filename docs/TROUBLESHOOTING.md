<!--
SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

SPDX-License-Identifier: MIT
-->

# Troubleshooting Guide

This guide helps diagnose and resolve common issues with the OpenCHAMI metadata service.

## Table of Contents

- [General Debugging](#general-debugging)
- [Service Startup Issues](#service-startup-issues)
- [Node Identity Resolution](#node-identity-resolution)
- [Template Rendering Errors](#template-rendering-errors)
- [SMD Integration Issues](#smd-integration-issues)
- [TokenSmith Authentication](#tokensmith-authentication)
- [WireGuard Issues](#wireguard-issues)
- [Storage Problems](#storage-problems)
- [Performance Issues](#performance-issues)
- [Known Issues](#known-issues)

---

## General Debugging

### Enable Debug Logging

```bash
# Environment variable
export LOG_LEVEL=debug

# Command-line flag
./server serve --log-level=debug

# Kubernetes
kubectl set env deployment/metadata-service LOG_LEVEL=debug -n openchami
```

### Check Service Health

```bash
curl http://localhost:8080/health
```

Expected response:
```json
{
  "status": "healthy",
  "smd_client": "connected",
  "storage": "ready",
  "wireguard": "enabled"
}
```

**Unhealthy States:**
- `smd_client: "disconnected"` → SMD unreachable
- `storage: "error"` → Data directory issues
- `wireguard: "disabled"` → WireGuard not configured (not an error if unused)

### View Logs

```bash
# Docker
docker logs metadata-service

# Kubernetes
kubectl logs -f deployment/metadata-service -n openchami

# Systemd
journalctl -u metadata-service -f

# Podman
podman logs -f metadata-service
```

### Test Basic Endpoints

```bash
# Health check
curl http://localhost:8080/health

# OpenAPI spec
curl http://localhost:8080/openapi.json

# Swagger UI (browser)
open http://localhost:8080/docs

# Cloud-init metadata (requires valid node IP)
curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8080/meta-data
```

---

## Service Startup Issues

### Problem: Service exits immediately

**Symptoms:**
- Container restarts continuously
- Exit code 1 or 2
- No logs or minimal logs

**Common Causes:**

#### 1. Data directory not writable

**Check:**
```bash
# Docker/Podman
docker exec metadata-service ls -la /data

# Kubernetes
kubectl exec deployment/metadata-service -n openchami -- ls -la /data
```

**Fix:**
```bash
# Docker - ensure volume is mounted
docker run -v metadata-data:/data ...

# Kubernetes - check PVC status
kubectl get pvc metadata-service-data -n openchami

# Fix permissions (if needed)
kubectl exec deployment/metadata-service -n openchami -- chmod 755 /data
```

#### 2. Invalid configuration

**Check logs for:**
```
level=error msg="invalid configuration"
level=error msg="required flag not set"
```

**Fix:**
- Verify all required environment variables are set
- Check for typos in flag names (use `--data-dir`, not `--data_dir`)
- Ensure SMD_URL is set (unless using `--mock-smd`)

#### 3. Port already in use

**Check logs for:**
```
level=error msg="bind: address already in use"
```

**Fix:**
```bash
# Change port
./server serve --port=8888

# Or stop conflicting service
lsof -i :8080
kill <PID>
```

### Problem: Service starts but health check fails

**Check health endpoint:**
```bash
curl -v http://localhost:8080/health
```

**Possible responses:**

#### HTTP 200 but status "unhealthy"

**Check response body:**
```json
{
  "status": "unhealthy",
  "smd_client": "disconnected",
  "storage": "ready"
}
```

**Action:** Fix SMD connectivity (see [SMD Integration Issues](#smd-integration-issues))

#### HTTP 500 Internal Server Error

**Check logs for:**
```
level=error msg="health check failed"
```

**Action:** Review detailed error in logs, likely storage or SMD initialization failure

#### Connection refused

**Possible causes:**
- Service not started
- Wrong port
- Firewall blocking connection

**Fix:**
```bash
# Verify service is running
ps aux | grep server

# Check listening ports
netstat -tlnp | grep 8080

# Test from inside container
kubectl exec deployment/metadata-service -n openchami -- wget -O- http://localhost:8080/health
```

---

## Node Identity Resolution

### Problem: "node not found" errors

**Symptom:** `/meta-data` returns 404 with message "node not found"

**Debug steps:**

#### 1. Verify request IP

**Check logs for:**
```
level=debug msg="resolving identity" ip="10.252.0.26"
```

**Ensure IP matches:**
- Physical node HMN IP (from SMD)
- Or WireGuard VPN IP (if using WireGuard)

#### 2. Check SMD IP lookup

**Test SMD directly:**
```bash
# Replace with your SMD URL and node IP
curl "$SMD_URL/hsm/v2/Inventory/EthernetInterfaces?IPAddress=10.252.0.26"
```

**Expected response:**
```json
[
  {
    "ID": "x1000c0s0b0n0",
    "Description": "Node Management Network",
    "MACAddress": "b4:2e:99:be:1a:6d",
    "IPAddresses": [{"IPAddress": "10.252.0.26"}],
    "ComponentID": "x1000c0s0b0n0"
  }
]
```

**If empty response:**
- Node not registered in SMD
- IP address mismatch
- SMD data stale

**Fix:**
- Register node in SMD
- Update SMD Ethernet interface data
- Force SMD sync: `curl -X POST $SMD_URL/hsm/v2/Inventory/Discover`

#### 3. Check X-Forwarded-For header

**When behind proxy/load balancer:**
```bash
# Ensure X-Forwarded-For is passed through
curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8080/meta-data
```

**Proxy configuration examples:**

**Nginx:**
```nginx
location / {
    proxy_pass http://metadata-service:8080;
    proxy_set_header X-Forwarded-For $remote_addr;
}
```

**HAProxy:**
```
option forwardfor
```

**Kubernetes Ingress:**
```yaml
nginx.ingress.kubernetes.io/use-forwarded-headers: "true"
```

#### 4. Check WireGuard IP mapping

**If using WireGuard:**
```bash
# Check if client IP is WireGuard VPN address
# Logs should show:
level=debug msg="checking WireGuard IP mapping" ip="100.97.0.2"

# Verify WireGuard peer exists
curl http://localhost:8080/wireguardpeers | jq '.items[] | select(.spec.allowedIP == "100.97.0.2/32")'
```

**Fix:**
- Ensure node completed WireGuard bootstrap (`/wg-init`)
- Check WireGuard peer status is "Ready"
- Verify phone-home didn't de-register peer

### Problem: Wrong node identity returned

**Symptom:** `/meta-data` returns data for different node than expected

**Possible causes:**

#### 1. Multiple interfaces with same IP

**Check SMD for duplicate IPs:**
```bash
curl "$SMD_URL/hsm/v2/Inventory/EthernetInterfaces" | \
  jq '.[] | select(.IPAddresses[].IPAddress == "10.252.0.26")'
```

**Fix:**
- Remove duplicate entries from SMD
- Use unique IPs per interface

#### 2. Cached stale data

**Check cache status in logs:**
```
level=debug msg="SMD cache hit" component="x1000c0s0b0n0"
```

**Force cache refresh:**
```bash
# Restart service
kubectl rollout restart deployment/metadata-service -n openchami

# Or wait for cache TTL (default 5 minutes)
```

---

## Template Rendering Errors

### Problem: Template validation fails at create time

**Symptom:** `POST /groups` returns 400 with validation error

**Common errors:**

#### 1. Invalid Pongo2 syntax

**Error message:**
```json
{
  "error": "template validation failed: unexpected token at line 5"
}
```

**Check template:**
```yaml
template: |
  #cloud-config
  hostname: {{ hostname }}
  write_files:
    - path: /etc/config
      content: |
        VALUE={{ value }  # Missing closing brace
```

**Fix:**
```yaml
template: |
  #cloud-config
  hostname: {{ hostname }}
  write_files:
    - path: /etc/config
      content: |
        VALUE={{ value }}  # Corrected
```

#### 2. Invalid YAML output

**Error message:**
```json
{
  "error": "template validation failed: rendered output is not valid YAML"
}
```

**Common causes:**
- Missing quotes around strings with special characters
- Incorrect indentation
- Unclosed brackets/braces

**Debug:**
```bash
# Test template locally with sample context
cat > test-template.txt <<'EOF'
#cloud-config
hostname: {{ hostname }}
write_files:
  - path: /etc/test
    content: |
      IP={{ ip }}
EOF

# Render with sample data (using pongo2 CLI or custom script)
# Verify output is valid YAML
```

#### 3. Undefined variable reference

**Error message:**
```json
{
  "error": "template validation failed: undefined variable 'custom_field'"
}
```

**Fix:**
- Ensure variable is in template context (see [Template Context](../CLOUDINIT.md#template-context))
- Or provide default: `{{ custom_field|default:"fallback" }}`

### Problem: Template renders but produces unexpected output

**Debug steps:**

#### 1. Check template context

**Enable debug logging:**
```bash
export LOG_LEVEL=debug
```

**Look for:**
```
level=debug msg="building template context" hostname="tc1000" nid=1000 ip="10.252.0.26"
```

#### 2. Test template locally

**Fetch group template:**
```bash
curl http://localhost:8080/groups | jq '.items[] | select(.metadata.name == "compute")'
```

**Create test script:**
```go
package main

import (
    "fmt"
    "github.com/flosch/pongo2/v6"
)

func main() {
    tpl, _ := pongo2.FromString(`{{ hostname }}`)
    out, _ := tpl.Execute(pongo2.Context{"hostname": "tc1000"})
    fmt.Println(out)
}
```

#### 3. Compare with expected output

**Fetch rendered template:**
```bash
curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8080/compute.yaml
```

**Check for:**
- Missing variables (empty values)
- Incorrect variable values
- Malformed YAML

---

## SMD Integration Issues

### Problem: SMD queries fail

**Symptoms:**
- Health check shows `smd_client: "disconnected"`
- Logs show `level=error msg="SMD query failed"`

**Debug steps:**

#### 1. Check SMD connectivity

**Test from service:**
```bash
# Docker
docker exec metadata-service wget -O- $SMD_URL/hsm/v2/service/ready

# Kubernetes
kubectl exec deployment/metadata-service -n openchami -- \
  wget -O- $SMD_URL/hsm/v2/service/ready
```

**Expected response:**
```json
{"code":0,"message":"HSM is healthy"}
```

**If connection fails:**
- Check SMD_URL is correct
- Verify network connectivity (DNS, firewall)
- Check SMD service is running

#### 2. Check authentication

**Test with auth token:**
```bash
curl -H "Authorization: Bearer $SMD_JWT" \
  $SMD_URL/hsm/v2/State/Components
```

**If 401 Unauthorized:**
- Token expired → Refresh token or use TokenSmith
- Token invalid → Check token format (should be JWT)
- Token lacks permissions → Request token with required scopes

**If 403 Forbidden:**
- Token valid but insufficient permissions
- Contact SMD administrator for proper scopes

#### 3. Check SMD URL format

**Valid formats:**
```bash
# Bare SMD service (normalized to /hsm/v2)
SMD_URL=http://smd.example.com:27779

# Gateway-mounted (already includes path)
SMD_URL=https://gateway.example.com/apis/smd/hsm/v2
```

**Invalid formats:**
```bash
# Missing protocol
SMD_URL=smd.example.com

# Wrong path
SMD_URL=http://smd.example.com/api
```

### Problem: SMD cache issues

**Symptom:** Stale data returned, or excessive SMD queries

#### Cache miss rate too high

**Check logs for:**
```
level=debug msg="SMD cache miss" component="x1000c0s0b0n0"
```

**Increase cache TTL (requires code change):**
```go
// pkg/smdclient/http_client.go
const defaultCacheTTL = 10 * time.Minute  // Increase from 5 min
```

#### Stale data in cache

**Force cache refresh:**
```bash
# Restart service
kubectl rollout restart deployment/metadata-service -n openchami
```

**Or adjust sync interval:**
```bash
./server serve --smd-sync-interval=30  # Sync every 30 seconds
```

---

## TokenSmith Authentication

### Problem: Token refresh failures

**Symptoms:**
- Logs show `level=error msg="token refresh failed"`
- SMD queries return 401 after service runs for a while

**Debug steps:**

#### 1. Check TokenSmith connectivity

**Test from service:**
```bash
kubectl exec deployment/metadata-service -n openchami -- \
  wget -O- $TOKENSMITH_URL/.well-known/openid-configuration
```

**If connection fails:**
- Check TOKENSMITH_URL is correct
- Verify network connectivity
- Check TokenSmith service is running

#### 2. Check mTLS certificates

**Verify certificate paths:**
```bash
kubectl exec deployment/metadata-service -n openchami -- ls -la /var/run/tokensmith/
```

**Expected files:**
```
-r--r--r-- client.crt
-r-------- client.key
-r--r--r-- ca.crt
```

**Check certificate validity:**
```bash
kubectl exec deployment/metadata-service -n openchami -- \
  openssl x509 -in /var/run/tokensmith/client.crt -noout -dates

# Output should show:
notBefore=Jan  1 00:00:00 2026 GMT
notAfter=Jan  1 00:00:00 2027 GMT  # Must be in future
```

**If certificate expired:**
- Rotate certificates
- Update Kubernetes secret
- Restart service

#### 3. Check bootstrap token

**If using bootstrap token fallback:**
```bash
# Verify token is set
kubectl exec deployment/metadata-service -n openchami -- \
  env | grep TOKENSMITH_BOOTSTRAP_TOKEN
```

**Test token exchange:**
```bash
curl -X POST $TOKENSMITH_URL/oauth/token \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -H "Authorization: Bearer $TOKENSMITH_BOOTSTRAP_TOKEN" \
  -d "grant_type=client_credentials&scope=smd:read"
```

**Expected response:**
```json
{
  "access_token": "eyJ...",
  "token_type": "Bearer",
  "expires_in": 3600
}
```

#### 4. Check refresh timing

**Look for refresh logs:**
```
level=info msg="token refreshed successfully" expires_in=3600
level=warn msg="token refresh scheduled" refresh_in=3300
```

**Adjust refresh skew if needed:**
```bash
# Refresh 10 minutes before expiry
./server serve --tokensmith-refresh-skew-sec=600
```

### Problem: Service identity exchange fails

**Symptom:** Logs show `level=error msg="service identity exchange failed"`

**Debug steps:**

#### 1. Check mTLS handshake

**Enable TLS debug (requires code change):**
```go
import "crypto/tls"

tlsConfig := &tls.Config{
    // ... existing config
    InsecureSkipVerify: false,  // Ensure this is false
    MinVersion: tls.VersionTLS12,
}
```

**Check CA trust:**
```bash
# Verify CA cert matches TokenSmith's issuer
kubectl exec deployment/metadata-service -n openchami -- \
  openssl verify -CAfile /var/run/tokensmith/ca.crt /var/run/tokensmith/client.crt
```

#### 2. Check TokenSmith service identity endpoint

**Test endpoint:**
```bash
curl -X POST $TOKENSMITH_URL/service-identity/session \
  --cert /path/to/client.crt \
  --key /path/to/client.key \
  --cacert /path/to/ca.crt
```

**If 404 Not Found:**
- TokenSmith version doesn't support service identity endpoint
- Fall back to bootstrap token

---

## WireGuard Issues

### Problem: WireGuard initialization fails

**Symptom:** `/wg-init` returns 500 error

**Check logs for:**
```
level=error msg="WireGuard controller not initialized"
```

**Fix:**
```bash
# Ensure WireGuard is enabled
./server serve --wireguard-server=100.97.0.1/16
```

### Problem: IP allocation exhausted

**Symptom:** `/wg-init` returns 500 with "no available IPs"

**Check allocated IPs:**
```bash
curl http://localhost:8080/wireguardpeers | jq '.items[].spec.allowedIP'
```

**Fix:**
- Increase CIDR size (e.g., /16 instead of /24)
- Clean up old peers via phone-home or manual deletion

### Problem: Peer reconciliation stuck

**Symptom:** WireGuard peer status shows "Degraded" or "Pending"

**Check reconciliation logs:**
```
level=error msg="peer reconciliation failed" uid="wireguardpeer-abc123"
```

**Debug:**
```bash
# Check peer status
curl http://localhost:8080/wireguardpeers/wireguardpeer-abc123 | jq '.status'

# Check controller state
# (requires access to state file)
cat /data/wireguard-state.yaml
```

**Fix:**
- Delete and recreate peer
- Restart service to reload controller state

### Problem: Phone-home fails

**Symptom:** `POST /phone-home/{id}` returns 404 or 500

**Check:**
```bash
# Verify peer exists
curl http://localhost:8080/wireguardpeers/{id}

# Check logs for deletion
kubectl logs deployment/metadata-service -n openchami | grep "phone-home"
```

**Common causes:**
- Peer already deleted
- Invalid peer UID
- Controller state out of sync

---

## Storage Problems

### Problem: Resource create/update fails

**Symptom:** `POST /groups` returns 500

**Check logs for:**
```
level=error msg="storage write failed" error="permission denied"
```

**Fix:**

#### Permission denied

```bash
# Check data directory permissions
kubectl exec deployment/metadata-service -n openchami -- ls -la /data

# Fix permissions
kubectl exec deployment/metadata-service -n openchami -- chmod 755 /data
```

#### Disk full

```bash
# Check disk space
kubectl exec deployment/metadata-service -n openchami -- df -h /data

# Clean up old resources
# (requires manual deletion or retention policy)
```

### Problem: Resources not persisting

**Symptom:** Resources disappear after service restart

**Check:**
- Volume is properly mounted
- Data directory path matches mount point
- Volume is not ephemeral (emptyDir)

**Fix:**
```yaml
# Kubernetes - use PersistentVolumeClaim
volumes:
- name: data
  persistentVolumeClaim:
    claimName: metadata-service-data  # Not emptyDir!
```

### Problem: Duplicate resources

**Symptom:** Multiple resources with same name but different UIDs

**This is expected behavior** - "latest by name" query semantics

**To clean up old versions:**
```bash
# List all versions
curl http://localhost:8080/groups | jq '.items[] | select(.metadata.name == "compute")'

# Delete old versions
curl -X DELETE http://localhost:8080/groups/{old-uid}
```

---

## Performance Issues

### Problem: Slow response times

**Symptom:** Requests take >1 second to complete

**Debug:**

#### Check SMD cache hit rate

**Look for:**
```
level=debug msg="SMD cache hit" component="x1000c0s0b0n0"  # Fast
level=debug msg="SMD cache miss" component="x1000c0s0b0n0" # Slow
```

**Improve cache hit rate:**
- Enable SMD sync: `--smd-sync-enabled`
- Increase sync interval: `--smd-sync-interval=30`

#### Check template rendering time

**Look for:**
```
level=debug msg="template rendered" duration="150ms"
```

**If >100ms:**
- Simplify templates
- Reduce number of groups per node
- Cache rendered templates (future enhancement)

#### Check storage latency

**Look for:**
```
level=debug msg="storage read" duration="50ms"
```

**If >50ms:**
- Check disk I/O (slow disk, network storage)
- Reduce resource count
- Consider database backend (future)

### Problem: High memory usage

**Symptom:** Container OOMKilled or high RSS

**Check:**
```bash
# Docker
docker stats metadata-service

# Kubernetes
kubectl top pod -l app=metadata-service -n openchami
```

**Reduce memory usage:**
- Reduce cache size (requires code change)
- Limit number of stored resources
- Increase resource limits if legitimate usage

---

## Known Issues

### Issue 1: Dash vs underscore in flags/env vars

**Problem:** Flags like `--data-dir` may not be recognized if using underscore env var `DATA_DIR`

**Workaround:** Use matching format:
```bash
# Use dashes consistently
export DATA_DIR=/data
./server serve --data-dir=/data

# Or use underscores
export data_dir=/data
```

**Tracking:** See [bugs.md](../bugs.md#1-data-dir-and-wireguard-state-file-style-flags-can-be-silently-ignored)

### Issue 2: Empty SMD response causes "node not found"

**Problem:** SMD returns empty JSON body for IP lookup, causing 404

**Workaround:**
- Verify node is registered in SMD
- Use mock SMD for testing: `--mock-smd`

**Tracking:** See [bugs.md](../bugs.md#3-meta-data-can-fail-with-node-not-found-when-smd-ip-lookup-returns-an-empty-json-body)

### Issue 3: Docker default data path fragile with read-only root

**Problem:** Data directory not writable with restricted security policies

**Workaround:**
```bash
# Default is now /data (absolute path)
# Ensure volume is mounted
docker run -v metadata-data:/data ...

# Kubernetes - use PVC
volumes:
- name: data
  persistentVolumeClaim:
    claimName: metadata-service-data
```

**Note:** The default was changed from `./data` (relative) to `/data` (absolute) to address this issue.

**Tracking:** See [bugs.md](../bugs.md#2-docker-default-data-is-fragile-with-restricted-psp)

---

## Getting Help

### Check Existing Resources

- [Architecture Documentation](./ARCHITECTURE.md)
- [Deployment Guide](./DEPLOYMENT.md)
- [Cloud-Init Reference](../CLOUDINIT.md)
- [Known Issues](../bugs.md)

### Enable Debug Logging

```bash
export LOG_LEVEL=debug
./server serve
```

### Collect Diagnostic Information

```bash
# Service version
./server version

# Health status
curl http://localhost:8080/health

# Recent logs (last 100 lines)
kubectl logs --tail=100 deployment/metadata-service -n openchami

# Resource counts
curl http://localhost:8080/groups | jq '.items | length'
curl http://localhost:8080/clusterdefaultss | jq '.items | length'

# SMD connectivity
curl -H "Authorization: Bearer $SMD_JWT" $SMD_URL/hsm/v2/service/ready
```

### Report Issues

When reporting issues, include:
1. Service version and deployment platform (K8s, Docker, etc.)
2. Relevant configuration (SMD_URL, flags, env vars)
3. Error message from logs
4. Steps to reproduce
5. Expected vs actual behavior

**GitHub Issues:** https://github.com/OpenCHAMI/metadata-service/issues
