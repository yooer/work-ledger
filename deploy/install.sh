#!/usr/bin/env bash
# 工作台账 work-ledger 一键部署脚本（Linux + systemd）
#
#   curl -fsSL https://github.com/yooer/work-ledger/raw/main/deploy/install.sh | sudo bash
#   curl -fsSL https://github.com/yooer/work-ledger/raw/main/deploy/install.sh | sudo bash -s -- --listen 0.0.0.0:8080 --admin-password '至少8位'
#
# 已安装时再次运行会自动转为升级（等同 wlctl upgrade）。
set -euo pipefail

BASE_URL="${WL_BASE_URL:-https://github.com/yooer/work-ledger}"
# 备用下载地址：官方地址连接失败时自动使用
MIRROR_URL="${WL_MIRROR_URL:-}"
VERSION="${WL_VERSION:-latest}"
WL_HOME="${WL_HOME_DIR:-/home/work-ledger}"
RUN_USER="${WL_USER:-work-ledger}"
LISTEN="${WL_LISTEN:-127.0.0.1:8080}"
ADMIN_USER="admin"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
DEMO=0

usage() {
  cat <<USAGE
用法：install.sh [选项]
  --version vX.Y.Z        安装指定版本（默认最新版）
  --listen 地址:端口      监听地址（默认 127.0.0.1:8080，建议配合 Nginx/Caddy 反代）
  --admin-user 名称       管理员登录名（默认 admin）
  --admin-password 密码   管理员初始密码（至少 8 位；不填则随机生成并打印）
  --home 目录             运行目录（默认 /home/work-ledger）
  --user 账号             运行账号（默认 work-ledger）
  --base-url URL          下载源（默认 https://github.com/yooer/work-ledger）
  --demo                  同时创建演示数据
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --listen) LISTEN="$2"; shift 2 ;;
    --admin-user) ADMIN_USER="$2"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift 2 ;;
    --home) WL_HOME="$2"; shift 2 ;;
    --user) RUN_USER="$2"; shift 2 ;;
    --base-url) BASE_URL="${2%/}"; shift 2 ;;
    --demo) DEMO=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数：$1"; usage; exit 2 ;;
  esac
done

info() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[警告]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[错误]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "请使用 root 运行（sudo bash install.sh）。"
[ "$(uname -s)" = "Linux" ] || die "该脚本只支持 Linux，Windows 请使用 install.ps1。"
command -v systemctl >/dev/null 2>&1 || die "系统没有 systemd，无法注册服务。"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "不支持的 CPU 架构：$(uname -m)（仅支持 amd64 / arm64）" ;;
esac
ASSET="work-ledger-linux-${ARCH}"
BIN="${WL_HOME}/bin/work-ledger"

if [ -z "$MIRROR_URL" ]; then
  case "$BASE_URL" in https://github.com/*) MIRROR_URL="https://github.tool.do/${BASE_URL#https://github.com/}" ;; esac
fi

fetch() { # url 输出文件
  if command -v curl >/dev/null 2>&1; then
    curl -fL --retry 2 --connect-timeout 10 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 15 -t 2 -O "$2" "$1"
  else
    die "需要 curl 或 wget。"
  fi
}

release_path() { # 文件名
  if [ "$VERSION" = "latest" ]; then echo "/releases/latest/download/$1"; else echo "/releases/download/${VERSION}/$1"; fi
}

# download 先从官方地址下载，失败时自动改用备用地址。
download() { # 文件名 输出文件
  local path; path="$(release_path "$1")"
  if fetch "${BASE_URL}${path}" "$2" 2>/dev/null; then return 0; fi
  if [ -n "$MIRROR_URL" ] && [ "$MIRROR_URL" != "$BASE_URL" ]; then
    warn "官方地址下载失败，改用备用地址 …"
    fetch "${MIRROR_URL}${path}" "$2" && return 0
  fi
  die "下载 $1 失败，请检查服务器网络。"
}

# ---------- 已安装：转为升级 ----------
if [ -x "$BIN" ]; then
  info "检测到已安装（${BIN}），执行升级 …"
  if [ "$VERSION" = "latest" ]; then
    WL_HOME="$WL_HOME" "$BIN" ctl upgrade -y
  else
    WL_HOME="$WL_HOME" "$BIN" ctl upgrade "$VERSION" -y
  fi
  exit $?
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "下载 ${ASSET}（${VERSION}）…"
download sha256sums.txt "$TMP/sha256sums.txt"
download "$ASSET" "$TMP/$ASSET"
EXPECT="$(awk -v f="$ASSET" '$2==f || $2=="*"f {print $1}' "$TMP/sha256sums.txt")"
[ -n "$EXPECT" ] || die "sha256sums.txt 中没有 ${ASSET}"
ACTUAL="$(sha256sum "$TMP/$ASSET" | awk '{print $1}')"
[ "$EXPECT" = "$ACTUAL" ] || die "sha256 校验失败：期望 ${EXPECT}，实际 ${ACTUAL}"
info "sha256 校验通过。"
chmod +x "$TMP/$ASSET"
"$TMP/$ASSET" version >/dev/null || die "下载的程序无法运行。"

# ---------- 用户与目录 ----------
if ! id "$RUN_USER" >/dev/null 2>&1; then
  info "创建系统用户 ${RUN_USER} …"
  NOLOGIN="$(command -v nologin || echo /usr/sbin/nologin)"
  useradd --system --home-dir "$WL_HOME" --no-create-home --shell "$NOLOGIN" "$RUN_USER"
fi
mkdir -p "$WL_HOME"/{bin,config,data,backups,logs,tmp}
install -m 0755 "$TMP/$ASSET" "$BIN"

randhex() { od -An -tx1 -N"$1" /dev/urandom | tr -d ' \n'; }
ENV_FILE="$WL_HOME/config/.env"
if [ ! -f "$ENV_FILE" ]; then
  info "生成配置文件 ${ENV_FILE} …"
  cat > "$ENV_FILE" <<ENV
# ===== 基础配置 =====
APP_NAME="工作台账"
APP_ENV=production
APP_DEBUG=false
APP_TIMEZONE=Asia/Shanghai
# 对外访问地址（用于页面展示接口示例），使用反向代理时改成域名
APP_URL=http://${LISTEN}
LISTEN_ADDR=${LISTEN}
SESSION_NAME=work_ledger_session
SESSION_LIFETIME_HOURS=12

# ===== AI 接口 =====
# 外部 AI 调用 /api/v1 的 Bearer 令牌（已随机生成）
AI_API_TOKEN=$(randhex 32)
# 任何兼容 OpenAI Chat Completions 协议的服务均可
AI_BASE_URL=https://api.deepseek.com/v1
AI_MODEL=deepseek-chat
AI_API_KEY=
AI_TIMEOUT=120

# ===== 二次验证 =====
TOTP_ISSUER="工作台账"
REQUIRE_2FA=false

# ===== 升级源 =====
UPDATE_BASE_URL=${BASE_URL}
ENV
fi
chown -R "$RUN_USER:$RUN_USER" "$WL_HOME"
chmod 750 "$WL_HOME"
chmod 600 "$ENV_FILE"

# ---------- 初始化数据库 ----------
info "初始化数据库 …"
INSTALL_ARGS=(install "--admin-user=$ADMIN_USER")
[ -n "$ADMIN_PASSWORD" ] && INSTALL_ARGS+=("--admin-password=$ADMIN_PASSWORD")
[ "$DEMO" = "1" ] && INSTALL_ARGS+=(--demo)
if command -v runuser >/dev/null 2>&1; then
  runuser -u "$RUN_USER" -- env WL_HOME="$WL_HOME" "$BIN" "${INSTALL_ARGS[@]}" | tee "$TMP/install.log"
else
  su -s /bin/sh "$RUN_USER" -c "WL_HOME='$WL_HOME' '$BIN' $(printf "'%s' " "${INSTALL_ARGS[@]}")" | tee "$TMP/install.log"
fi

# ---------- 注册服务 ----------
info "注册 systemd 服务 …"
WL_HOME="$WL_HOME" "$BIN" service install --user "$RUN_USER"
systemctl restart work-ledger

info "等待服务启动 …"
HOST="${LISTEN%:*}"; PORT="${LISTEN##*:}"
case "$HOST" in ""|0.0.0.0|"[::]"|::) HOST=127.0.0.1 ;; esac
OK=0
for _ in $(seq 1 30); do
  if curl -fsS "http://${HOST}:${PORT}/api/v1/health" >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done
[ "$OK" = "1" ] || { warn "服务未通过健康检查，请执行 wlctl logs 查看日志。"; exit 1; }

echo
info "部署完成！"
echo "  访问地址：http://${LISTEN}"
echo "  运行目录：${WL_HOME}"
echo "  配置文件：${ENV_FILE}（配置 AI_API_KEY 后执行 sudo wlctl restart）"
echo "  管理命令：wlctl status | logs | check | upgrade | backup（需要 sudo 的命令会提示）"
grep -E "初始密码|管理员账号|演示账号" "$TMP/install.log" || true
case "$LISTEN" in 127.0.0.1:*|localhost:*) echo "  提示：当前只监听本机，请配置 Nginx/Caddy 反向代理对外提供 HTTPS 访问（见 README）。" ;; esac
