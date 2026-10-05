#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $(id -u) != 0 ]]; then
  echo 'Run this installer with sudo.' >&2
  exit 1
fi
for command in docker systemctl systemd-analyze; do command -v "$command" >/dev/null; done
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
unit=ourtaiko-fanmade-backup
active=$(systemctl show "$unit.service" --property=ActiveState --value)
if [[ "$active" != inactive && "$active" != failed && -n "$active" ]]; then
  echo 'A backup is running; wait for completion before installing.' >&2
  exit 1
fi
test -r /etc/ourtaiko-fanmade/docker/storage.env
build=$(mktemp -d)
trap 'rm -rf -- "$build"' EXIT
docker build --file "$repo/deploy/backup/Dockerfile" --output "type=local,dest=$build" "$repo"

# Keep previous installed units and executable independently of data retention.
previous="/var/backups/ourtaiko-fanmade/backup-install-$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 "$previous" /var/backups/ourtaiko-fanmade/daily
for file in /usr/local/libexec/ourtaiko-fanmade-backup "/etc/systemd/system/$unit.service" "/etc/systemd/system/$unit.timer" /etc/ourtaiko-fanmade/backup.env; do
  if [[ -f "$file" ]]; then cp -p -- "$file" "$previous/"; fi
done
install -d -m 0755 /usr/local/libexec
mkdir -p /etc/ourtaiko-fanmade
install -m 0755 "$build/ourtaiko-fanmade-backup" /usr/local/libexec/ourtaiko-fanmade-backup.new
mv -f /usr/local/libexec/ourtaiko-fanmade-backup.new /usr/local/libexec/ourtaiko-fanmade-backup
if [[ ! -e /etc/ourtaiko-fanmade/backup.env ]]; then
  install -m 0600 "$repo/deploy/backup/backup.env.example" /etc/ourtaiko-fanmade/backup.env
fi
install -m 0644 "$repo/deploy/backup/$unit.service" "/etc/systemd/system/$unit.service"
install -m 0644 "$repo/deploy/backup/$unit.timer" "/etc/systemd/system/$unit.timer"
systemd-analyze verify "/etc/systemd/system/$unit.service" "/etc/systemd/system/$unit.timer"
systemctl daemon-reload
if [[ "$active" == failed ]]; then systemctl reset-failed "$unit.service"; fi
# Verify a real backup before enabling the daily schedule.
systemctl start "$unit.service"
systemctl enable --now "$unit.timer"
systemctl list-timers "$unit.timer" --no-pager
echo "Installed. Previous installation files: $previous"
