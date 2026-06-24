<!--
SPDX-FileCopyrightText: 2025 OpenCHAMI Contributors

SPDX-License-Identifier: MIT
-->

# OpenCHAMI Metadata Service

The OpenCHAMI metadata service provides NoCloud-compatible cloud-init endpoints for HPC nodes and a generated resource API for the data those endpoints render. It is built on Fabrica, stores resources on disk, integrates with SMD for node identity and group membership, and can use a built-in mock SMD client for local development when `--mock-smd` is set.

Key capabilities
- NoCloud-style endpoints: `/meta-data`, `/user-data`, `/vendor-data`, `/network-config`, and `/{group}.yaml`
- Generated resource APIs and client commands for `clusterdefaults`, `group`, `instanceinfo`, and `wireguardpeer`
- Server-side template validation for group cloud-config templates using Pongo2 plus YAML validation
- OpenAPI output at `/openapi.json` and Swagger UI at `/docs`
- Optional userspace WireGuard bootstrap endpoints at `/wg-init` and `/phone-home/{id}`

## Quick Start

The server defaults to port `8080`. The examples below use `8888` explicitly.

1. Start the server with the built-in mock SMD data:

	 ```bash
	 go run ./cmd/server/main.go serve --port 8888 --mock-smd
	 ```

2. Verify the service endpoints that work without any stored resources:

	 ```bash
	 curl http://localhost:8888/health
	 curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/meta-data
	 curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/user-data
	 curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/network-config
	 ```

3. Create the minimum resources needed for meaningful `vendor-data` and group template rendering. The generated client expects a full request object with both `metadata` and `spec`.

	 ```bash
	 cat > /tmp/clusterdefaults.json <<'EOF'
	 {
		 "metadata": {
			 "name": "demo-cluster"
		 },
		 "spec": {
			 "description": "Local demo cluster defaults",
			 "base_url": "http://localhost:8888",
			 "cloud_provider": "OpenCHAMI",
			 "region": "lab",
			 "availability_zone": "lab-a",
			 "cluster_name": "testcluster",
			 "short_name": "tc",
			 "nid_length": 4,
			 "public_keys": [
				 "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyExample demo@example"
			 ]
		 }
	 }
	 EOF
	 go run ./cmd/client/main.go --server http://localhost:8888 clusterdefaults create --spec "$(cat /tmp/clusterdefaults.json)"

	 cat > /tmp/compute-group.json <<'EOF'
	 {
		 "metadata": {
			 "name": "compute"
		 },
		 "spec": {
			 "description": "Compute nodes",
			 "template": "#cloud-config\nhostname: {{ hostname }}\nwrite_files:\n  - path: /etc/node-role\n    content: |\n      ROLE={{ role }}\n      NID={{ nid }}\n      IP={{ ip }}\n",
			 "metaData": {
				 "scheduler": "slurm"
			 }
		 }
	 }
	 EOF
	 go run ./cmd/client/main.go --server http://localhost:8888 group create --spec "$(cat /tmp/compute-group.json)"

	 cat > /tmp/green-group.json <<'EOF'
	 {
		 "metadata": {
			 "name": "green"
		 },
		 "spec": {
			 "description": "Green nodes",
			 "template": "#cloud-config\nwrite_files:\n  - path: /etc/node-color\n    content: |\n      COLOR={{ color }}\n",
			 "metaData": {
				 "color": "green"
			 }
		 }
	 }
	 EOF
	 go run ./cmd/client/main.go --server http://localhost:8888 group create --spec "$(cat /tmp/green-group.json)"
	 ```

4. With those resources in place, test the rendered cloud-init flows for the first mock node:

	 ```bash
	 curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/vendor-data
	 curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/compute.yaml
	 curl -H "X-Forwarded-For: 10.252.0.26" http://localhost:8888/green.yaml
	 ```

Mock SMD nodes available by default
- `x1000c0s0b0n0` at `10.252.0.26` with groups `compute`, `green`
- `x1000c0s0b0n1` at `10.252.0.27` with groups `compute`, `blue`
- `x1000c0s1b0n0` at `10.252.0.28` with group `storage`

## API Surface

Public service endpoints
- `/health`
- `/openapi.json`
- `/docs`

Cloud-init endpoints
- `/meta-data`
- `/user-data`
- `/vendor-data`
- `/network-config`
- `/{group}.yaml`

Generated resource APIs
- Prefer the generated client commands: `clusterdefaults`, `group`, `instanceinfo`, `wireguardpeer`
- The raw generated REST collections are `/clusterdefaultss`, `/groups`, `/instanceinfos`, and `/wireguardpeers`

## Template Context

Group templates are stored as plain text and rendered with these runtime values:
- Flat keys such as `hostname`, `local_hostname`, `instance_id`, `cluster_name`, `cloud_provider`, `region`, `nid`, `role`, `mac`, `ip`, `interfaces`, and `public_keys`
- Nested `vendor_data` matching the `/meta-data` payload
- Nested `meta_data` containing the full cloud-init metadata document
- Custom keys from `Group.Spec.MetaData`

The server validates templates at create and update time. A template must render successfully against sample metadata and produce valid YAML.

## Running With Real SMD

Set `SMD_URL` to use a real SMD instance.
Supported URL shapes:
- Bare SMD service URL: `http://smd.example.com:27779` (normalized to `.../hsm/v2`)
- Gateway-mounted URL: `https://gateway.example.com/apis/smd` or `https://gateway.example.com/apis/smd/hsm/v2`

Static auth mode (default when `tokensmith_url` is unset):
- Uses `SMD_JWT` (or `SMD_TOKEN`) as the outbound `Authorization: Bearer` token.

```bash
SMD_URL=https://smd.example.com \
SMD_JWT="$JWT" \
go run ./cmd/server/main.go serve --port 8888
```

TokenSmith dynamic auth mode (enabled when `tokensmith_url` is set):
- Primary path: mTLS service identity session exchange through `POST /service-identity/session`.
- Compatibility fallback path: bootstrap token exchange through `POST /oauth/token` when service identity cert/key are missing or unreadable.
- Dynamic auth initializes with bounded retries and refreshes in the background with bounded retries.
- If retries are exhausted, runtime health becomes unhealthy and SMD calls fail closed.
- When `tokensmith_url` is set, the server does not silently degrade to static `SMD_JWT`/`SMD_TOKEN`.

mTLS-first example:

```bash
SMD_URL=https://smd.example.com \
TOKENSMITH_URL=https://tokensmith.example.com \
TOKENSMITH_SERVICE_IDENTITY_CERT=/run/secrets/metadata-service/tokensmith-client.crt \
TOKENSMITH_SERVICE_IDENTITY_KEY=/run/secrets/metadata-service/tokensmith-client.key \
TOKENSMITH_SERVICE_IDENTITY_CA=/run/secrets/metadata-service/tokensmith-ca.crt \
go run ./cmd/server/main.go serve --port 8888 \
  --tokensmith-target-service smd \
  --tokensmith-refresh-skew-sec 300
```

Bootstrap fallback example (legacy compatibility):

```bash
SMD_URL=https://smd.example.com \
TOKENSMITH_URL=https://tokensmith.example.com \
TOKENSMITH_BOOTSTRAP_TOKEN="$BOOTSTRAP_TOKEN" \
go run ./cmd/server/main.go serve --port 8888 \
  --tokensmith-target-service smd \
  --tokensmith-scopes metadata:read,groups:read \
  --tokensmith-refresh-skew-sec 300
```

TokenSmith server options:
- `tokensmith_url` / `TOKENSMITH_URL`
- `tokensmith_bootstrap_token` / `TOKENSMITH_BOOTSTRAP_TOKEN`
- `tokensmith_service_identity_cert` / `TOKENSMITH_SERVICE_IDENTITY_CERT`
- `tokensmith_service_identity_key` / `TOKENSMITH_SERVICE_IDENTITY_KEY`
- `tokensmith_service_identity_ca` / `TOKENSMITH_SERVICE_IDENTITY_CA` (optional)
- `tokensmith_target_service` / `TOKENSMITH_TARGET_SERVICE` (default: `smd`)
- `tokensmith_scopes` / `TOKENSMITH_SCOPES` (diagnostics metadata only)
- `tokensmith_refresh_skew_sec` / `TOKENSMITH_REFRESH_SKEW_SEC` (default: `300`)

Request identity resolution prefers a WireGuard reverse lookup when available, then falls back to direct IP lookup through SMD.

Optional SMD sync controls:
- `--smd-sync-enabled` (default `true`)
- `--smd-sync-interval` in seconds (default `60`)

Deployment examples (path-based cert/key injection):
- Kubernetes:
```yaml
env:
  - name: TOKENSMITH_URL
    value: https://tokensmith.example.com
  - name: TOKENSMITH_SERVICE_IDENTITY_CERT
    value: /var/run/tokensmith/client.crt
  - name: TOKENSMITH_SERVICE_IDENTITY_KEY
    value: /var/run/tokensmith/client.key
  - name: TOKENSMITH_SERVICE_IDENTITY_CA
    value: /var/run/tokensmith/ca.crt
volumeMounts:
  - name: tokensmith-identity
    mountPath: /var/run/tokensmith
    readOnly: true
```

- `systemd`:
```ini
[Service]
Environment=TOKENSMITH_URL=https://tokensmith.example.com
Environment=TOKENSMITH_SERVICE_IDENTITY_CERT=/etc/metadata-service/tokensmith/client.crt
Environment=TOKENSMITH_SERVICE_IDENTITY_KEY=/etc/metadata-service/tokensmith/client.key
Environment=TOKENSMITH_SERVICE_IDENTITY_CA=/etc/metadata-service/tokensmith/ca.crt
```

- Quadlet/Podman:
```ini
[Container]
Environment=TOKENSMITH_URL=https://tokensmith.example.com
Environment=TOKENSMITH_SERVICE_IDENTITY_CERT=/run/secrets/tokensmith/client.crt
Environment=TOKENSMITH_SERVICE_IDENTITY_KEY=/run/secrets/tokensmith/client.key
Environment=TOKENSMITH_SERVICE_IDENTITY_CA=/run/secrets/tokensmith/ca.crt
Volume=/host/secrets/tokensmith:/run/secrets/tokensmith:ro
```

- Docker Compose:
```yaml
services:
  metadata-service:
    environment:
      TOKENSMITH_URL: https://tokensmith.example.com
      TOKENSMITH_SERVICE_IDENTITY_CERT: /run/secrets/tokensmith/client.crt
      TOKENSMITH_SERVICE_IDENTITY_KEY: /run/secrets/tokensmith/client.key
      TOKENSMITH_SERVICE_IDENTITY_CA: /run/secrets/tokensmith/ca.crt
    volumes:
      - ./secrets/tokensmith:/run/secrets/tokensmith:ro
```

## Optional WireGuard Support

Enable the userspace WireGuard controller by passing a CIDR whose host address is the server address inside the VPN network.

```bash
go run ./cmd/server/main.go serve --port 8888 --wireguard-server 100.97.0.1/16
```

Bootstrap a peer with:

```bash
curl \
	-X POST \
	-H "Content-Type: application/json" \
	-H "X-Forwarded-For: 10.252.0.26" \
	-d '{"public_key":"REPLACE_WITH_BASE64_WIREGUARD_PUBLIC_KEY"}' \
	http://localhost:8888/wg-init
```

If you also pass `--wireguard-only`, the server will reject requests whose remote address is not inside the configured WireGuard CIDR.

## Documentation

- **[Architecture Overview](docs/ARCHITECTURE.md)** - System design, component interaction, and request flows
- **[Deployment Guide](docs/DEPLOYMENT.md)** - Production deployment for Kubernetes, Docker, Podman, and systemd
- **[Cloud-Init Reference](CLOUDINIT.md)** - NoCloud endpoint behavior and template context
- **[Client Usage Guide](docs/CLIENT_USAGE.md)** - CLI and Go client library usage
- **[Troubleshooting](docs/TROUBLESHOOTING.md)** - Common issues and solutions
- **[FAQ](docs/FAQ.md)** - Frequently asked questions
- **[WireGuard Testing](docs/wireguard.md)** - WireGuard bootstrap and lifecycle testing
- **[Legacy Compatibility](LEGACY_COMPATIBILITY.md)** - Migration from legacy cloud-init service
- **[Known Issues](bugs.md)** - Current bugs and workarounds

## Development

```bash
go mod tidy
make generate
make build
make test
make pre-commit-run
```

Additional examples live in [examples/README.md](examples/README.md).

## Release Notes

See [CHANGELOG.md](CHANGELOG.md) for the `0.1.0` release notes.
