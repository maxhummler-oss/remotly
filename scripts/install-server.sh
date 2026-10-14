#!/bin/sh
# Remotly server installer for Linux (systemd).
#
# From a release archive:   sudo ./install.sh
# From the source tree:     sudo scripts/install-server.sh   (builds with Go)
set -eu

if [ "$(id -u)" -ne 0 ]; then
  echo "Bitte als root ausführen (sudo $0)" >&2
  exit 1
fi

HERE=$(cd "$(dirname "$0")" && pwd)
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "Nicht unterstützte Architektur: $ARCH" >&2; exit 1 ;;
esac

# --- locate or build binaries ------------------------------------------------
if [ -x "$HERE/remotly-server" ] && [ -d "$HERE/agents" ]; then
  SERVER_BIN="$HERE/remotly-server"
  AGENTS_SRC="$HERE/agents"
elif [ -f "$HERE/../go.mod" ]; then
  if ! command -v go >/dev/null 2>&1 && [ ! -x /usr/local/go/bin/go ]; then
    echo "Go ist nicht installiert. Release-Archiv verwenden oder Go installieren (https://go.dev/dl/)." >&2
    exit 1
  fi
  PATH="$PATH:/usr/local/go/bin"
  echo "==> Baue Server und Agents (alle Plattformen)"
  (cd "$HERE/.." && make server agents)
  SERVER_BIN="$HERE/../dist/remotly-server-linux-$ARCH"
  AGENTS_SRC="$HERE/../dist/agents"
else
  echo "Binärdateien nicht gefunden." >&2
  exit 1
fi

# --- install -----------------------------------------------------------------
echo "==> Installiere nach /usr/local"
id remotly >/dev/null 2>&1 || useradd --system --home-dir /var/lib/remotly --shell /usr/sbin/nologin remotly
install -m 0755 "$SERVER_BIN" /usr/local/bin/remotly-server
install -d -m 0755 /usr/local/share/remotly/agents
rm -f /usr/local/share/remotly/agents/remotly-agent-*
install -m 0644 "$AGENTS_SRC"/remotly-agent-* /usr/local/share/remotly/agents/
install -d -m 0750 -o root -g remotly /etc/remotly
install -d -m 0700 -o remotly -g remotly /var/lib/remotly

if [ ! -f /etc/remotly/server.json ]; then
  # Writes the default config and initializes state (keys, CA on first start).
  /usr/local/bin/remotly-server -config /etc/remotly/server.json enroll-key >/dev/null
fi
chown root:remotly /etc/remotly/server.json
chmod 0640 /etc/remotly/server.json
chown -R remotly:remotly /var/lib/remotly

cat > /etc/systemd/system/remotly.service <<'EOF'
[Unit]
Description=Remotly server (remote desktop hub)
After=network-online.target
Wants=network-online.target

[Service]
User=remotly
Group=remotly
ExecStart=/usr/local/bin/remotly-server -config /etc/remotly/server.json run
Restart=always
RestartSec=2
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
ReadWritePaths=/var/lib/remotly
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable remotly.service >/dev/null
systemctl restart remotly.service

# --- firewall ----------------------------------------------------------------
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  echo "==> Öffne Ports in ufw"
  ufw allow 443/tcp >/dev/null; ufw allow 80/tcp >/dev/null
  ufw allow 3478/udp >/dev/null; ufw allow 50000:50999/udp >/dev/null
elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
  echo "==> Öffne Ports in firewalld"
  firewall-cmd --permanent --add-port=443/tcp --add-port=80/tcp --add-port=3478/udp --add-port=50000-50999/udp >/dev/null
  firewall-cmd --reload >/dev/null
fi

sleep 1
IP=$(ip route get 192.0.2.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p')
FP=""
if [ -f /var/lib/remotly/ca.crt ] && command -v openssl >/dev/null 2>&1; then
  FP=$(openssl x509 -in /var/lib/remotly/ca.crt -outform DER | sha256sum | cut -d' ' -f1)
fi

# --- first admin ---------------------------------------------------------------
if ! /usr/local/bin/remotly-server -config /etc/remotly/server.json user list | grep -q .; then
  if [ -t 0 ] || [ -e /dev/tty ]; then
    echo
    echo "==> Ersten Administrator anlegen"
    printf "Benutzername [admin]: "
    read -r NAME </dev/tty || NAME=""
    NAME=${NAME:-admin}
    /usr/local/bin/remotly-server -config /etc/remotly/server.json user add "$NAME" --admin </dev/tty
  else
    echo "Ersten Administrator anlegen mit:"
    echo "    sudo remotly-server -config /etc/remotly/server.json user add admin --admin"
  fi
fi

echo
echo "=============================================================="
echo " Remotly läuft:  https://${IP:-<server-ip>}/"
echo "=============================================================="
echo " Status:      systemctl status remotly"
echo " Log:         journalctl -u remotly -f"
echo " Benutzer:    sudo remotly-server -config /etc/remotly/server.json user add NAME"
echo " CA-Zertifikat für Thin Clients: https://${IP:-<server-ip>}/ca.crt"
[ -n "$FP" ] && echo " CA-Fingerabdruck (SHA-256): $FP"
echo
echo " Geräte hinzufügen: Weboberfläche > Verwaltung > Gerät hinzufügen"
