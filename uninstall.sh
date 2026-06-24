#!/usr/bin/env bash
# Removes the hidybot service. Pass --purge to also delete data + config.
set -euo pipefail
APP="hidybot"
[ "$(id -u)" -eq 0 ] || SUDO="sudo"; SUDO="${SUDO:-}"

$SUDO systemctl stop "$APP" 2>/dev/null || true
$SUDO systemctl disable "$APP" 2>/dev/null || true
$SUDO rm -f "/etc/systemd/system/${APP}.service"
$SUDO systemctl daemon-reload
$SUDO rm -f "/usr/local/bin/${APP}"

if [ "${1:-}" = "--purge" ]; then
  $SUDO rm -rf "/etc/${APP}" "/var/lib/${APP}"
  $SUDO userdel "$APP" 2>/dev/null || true
  echo "🗑 hidybot و همه داده‌ها حذف شدند."
else
  echo "✅ سرویس hidybot حذف شد. داده‌ها در /var/lib/${APP} نگه داشته شد (برای حذف کامل: --purge)."
fi
