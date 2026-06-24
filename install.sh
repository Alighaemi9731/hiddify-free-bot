#!/usr/bin/env bash
#
# One-line installer for the Hiddify free-config Telegram bot.
#
#   curl -fsSL https://raw.githubusercontent.com/Alighaemi9731/hiddify-free-bot/main/install.sh | bash
#
# Downloads the latest release binary, asks for the bot token + admin id and
# installs a systemd service. Re-running upgrades an existing install.

set -euo pipefail

REPO="Alighaemi9731/hiddify-free-bot"
APP="hidybot"
BIN="/usr/local/bin/${APP}"
CONF_DIR="/etc/${APP}"
CONF="${CONF_DIR}/config.env"
DATA_DIR="/var/lib/${APP}"
SVC="/etc/systemd/system/${APP}.service"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
say()  { echo -e "${GREEN}$*${NC}"; }
warn() { echo -e "${YELLOW}$*${NC}"; }
die()  { echo -e "${RED}خطا: $*${NC}" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || SUDO="sudo"
SUDO="${SUDO:-}"

command -v curl >/dev/null 2>&1 || die "curl نصب نیست. ابتدا curl را نصب کنید."

# --- detect platform ---
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
[ "$OS" = "linux" ] || die "این نصب‌کننده فقط روی Linux (اوبونتو) کار می‌کند."
case "$(uname -m)" in
  x86_64|amd64)   ARCH="amd64" ;;
  aarch64|arm64)  ARCH="arm64" ;;
  armv7l|armv7)   ARCH="arm" ;;
  *) die "معماری پشتیبانی‌نشده: $(uname -m)" ;;
esac
ASSET="${APP}_linux_${ARCH}"
URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"

say "⬇️  دانلود ${APP} (${ARCH})..."
TMP="$(mktemp)"
curl -fL --retry 3 -o "$TMP" "$URL" || die "دانلود باینری ناموفق بود ($URL)"
chmod +x "$TMP"
$SUDO mv "$TMP" "$BIN"
say "✅ باینری در ${BIN} نصب شد."

# --- gather credentials (read from the real terminal even when piped) ---
if [ -t 0 ]; then TTY=/dev/stdin; else TTY=/dev/tty; fi

# Keep an existing config on upgrade unless the user wants to change it.
if [ -f "$CONF" ]; then
  warn "پیکربندی قبلی پیدا شد. برای حفظ مقادیر فعلی Enter بزنید."
fi

printf "🤖 توکن ربات تلگرام را وارد کنید: "
read -r BOT_TOKEN < "$TTY" || true
printf "👤 آیدی عددی ادمین را وارد کنید: "
read -r ADMIN_ID < "$TTY" || true

if [ -f "$CONF" ]; then
  # shellcheck disable=SC1090
  OLD_TOKEN="$(grep -E '^BOT_TOKEN=' "$CONF" | cut -d= -f2- || true)"
  OLD_ADMIN="$(grep -E '^ADMIN_ID=' "$CONF" | cut -d= -f2- || true)"
  [ -z "${BOT_TOKEN:-}" ] && BOT_TOKEN="$OLD_TOKEN"
  [ -z "${ADMIN_ID:-}" ] && ADMIN_ID="$OLD_ADMIN"
fi

[ -n "${BOT_TOKEN:-}" ] || die "توکن ربات نمی‌تواند خالی باشد."
case "${ADMIN_ID:-}" in (''|*[!0-9]*) die "آیدی ادمین باید عددی باشد." ;; esac

# --- write config ---
$SUDO mkdir -p "$CONF_DIR" "$DATA_DIR"
$SUDO tee "$CONF" >/dev/null <<EOF
BOT_TOKEN=${BOT_TOKEN}
ADMIN_ID=${ADMIN_ID}
DATA_DIR=${DATA_DIR}
EOF
$SUDO chmod 600 "$CONF"

# --- dedicated service user ---
if ! id "$APP" >/dev/null 2>&1; then
  $SUDO useradd --system --no-create-home --shell /usr/sbin/nologin "$APP" || true
fi
$SUDO chown -R "$APP:$APP" "$DATA_DIR"
$SUDO chown "$APP:$APP" "$CONF"

# --- systemd unit ---
$SUDO tee "$SVC" >/dev/null <<EOF
[Unit]
Description=Hiddify free-config Telegram bot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${APP}
Group=${APP}
EnvironmentFile=${CONF}
WorkingDirectory=${DATA_DIR}
ExecStart=${BIN}
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=full
PrivateTmp=yes
MemoryMax=256M

[Install]
WantedBy=multi-user.target
EOF

say "🚀 راه‌اندازی سرویس..."
$SUDO systemctl daemon-reload
$SUDO systemctl enable "$APP" >/dev/null 2>&1 || true
$SUDO systemctl restart "$APP"

sleep 2
if $SUDO systemctl is-active --quiet "$APP"; then
  say "✅ ربات با موفقیت نصب و اجرا شد!"
  echo
  echo "  وضعیت:  $SUDO systemctl status $APP"
  echo "  لاگ:    $SUDO journalctl -u $APP -f"
  echo "  حذف:    curl -fsSL https://raw.githubusercontent.com/${REPO}/main/uninstall.sh | bash"
  echo
  echo "حالا در تلگرام به ربات /start بده و از منوی مدیریت پنل و کانال اضافه کن."
else
  warn "سرویس فعال نشد. لاگ را ببینید:"
  echo "  $SUDO journalctl -u $APP -n 50 --no-pager"
  exit 1
fi
