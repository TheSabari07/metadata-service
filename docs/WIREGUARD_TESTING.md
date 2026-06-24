# Testing Guide: Reconciliation Runtime & WireGuard Peer Lifecycle

This is a quick guide for testing the WireGuard with the metadata-service.

## Prerequisites

Two terminal sessions — one for the server, one for testing. You'll also need `jq` and `wg` (WireGuard tools) installed. This guide assumes that you have cloned the respository and are testing from source. Install `go`, `git`, and `make` if necessary and build the binaries before proceeding with the rest of this guide.

```bash
git clone https://github.com/OpenCHAMI/metadata-service
cd metadata-service && make build && ls bin -lah
```

Additionally, export the host of the server as an environment variable. We will need this for the steps below.

```bash
export METADATA_SERVICE_HOST=http://localhost:8080
```

> [!TIP]
> You may also need to run the following if you receieve an error stating "Failed to initialize WireGuard controller: failed to create userspace device: failed to create TUN device: operation not permitted".
>
> ```bash
> sudo setcap cap_net_admin+ep bin/ochami-metadata-server
> ```
>
> If you are using Podman Quadlets, add the following lines to your quadlet file under the `[Container]` section.
>
> ```ini
> AddCapability=cap_net_admin
> AddDevice=/dev/net/tun
> ```

---

### 1. Start the server with WireGuard enabled

```bash
bin/ochami-metadata-server serve \
  --data-dir /tmp/metadata-test \
  --wireguard-server 100.97.0.1/24 \
  --wireguard-state-file /tmp/metadata-test/wg-state.yaml
```

Expected startup log lines:

- `"WireGuard userspace controller enabled on 100.97.0.1/24"`
- `"Mock SMD client initialized with sample data"`
- `"Reconciliation runtime initialized"`

---

### 2. Test WireGuard peer initialization (`POST /wg-init`)

```bash
PUBKEY=$(wg genkey | wg pubkey)

curl -s -X POST $METADATA_SERVICE_HOST/wg-init \
  -H "Content-Type: application/json" \
  -d "{\"public_key\": \"$PUBKEY\"}" | jq .
```

Expected response (HTTP 202):

```json
{
  "message": "WireGuard peer allocation accepted",
  "peer-uid": "wireguardpeer-<hex>",
  "client-vpn-ip": "100.97.0.2",
  "server-public-key": "<base64>",
  "server-ip": "100.97.0.1",
  "server-port": "51820"
}
```

---

### 3. Test idempotency (same key returns same config)

```bash
curl -s -X POST $METADATA_SERVICE_HOST/wg-init \
  -H "Content-Type: application/json" \
  -d "{\"public_key\": \"$PUBKEY\"}" | jq .
```

Expected: Same `peer-uid` and `client-vpn-ip` as the first call.

---

### 4. Verify the peer via the REST API

```bash
curl -s $METADATA_SERVICE_HOST/wireguardpeers | jq .
curl -s $METADATA_SERVICE_HOST/wireguardpeers/wireguardpeer-<hex> | jq .
```

Expected: Status shows `"phase": "Ready"` if reconciliation succeeded. `"Degraded"` is also normal when running outside the reconciler's event bus (peer still works).

---

### 5. Test phone-home de-registration (`POST /phone-home/{id}`)

```bash
curl -s -X POST $METADATA_SERVICE_HOST/phone-home/wireguardpeer-<hex> -w "\nHTTP %{http_code}\n"
curl -s $METADATA_SERVICE_HOST/wireguardpeers/wireguardpeer-<hex>
```

Expected: HTTP 200, then 404 (peer deleted).

---

### 6. Test REST API CRUD directly

```bash
curl -s -X POST $METADATA_SERVICE_HOST/wireguardpeers \
  -H "Content-Type: application/json" \
  -d '{
    "apiVersion": "v1",
    "kind": "WireGuardPeer",
    "metadata": {"name": "test-peer"},
    "spec": {
      "public_key": "'"$(wg genkey | wg pubkey)"'",
      "allowed_ip": "100.97.0.42/32"
    }
  }' | jq .

curl -s $METADATA_SERVICE_HOST/wireguardpeers | jq '.items | length'

PEER_UID=$(curl -s $METADATA_SERVICE_HOST/wireguardpeers | jq -r '.items[0].metadata.uid')
curl -s -X DELETE "$METADATA_SERVICE_HOST/wireguardpeers/$PEER_UID" -w "HTTP %{http_code}\n"
```

---

### 7. Test WireGuard-only access restriction

Restart the server with the following.

```bash
bin/ochami-metadata-server serve \
  --data-dir /tmp/metadata-test-wgonly \
  --wireguard-server 100.97.0.1/24 \
  --wireguard-only 
curl -s $METADATA_SERVICE_HOST/wg-init -w "\nHTTP %{http_code}\n"                                    # 403
curl -s -H "X-Forwarded-For: 100.97.0.5" $METADATA_SERVICE_HOST/wg-init \
  -H "Content-Type: application/json" \
  -d "{\"public_key\": \"$(wg genkey | wg pubkey)\"}" | jq .                                         # 202
```

---

### 8. Run the test suite

```bash
go test ./cmd/server/... ./pkg/reconcilers/... ./pkg/wireguard/... -v -count=1 2>&1 | \
  grep -E "^(=== RUN|--- PASS|--- FAIL|ok |FAIL)"
```

---

### 9. Test state persistence

Stop the server from step 1 (Ctrl+C), then restart:

```bash
bin/ochami-metadata-server serve \
  --data-dir /tmp/metadata-test \
  --wireguard-server 100.97.0.1/24 \
  --wireguard-state-file /tmp/metadata-test/wg-state.yaml

curl -s $METADATA_SERVICE_HOST/wireguardpeers | jq '.items | length'
```

Expected: Previous peers are still present (reconciled from storage on startup).

---

### Cleanup

```bash
rm -rf /tmp/metadata-test /tmp/metadata-test-wgonly
```
