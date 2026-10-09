#!/usr/bin/env bash
set -euo pipefail
umask 077

if [[ $(id -u) != 0 ]]; then
  echo 'Run this installer with sudo.' >&2
  exit 1
fi
for command in systemctl systemd-analyze; do command -v "$command" >/dev/null; done
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
unit=ourtaiko-sso-backup
# The shared executable must be updated via install.sh before installing this unit.
test -x /usr/local/libexec/ourtaiko-fanmade-backup
test -r /etc/ourtaiko-sso/docker/sso.env
test -r /etc/ourtaiko-sso/backup.env
if grep -q 'REPLACE_WITH_' /etc/ourtaiko-sso/backup.env; then
  echo 'Configure the private backup bucket before installation.' >&2
  exit 1
fi
active=$(systemctl show "$unit.service" --property=ActiveState --value)
if [[ "$active" != inactive && "$active" != failed && -n "$active" ]]; then
  echo 'An SSO backup is running; wait for completion.' >&2
  exit 1
fi
previous="/var/backups/ourtaiko-sso/backup-install-$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 "$previous" /var/backups/ourtaiko-sso/daily
for file in "/etc/systemd/system/$unit.service" "/etc/systemd/system/$unit.timer" /etc/ourtaiko-sso/backup.env; do
  if [[ -f "$file" ]]; then cp -p -- "$file" "$previous/"; fi
done
install -m 0644 "$repo/deploy/backup/$unit.service" "/etc/systemd/system/$unit.service"
install -m 0644 "$repo/deploy/backup/$unit.timer" "/etc/systemd/system/$unit.timer"
systemd-analyze verify "/etc/systemd/system/$unit.service" "/etc/systemd/system/$unit.timer"
systemctl daemon-reload
if [[ "$active" == failed ]]; then systemctl reset-failed "$unit.service"; fi
# The daily schedule is enabled only after a complete backup and S3 readback.
systemctl start "$unit.service"
systemctl enable --now "$unit.timer"
systemctl list-timers "$unit.timer" --no-pager
echo "Installed. Previous SSO backup installation files: $previous"
