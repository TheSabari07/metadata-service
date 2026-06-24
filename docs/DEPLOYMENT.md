<!--
SPDX-FileCopyrightText: 2026 OpenCHAMI Contributors

SPDX-License-Identifier: MIT
-->

# Deployment Guide

This guide covers production deployment of the OpenCHAMI metadata service across different container platforms and orchestrators.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Container Image](#container-image)
- [Kubernetes Deployment](#kubernetes-deployment)
- [Docker Compose Deployment](#docker-compose-deployment)
- [Podman Deployment](#podman-deployment)
- [Systemd Service](#systemd-service)
- [Configuration Reference](#configuration-reference)
- [Production Checklist](#production-checklist)
- [Monitoring and Health Checks](#monitoring-and-health-checks)
- [Backup and Recovery](#backup-and-recovery)
- [Troubleshooting](#troubleshooting)

---

## Prerequisites

### Required
- Container runtime (Docker, Podman, or containerd)
- Access to SMD service (or use `--mock-smd` for testing)
- Persistent storage for data directory

### Optional
- TokenSmith service for dynamic authentication
- WireGuard support (for VPN bootstrap)
- Load balancer or ingress controller

### Network Requirements
- Inbound: Port 8080 (default) or custom port
- Outbound: SMD service (typically port 27779)
- Outbound: TokenSmith service (if using dynamic auth)

---

## Container Image

### Official Images

```bash
# GitHub Container Registry
ghcr.io/openchami/metadata-service:latest
ghcr.io/openchami/metadata-service:v0.1.0

# Docker Hub (if published)
openchami/metadata-service:latest
```

### Build from Source

```bash
# Clone repository
git clone https://github.com/OpenCHAMI/metadata-service.git
cd metadata-service

# Build binary
make build

# Build container image
docker build -t metadata-service:local .
```

### Image Details

**Base Image:** `gcr.io/distroless/static-debian12:nonroot`

**User:** `nonroot` (UID 65532)

**Workdir:** `/app`

**Default Port:** `8080`

**Default Data Dir:** `/data` (absolute path)

---

## Kubernetes Deployment

### Basic Deployment

```yaml
# metadata-service.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: openchami

---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: metadata-service-data
  namespace: openchami
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  storageClassName: standard  # Adjust for your cluster

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metadata-service
  namespace: openchami
spec:
  replicas: 1  # Do not scale >1 without external storage coordination
  selector:
    matchLabels:
      app: metadata-service
  template:
    metadata:
      labels:
        app: metadata-service
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        fsGroup: 65532
      containers:
      - name: metadata-service
        image: ghcr.io/openchami/metadata-service:latest
        imagePullPolicy: Always
        args:
          - serve
          - --port=8080
          - --data-dir=/data
        ports:
        - containerPort: 8080
          name: http
          protocol: TCP
        env:
        - name: SMD_URL
          value: "http://smd.openchami.svc.cluster.local:27779"
        - name: SMD_JWT
          valueFrom:
            secretKeyRef:
              name: smd-credentials
              key: jwt-token
        - name: LOG_LEVEL
          value: "info"
        volumeMounts:
        - name: data
          mountPath: /data
        livenessProbe:
          httpGet:
            path: /health
            port: http
          initialDelaySeconds: 10
          periodSeconds: 30
          timeoutSeconds: 5
        readinessProbe:
          httpGet:
            path: /health
            port: http
          initialDelaySeconds: 5
          periodSeconds: 10
          timeoutSeconds: 5
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "512Mi"
            cpu: "500m"
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: metadata-service-data

---
apiVersion: v1
kind: Service
metadata:
  name: metadata-service
  namespace: openchami
spec:
  selector:
    app: metadata-service
  ports:
  - port: 80
    targetPort: http
    protocol: TCP
    name: http
  type: ClusterIP

---
apiVersion: v1
kind: Secret
metadata:
  name: smd-credentials
  namespace: openchami
type: Opaque
stringData:
  jwt-token: "YOUR_SMD_JWT_TOKEN_HERE"
```

Apply:
```bash
kubectl apply -f metadata-service.yaml
```

### Deployment with TokenSmith (mTLS)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metadata-service
  namespace: openchami
spec:
  template:
    spec:
      containers:
      - name: metadata-service
        env:
        - name: SMD_URL
          value: "https://smd.example.com"
        - name: TOKENSMITH_URL
          value: "https://tokensmith.openchami.svc.cluster.local"
        - name: TOKENSMITH_SERVICE_IDENTITY_CERT
          value: "/var/run/tokensmith/client.crt"
        - name: TOKENSMITH_SERVICE_IDENTITY_KEY
          value: "/var/run/tokensmith/client.key"
        - name: TOKENSMITH_SERVICE_IDENTITY_CA
          value: "/var/run/tokensmith/ca.crt"
        - name: TOKENSMITH_TARGET_SERVICE
          value: "smd"
        - name: TOKENSMITH_REFRESH_SKEW_SEC
          value: "300"
        volumeMounts:
        - name: data
          mountPath: /data
        - name: tokensmith-identity
          mountPath: /var/run/tokensmith
          readOnly: true
      volumes:
      - name: data
        persistentVolumeClaim:
          claimName: metadata-service-data
      - name: tokensmith-identity
        secret:
          secretName: tokensmith-client-cert
          items:
          - key: tls.crt
            path: client.crt
          - key: tls.key
            path: client.key
          - key: ca.crt
            path: ca.crt
```

Create TokenSmith secret:
```bash
kubectl create secret generic tokensmith-client-cert \
  --from-file=tls.crt=client.crt \
  --from-file=tls.key=client.key \
  --from-file=ca.crt=ca.crt \
  -n openchami
```

### Deployment with WireGuard

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: metadata-service
  namespace: openchami
spec:
  template:
    spec:
      containers:
      - name: metadata-service
        args:
          - serve
          - --port=8080
          - --data-dir=/data
          - --wireguard-server=100.97.0.1/16
          - --wireguard-state-file=/data/wireguard-state.yaml
        env:
        - name: WIREGUARD_SERVER
          value: "100.97.0.1/16"
        securityContext:
          capabilities:
            add:
            - NET_ADMIN  # Required for userspace WireGuard
```

**Note:** WireGuard requires `NET_ADMIN` capability. Adjust Pod Security Standards accordingly.

### Ingress Configuration

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: metadata-service
  namespace: openchami
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
spec:
  ingressClassName: nginx
  rules:
  - host: metadata.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: metadata-service
            port:
              number: 80
  tls:
  - hosts:
    - metadata.example.com
    secretName: metadata-service-tls
```

---

## Docker Compose Deployment

### Basic Compose File

```yaml
# docker-compose.yml
version: '3.8'

services:
  metadata-service:
    image: ghcr.io/openchami/metadata-service:latest
    container_name: metadata-service
    command:
      - serve
      - --port=8080
      - --data-dir=/data
    ports:
      - "8080:8080"
    environment:
      SMD_URL: "http://smd:27779"
      SMD_JWT: "${SMD_JWT}"
      LOG_LEVEL: "info"
    volumes:
      - metadata-data:/data
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "--spider", "-q", "http://localhost:8080/health"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

volumes:
  metadata-data:
    driver: local
```

Run:
```bash
export SMD_JWT="your-token-here"
docker-compose up -d
```

### Compose with TokenSmith

```yaml
version: '3.8'

services:
  metadata-service:
    image: ghcr.io/openchami/metadata-service:latest
    environment:
      SMD_URL: "https://smd.example.com"
      TOKENSMITH_URL: "https://tokensmith.example.com"
      TOKENSMITH_SERVICE_IDENTITY_CERT: "/run/secrets/tokensmith/client.crt"
      TOKENSMITH_SERVICE_IDENTITY_KEY: "/run/secrets/tokensmith/client.key"
      TOKENSMITH_SERVICE_IDENTITY_CA: "/run/secrets/tokensmith/ca.crt"
      TOKENSMITH_TARGET_SERVICE: "smd"
    volumes:
      - metadata-data:/data
      - ./secrets/tokensmith:/run/secrets/tokensmith:ro
    ports:
      - "8080:8080"
    restart: unless-stopped

volumes:
  metadata-data:
```

---

## Podman Deployment

### Podman Run

```bash
# Create volume
podman volume create metadata-data

# Run container
podman run -d \
  --name metadata-service \
  -p 8080:8080 \
  -v metadata-data:/data:Z \
  -e SMD_URL=http://smd.example.com:27779 \
  -e SMD_JWT="$SMD_JWT" \
  -e LOG_LEVEL=info \
  --health-cmd="wget --spider -q http://localhost:8080/health || exit 1" \
  --health-interval=30s \
  --health-timeout=5s \
  --health-retries=3 \
  ghcr.io/openchami/metadata-service:latest \
  serve --port=8080 --data-dir=/data
```

### Podman with SELinux

```bash
# Create labeled volume
podman volume create metadata-data

# Run with SELinux label
podman run -d \
  --name metadata-service \
  -p 8080:8080 \
  -v metadata-data:/data:Z \
  --security-opt label=type:container_runtime_t \
  -e SMD_URL=http://smd.example.com:27779 \
  -e SMD_JWT="$SMD_JWT" \
  ghcr.io/openchami/metadata-service:latest \
  serve --port=8080 --data-dir=/data
```

### Quadlet (systemd-managed Podman)

```ini
# /etc/containers/systemd/metadata-service.container
[Unit]
Description=OpenCHAMI Metadata Service
After=network-online.target

[Container]
Image=ghcr.io/openchami/metadata-service:latest
PublishPort=8080:8080
Volume=metadata-data:/data:Z
Environment=SMD_URL=http://smd.example.com:27779
Environment=SMD_JWT=%SMD_JWT%
Environment=LOG_LEVEL=info
Exec=serve --port=8080 --data-dir=/data

HealthCmd=wget --spider -q http://localhost:8080/health || exit 1
HealthInterval=30s
HealthTimeout=5s
HealthRetries=3

[Service]
Restart=always
TimeoutStartSec=900

[Install]
WantedBy=multi-user.target default.target
```

Enable and start:
```bash
systemctl daemon-reload
systemctl enable --now metadata-service.service
```

---

## Systemd Service

### Native Binary Service

```ini
# /etc/systemd/system/metadata-service.service
[Unit]
Description=OpenCHAMI Metadata Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=metadata
Group=metadata
WorkingDirectory=/opt/metadata-service
ExecStart=/opt/metadata-service/bin/server serve \
  --port=8080 \
  --data-dir=/var/lib/metadata-service
Environment=SMD_URL=http://smd.example.com:27779
Environment=SMD_JWT=file:///etc/metadata-service/smd-jwt.token
Environment=LOG_LEVEL=info
Restart=on-failure
RestartSec=5s
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/metadata-service
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
```

Setup:
```bash
# Create user
sudo useradd -r -s /bin/false metadata

# Create directories
sudo mkdir -p /opt/metadata-service/bin
sudo mkdir -p /var/lib/metadata-service
sudo mkdir -p /etc/metadata-service

# Install binary
sudo cp bin/server /opt/metadata-service/bin/

# Set ownership
sudo chown -R metadata:metadata /var/lib/metadata-service
sudo chown metadata:metadata /opt/metadata-service/bin/server

# Store token securely
echo "your-jwt-token" | sudo tee /etc/metadata-service/smd-jwt.token
sudo chmod 600 /etc/metadata-service/smd-jwt.token
sudo chown metadata:metadata /etc/metadata-service/smd-jwt.token

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable metadata-service
sudo systemctl start metadata-service
```

---

## Configuration Reference

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `SMD_URL` | SMD service URL | - | Yes (unless `--mock-smd`) |
| `SMD_JWT` | Static SMD auth token | - | Yes (unless TokenSmith) |
| `SMD_TOKEN` | Alias for `SMD_JWT` | - | No |
| `TOKENSMITH_URL` | TokenSmith service URL | - | No |
| `TOKENSMITH_BOOTSTRAP_TOKEN` | Bootstrap token | - | No |
| `TOKENSMITH_SERVICE_IDENTITY_CERT` | Client cert path | - | No |
| `TOKENSMITH_SERVICE_IDENTITY_KEY` | Client key path | - | No |
| `TOKENSMITH_SERVICE_IDENTITY_CA` | CA cert path | - | No |
| `TOKENSMITH_TARGET_SERVICE` | Target service name | `smd` | No |
| `TOKENSMITH_SCOPES` | OAuth scopes | - | No |
| `TOKENSMITH_REFRESH_SKEW_SEC` | Refresh skew (seconds) | `300` | No |
| `TOKENSMITH_SCOPE_HINT` | Scope hint for diagnostics | - | No |
| `LOG_LEVEL` | Log level (debug/info/warn/error) | `info` | No |
| `WIREGUARD_SERVER` | WireGuard server CIDR | - | No |
| `WIREGUARD_STATE_FILE` | WireGuard state file path | - | No |

### Command-Line Flags

```bash
metadata-service serve [flags]

Flags:
  --port int                      HTTP port (default 8080)
  --data-dir string               Data directory (default "/data")
  --smd-url string                SMD service URL
  --smd-jwt string                SMD JWT token
  --mock-smd                      Use mock SMD client
  --smd-sync-enabled              Enable SMD cache sync (default true)
  --smd-sync-interval int         Sync interval in seconds (default 60)
  --tokensmith-url string         TokenSmith service URL
  --tokensmith-bootstrap-token string
                                  Bootstrap token
  --tokensmith-target-service string
                                  Target service (default "smd")
  --tokensmith-scopes string      OAuth scopes
  --tokensmith-refresh-skew-sec int
                                  Refresh skew in seconds (default 300)
  --wireguard-server string       WireGuard server CIDR (e.g., 100.97.0.1/16)
  --wireguard-state-file string   WireGuard state file path
  --wireguard-only                Restrict to WireGuard clients only
  --log-level string              Log level (default "info")
```

### Precedence

Configuration precedence (highest to lowest):
1. Command-line flags
2. Environment variables
3. Default values

---

## Production Checklist

### Pre-Deployment

- [ ] SMD connectivity verified
- [ ] Authentication configured (static JWT or TokenSmith)
- [ ] Data directory storage provisioned (>= 10GB recommended)
- [ ] Backup strategy defined
- [ ] Monitoring/alerting configured
- [ ] Network policies/firewall rules reviewed
- [ ] TLS termination configured (if using ingress/gateway)

### Deployment

- [ ] Container image pinned to specific version (not `:latest`)
- [ ] Resource limits set (memory, CPU)
- [ ] Health checks configured
- [ ] Persistent storage attached
- [ ] Secrets managed securely (not in env vars)
- [ ] Security context configured (non-root user)
- [ ] Logging configured and tested

### Post-Deployment

- [ ] Health endpoint returns 200 OK
- [ ] `/meta-data` returns valid YAML for test node
- [ ] SMD integration working (check logs)
- [ ] TokenSmith token refresh working (if applicable)
- [ ] WireGuard bootstrap working (if enabled)
- [ ] Monitoring dashboards populated
- [ ] Backup tested and verified

---

## Monitoring and Health Checks

### Health Endpoint

```bash
curl http://localhost:8080/health
```

Response:
```json
{
  "status": "healthy",
  "smd_client": "connected",
  "storage": "ready",
  "wireguard": "enabled"
}
```

### Kubernetes Probes

```yaml
livenessProbe:
  httpGet:
    path: /health
    port: http
  initialDelaySeconds: 10
  periodSeconds: 30
  timeoutSeconds: 5
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /health
    port: http
  initialDelaySeconds: 5
  periodSeconds: 10
  timeoutSeconds: 5
  failureThreshold: 3
```

### Log Monitoring

Key log patterns to monitor:

**Errors:**
```
level=error msg="SMD query failed"
level=error msg="Token refresh failed"
level=error msg="Storage write failed"
```

**Warnings:**
```
level=warn msg="SMD cache miss"
level=warn msg="Template validation failed"
level=warn msg="Node not found"
```

**Success Indicators:**
```
level=info msg="Server started"
level=info msg="SMD sync completed"
level=info msg="WireGuard peer allocated"
```

### Metrics (Future)

Planned Prometheus metrics:
- `metadata_requests_total{endpoint, status}`
- `metadata_request_duration_seconds{endpoint}`
- `metadata_smd_cache_hits_total`
- `metadata_smd_cache_misses_total`
- `metadata_wireguard_peers_total`
- `metadata_storage_operations_total{operation, status}`

---

## Backup and Recovery

### Data Directory Backup

```bash
# Stop service (or use snapshot if supported)
kubectl scale deployment metadata-service --replicas=0 -n openchami

# Backup data directory
tar -czf metadata-backup-$(date +%Y%m%d).tar.gz -C /data .

# Restart service
kubectl scale deployment metadata-service --replicas=1 -n openchami
```

### Kubernetes Volume Backup

```bash
# Using velero
velero backup create metadata-service-backup \
  --include-namespaces openchami \
  --include-resources pvc,pv

# Using volume snapshots (if supported by storage class)
kubectl create volumesnapshot metadata-service-snapshot \
  --volumesnapshotclass csi-snapclass \
  --source metadata-service-data \
  -n openchami
```

### Restore

```bash
# Stop service
kubectl scale deployment metadata-service --replicas=0 -n openchami

# Restore data
tar -xzf metadata-backup-20260101.tar.gz -C /data

# Restart service
kubectl scale deployment metadata-service --replicas=1 -n openchami

# Verify
kubectl logs -f deployment/metadata-service -n openchami
curl http://metadata-service/health
```

### Disaster Recovery

**RTO (Recovery Time Objective):** < 15 minutes

**RPO (Recovery Point Objective):** Depends on backup frequency

**Recovery Steps:**
1. Provision new cluster/infrastructure
2. Deploy metadata-service from manifest
3. Restore data directory from backup
4. Verify health endpoint
5. Test cloud-init flow with sample node

---

## Troubleshooting

### Service Won't Start

**Symptom:** Container exits immediately

**Check:**
```bash
# View logs
kubectl logs deployment/metadata-service -n openchami

# Common issues:
# - Data directory not writable
# - SMD URL unreachable
# - Invalid TokenSmith certificates
```

**Fix:**
```bash
# Check permissions
kubectl exec -it deployment/metadata-service -n openchami -- ls -la /data

# Verify SMD connectivity
kubectl exec -it deployment/metadata-service -n openchami -- wget -O- $SMD_URL/hsm/v2/service/ready
```

### Health Check Failing

**Symptom:** Readiness/liveness probes fail

**Check:**
```bash
curl http://localhost:8080/health
```

**Common Causes:**
- SMD unreachable → Check `smd_client` status
- Storage not writable → Check `storage` status
- WireGuard initialization failed → Check `wireguard` status

### Node Not Found Errors

**Symptom:** `/meta-data` returns 404

**Debug:**
```bash
# Check SMD IP lookup
curl "$SMD_URL/hsm/v2/Inventory/EthernetInterfaces?IPAddress=10.252.0.26"

# Check service logs for identity resolution
kubectl logs deployment/metadata-service -n openchami | grep "resolving identity"
```

### Template Rendering Errors

**Symptom:** `/{group}.yaml` returns 500

**Debug:**
```bash
# Check group template validation
kubectl exec -it deployment/metadata-service -n openchami -- \
  wget -O- http://localhost:8080/groups

# View detailed error in logs
kubectl logs deployment/metadata-service -n openchami | grep "template render"
```

### TokenSmith Auth Failures

**Symptom:** SMD queries return 401/403

**Debug:**
```bash
# Check TokenSmith connectivity
kubectl exec -it deployment/metadata-service -n openchami -- \
  wget -O- $TOKENSMITH_URL/.well-known/openid-configuration

# Check certificate validity
kubectl exec -it deployment/metadata-service -n openchami -- \
  openssl x509 -in /var/run/tokensmith/client.crt -noout -dates

# View token refresh logs
kubectl logs deployment/metadata-service -n openchami | grep "token refresh"
```

### Storage Issues

**Symptom:** Resource create/update fails

**Debug:**
```bash
# Check disk space
kubectl exec -it deployment/metadata-service -n openchami -- df -h /data

# Check permissions
kubectl exec -it deployment/metadata-service -n openchami -- ls -la /data

# View storage logs
kubectl logs deployment/metadata-service -n openchami | grep "storage"
```

---

## Related Documentation

- [Architecture Overview](./ARCHITECTURE.md)
- [Configuration Reference](../README.md#running-with-real-smd)
- [TokenSmith Integration](./tokensmith-integration.md)
- [WireGuard Setup](./wireguard-architecture.md)
- [Troubleshooting Guide](./TROUBLESHOOTING.md)
