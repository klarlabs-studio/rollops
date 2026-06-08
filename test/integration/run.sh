#!/usr/bin/env bash
# Run the live integration tests against real SSH + FTP servers in Docker.
#   ./test/integration/run.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
cd "$here"

key="$here/keys/id_test"

cleanup() { docker compose -f docker-compose.test.yml down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT

# 1. Fresh SSH keypair for the test.
rm -f "$key" "$key.pub"
ssh-keygen -t ed25519 -N "" -f "$key" -q

# 2. Bring the environment up.
docker compose -f docker-compose.test.yml up -d

# 3. Wait for SSH to accept the key (up to ~40s).
echo "waiting for sshd..."
for i in $(seq 1 40); do
  if ssh -i "$key" -p 2222 \
        -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o ConnectTimeout=2 -o BatchMode=yes \
        deploy@127.0.0.1 'mkdir -p /config/deploy && true' >/dev/null 2>&1; then
    echo "sshd ready"; break
  fi
  sleep 1
  [ "$i" = 40 ] && { echo "sshd did not come up"; docker compose -f docker-compose.test.yml logs ssh; exit 1; }
done

# 4. Wait for FTP to accept a real login (up to ~30s).
echo "waiting for ftp..."
for i in $(seq 1 30); do
  if curl -s --connect-timeout 2 -u deploy:deploypass "ftp://127.0.0.1/" >/dev/null 2>&1; then
    echo "ftp ready"; break
  fi
  sleep 1
  [ "$i" = 30 ] && { echo "ftp did not come up"; docker compose -f docker-compose.test.yml logs ftp; exit 1; }
done

# 5. Vault: wait for the dev server, write a KV v2 secret.
echo "waiting for vault..."
for i in $(seq 1 30); do
  if curl -s -o /dev/null "http://127.0.0.1:8200/v1/sys/health"; then echo "vault ready"; break; fi
  sleep 1
  [ "$i" = 30 ] && { echo "vault did not come up"; docker compose -f docker-compose.test.yml logs vault; exit 1; }
done
curl -s -H "X-Vault-Token: roottoken" -X POST \
  -d '{"data":{"password":"s3cr3t-live"}}' \
  "http://127.0.0.1:8200/v1/secret/data/myapp" >/dev/null

# 6. cosign: push a signed and an unsigned image to the local registry.
cosign_pub=""; signed_image=""; unsigned_image=""
if command -v cosign >/dev/null 2>&1; then
  echo "setting up cosign..."
  echo "waiting for registry..."
  for i in $(seq 1 20); do curl -s -o /dev/null "http://127.0.0.1:5050/v2/" && break; sleep 1; done

  docker pull -q alpine:3.20 >/dev/null
  docker tag alpine:3.20 localhost:5050/signed:latest
  docker tag alpine:3.20 localhost:5050/unsigned:latest
  docker push -q localhost:5050/signed:latest   >/dev/null
  docker push -q localhost:5050/unsigned:latest >/dev/null

  keydir="$here/keys"
  rm -f "$keydir/cosign.key" "$keydir/cosign.pub"
  ( cd "$keydir" && COSIGN_PASSWORD="" cosign generate-key-pair >/dev/null 2>&1 )
  COSIGN_PASSWORD="" cosign sign --key "$keydir/cosign.key" --yes \
    --allow-http-registry localhost:5050/signed:latest >/dev/null 2>&1

  cosign_pub="$keydir/cosign.pub"
  signed_image="localhost:5050/signed:latest"
  unsigned_image="localhost:5050/unsigned:latest"
fi

# 7. Run the tagged integration tests.
cd "$repo"
SSH_HOST=127.0.0.1 SSH_PORT=2222 SSH_USER=deploy SSH_KEY="$key" SSH_DEPLOY_PATH=/config/deploy/app \
FTP_HOST=127.0.0.1 FTP_PORT=21 FTP_USER=deploy FTP_PASSWORD=deploypass FTP_DEPLOY_PATH=index.html \
VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=roottoken \
COSIGN_PUB="$cosign_pub" COSIGN_SIGNED_IMAGE="$signed_image" COSIGN_UNSIGNED_IMAGE="$unsigned_image" \
  go test -tags integration -count=1 -v ./test/integration/...
