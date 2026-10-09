#!/usr/bin/env bash
# 安装或升级 syntropy_update 服务（幂等：重复执行只会替换二进制并重启服务）。
#
# 用法：
#   sudo ./install.sh --binary ./syntropy-update [--addr 127.0.0.1:8443] \
#     [--public-base-url https://app.syntropy-lab.com] \
#     [--tls-domain app.syntropy-lab.com] [--tls-email ops@example.com] \
#     [--tls-addr :443] [--no-tls]
#
# 目录约定：
#   /opt/syntropy-update/syntropy-update   二进制
#   /etc/syntropy-update/update.json       配置（0640 root:syntropy-update，含管理令牌）
#   /etc/syntropy-update/keys/             签名私钥（0700 syntropy-update）
#   /var/lib/syntropy-update/              元数据 db 与内容寻址 blob
#   /var/lib/syntropy-update/data/acme/    ACME 证书缓存（重启不丢，避免重复签发）
#   /var/log/syntropy-update/              日志
#
# 明文管理面默认只监听 127.0.0.1。公网 HTTPS 另起一个监听器（默认 :443），
# 证书由 ACME 自动签发：tlsDomains 默认取 publicBaseURL 的域名，传 --no-tls 可关掉。
# 80 端口通常进不来，签发走 TLS-ALPN-01，只要 443/安全组放行即可。
set -euo pipefail

SERVICE_USER=syntropy-update
BIN_DIR=/opt/syntropy-update
CONF_DIR=/etc/syntropy-update
DATA_DIR=/var/lib/syntropy-update
LOG_DIR=/var/log/syntropy-update
UNIT_PATH=/etc/systemd/system/syntropy-update.service

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BINARY="$SCRIPT_DIR/syntropy-update"
ADDR=127.0.0.1:8443
PUBLIC_BASE_URL=https://app.syntropy-lab.com
TLS_ADDR=":443"
TLS_EMAIL=""
TLS_DOMAINS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --binary) BINARY="$2"; shift 2 ;;
    --addr) ADDR="$2"; shift 2 ;;
    --public-base-url) PUBLIC_BASE_URL="$2"; shift 2 ;;
    --tls-domain) TLS_DOMAINS+=("$2"); shift 2 ;;
    --tls-email) TLS_EMAIL="$2"; shift 2 ;;
    --tls-addr) TLS_ADDR="$2"; shift 2 ;;
    --no-tls) TLS_ADDR=""; shift ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# 默认用 publicBaseURL 的域名对外签发证书；--no-tls 时留空表示不启用 HTTPS。
if [ -n "$TLS_ADDR" ] && [ "${#TLS_DOMAINS[@]}" -eq 0 ]; then
  case "$PUBLIC_BASE_URL" in
    https://*) TLS_DOMAINS=("${PUBLIC_BASE_URL#https://}") ;;
  esac
fi
if [ "${#TLS_DOMAINS[@]}" -gt 0 ]; then
  TLS_DOMAINS=("${TLS_DOMAINS[0]%%/*}")
fi

json_string_array() {
  local out="" item
  for item in "$@"; do
    out="${out}\"${item}\","
  done
  printf '[%s]' "${out%,}"
}

if [ "$(id -u)" != "0" ]; then
  echo "must run as root (sudo $0 ...)" >&2
  exit 1
fi
if [ ! -f "$BINARY" ]; then
  echo "binary not found: $BINARY" >&2
  exit 1
fi
if [ ! -f "$SCRIPT_DIR/syntropy-update.service" ]; then
  echo "syntropy-update.service not found next to $0" >&2
  exit 1
fi

random_token() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
  else
    head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi

install -d -m 0755 "$BIN_DIR"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR" "$LOG_DIR"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR/data" "$DATA_DIR/data/acme"
install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$CONF_DIR" "$CONF_DIR/keys"
install -m 0755 "$BINARY" "$BIN_DIR/syntropy-update"

CONFIG="$CONF_DIR/update.json"
CREATED_TOKEN=""
CREATED_CLIENT_TOKEN=""
if [ ! -f "$CONFIG" ]; then
  CREATED_TOKEN="$(random_token)"
  CREATED_CLIENT_TOKEN="$(random_token)"
  cat >"$CONFIG" <<JSON
{
  "addr": "$ADDR",
  "publicBaseURL": "$PUBLIC_BASE_URL",
  "dataDir": "$DATA_DIR/data",
  "logDir": "$LOG_DIR",
  "logLevel": "info",
  "signingKeyFile": "$CONF_DIR/keys/update-signing.pem",
  "adminTokens": ["$CREATED_TOKEN"],
  "clientTokens": ["$CREATED_CLIENT_TOKEN"],
  "requireDeviceTokens": true,
  "requireReadTokens": false,
  "channels": ["alpha", "beta", "stable"],
  "defaultChannel": "stable",
  "tlsAddr": "$TLS_ADDR",
  "tlsDomains": $(json_string_array "${TLS_DOMAINS[@]}"),
  "tlsEmail": "$TLS_EMAIL",
  "tlsCacheDir": "$DATA_DIR/data/acme",
  "blobDir": "$DATA_DIR/data/blobs",
  "dbFile": "$DATA_DIR/data/update.db"
}
JSON
  echo "wrote config $CONFIG"
else
  echo "keeping existing config $CONFIG"
  # 老配置没有 requireDeviceTokens：上报类接口在 clientTokens 为空时仍然接受匿名写入。
  # 只提示、不动配置——收紧鉴权要先给所有目标端发好令牌，不然它们会一起上报失败。
  if ! grep -q '"requireDeviceTokens"' "$CONFIG"; then
    echo "note: $CONFIG 未设置 requireDeviceTokens；配了 clientTokens 才校验客户端令牌，"
    echo "      否则 /v1/plan、库存、忽略、升级结果接受匿名写入。收紧请加"
    echo "      \"clientTokens\": [\"<令牌>\"] 与 \"requireDeviceTokens\": true 后重启服务。"
  fi
  if grep -q '"requireReadTokens"[[:space:]]*:[[:space:]]*true' "$CONFIG"; then
    echo "note: $CONFIG 打开了 requireReadTokens：读取接口（清单 / 制品 / 分发定义）也要令牌，"
    echo "      thin 安装器必须把令牌烘进 bootstrap.json（--update-client-token），否则引导会 401。"
  fi
  # 老配置没有 TLS 字段时补进去（只动 tls* 四行，令牌与其它设置原样保留）。
  if ! grep -q '"tlsDomains"' "$CONFIG"; then
    if sed -i "s|^\([[:space:]]*\)\"blobDir\"|\1\"tlsAddr\": \"$TLS_ADDR\",\n\1\"tlsDomains\": $(json_string_array "${TLS_DOMAINS[@]}"),\n\1\"tlsEmail\": \"$TLS_EMAIL\",\n\1\"tlsCacheDir\": \"$DATA_DIR/data/acme\",\n\1\"blobDir\"|" "$CONFIG" &&
      grep -q '"tlsDomains"' "$CONFIG"; then
      echo "updated $CONFIG with TLS settings"
    else
      echo "note: 无法自动补 tlsDomains，HTTPS 不会启用；请手动编辑 $CONFIG。" >&2
    fi
  fi
fi
chown root:"$SERVICE_USER" "$CONFIG"
chmod 0640 "$CONFIG"

if [ ! -f "$CONF_DIR/keys/update-signing.pem" ]; then
  sudo -u "$SERVICE_USER" "$BIN_DIR/syntropy-update" keygen -config "$CONFIG"
fi

install -m 0644 "$SCRIPT_DIR/syntropy-update.service" "$UNIT_PATH"
systemctl daemon-reload
systemctl enable syntropy-update >/dev/null
systemctl restart syntropy-update

PORT="${ADDR##*:}"
echo -n "waiting for http://127.0.0.1:$PORT/healthz "
for _ in $(seq 1 30); do
  if curl -fsS -m 2 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    echo "ok"
    break
  fi
  echo -n "."
  sleep 1
done
curl -fsS -m 3 "http://127.0.0.1:$PORT/healthz" || { echo; systemctl status syntropy-update --no-pager -l | head -30; exit 1; }
echo

TOKEN="$(sed -n 's/.*"adminTokens"[[:space:]]*:[[:space:]]*\["\([^"]*\)".*/\1/p' "$CONFIG" | head -1)"
CLIENT_TOKEN="$(sed -n 's/.*"clientTokens"[[:space:]]*:[[:space:]]*\["\([^"]*\)".*/\1/p' "$CONFIG" | head -1)"
echo
echo "管理页   : http://127.0.0.1:$PORT/admin/   （对外域名：$PUBLIC_BASE_URL/admin/）"
if tr -d '\n' <"$CONFIG" | grep -Eq '"tlsDomains"[[:space:]]*:[[:space:]]*\[[[:space:]]*"'; then
  echo "HTTPS    : 已启用（证书自动签发，首次访问可能等几秒；tlsAddr/tlsDomains 见 $CONFIG）"
else
  echo "HTTPS    : 未启用（$PUBLIC_BASE_URL 需由前置代理或其它方式提供，或在 $CONFIG 里配 tlsDomains）"
fi
if [ -n "$TOKEN" ]; then
  echo "管理令牌 : $TOKEN"
fi
if [ -n "$CLIENT_TOKEN" ]; then
  echo "客户端令牌: $CLIENT_TOKEN"
  echo "          （目标端上报计划/库存/忽略/结果用；下发到目标端的 SYNTROPY_UPDATE_CLIENT_TOKEN）"
fi
if [ -n "$CREATED_TOKEN" ]; then
  echo "（令牌本次新生成，已写入 $CONFIG；请在管理页右上角粘贴后点「使用」）"
fi
if [ -n "$CREATED_CLIENT_TOKEN" ]; then
  echo "（客户端令牌本次新生成，已写入 $CONFIG；requireDeviceTokens=true，未带令牌的上报会被拒绝）"
fi
echo "公钥     : $("$BIN_DIR/syntropy-update" pubkey -config "$CONFIG" 2>/dev/null | tr -d '\n' | head -c 80)…"
echo "（完整公钥：sudo -u $SERVICE_USER $BIN_DIR/syntropy-update pubkey -config $CONFIG）"
