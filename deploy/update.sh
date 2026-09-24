#!/usr/bin/env bash
set -euo pipefail
base=/opt/ourtaiko-fanmade
export PATH="$base/toolchains/go/bin:$PATH"
cd "$base/src/backend"
test -z "$(git status --porcelain)" || { echo 'Backend checkout has local changes'; exit 1; }
git pull --ff-only origin main
if sudo -n test -f "$base/docker-compose.yml"; then
  sudo docker compose -f "$base/docker-compose.yml" build fanmade
  sudo docker compose -f "$base/docker-compose.yml" up -d --wait --no-deps fanmade
  curl -fsS http://127.0.0.1:8080/readyz
  git rev-parse HEAD
  exit 0
fi
command -v cwebp >/dev/null || { echo 'Install WebP tools first: sudo apt-get install webp'; exit 1; }
cwebp -version >/dev/null
GOTOOLCHAIN=local go build -trimpath -o "$base/bin/ourtaiko-api.next" ./cmd/server
if test -f "$base/bin/ourtaiko-api"; then
  cp -p "$base/bin/ourtaiko-api" "$base/bin/ourtaiko-api.previous"
fi
mv "$base/bin/ourtaiko-api.next" "$base/bin/ourtaiko-api"
sudo systemctl restart ourtaiko-fanmade
for attempt in {1..20}; do
  if curl -fsS http://127.0.0.1:8080/readyz; then
    git rev-parse HEAD
    exit 0
  fi
  sleep 1
done
echo 'API did not become ready; inspect journalctl -u ourtaiko-fanmade'
exit 1
