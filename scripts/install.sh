#!/usr/bin/env bash
set -euo pipefail

REPO="ZhangShenao/harness9"
BINARY="harness9"
INSTALL_DIR="/usr/local/bin"

# ── 检测操作系统 ──────────────────────────────────────────
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "错误：不支持的操作系统 $OS" >&2; exit 1 ;;
esac

# ── 检测 CPU 架构 ─────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "错误：不支持的架构 $ARCH" >&2; exit 1 ;;
esac

# ── 获取最新版本号 ─────────────────────────────────────────
echo "正在查询最新版本..."
API_RESPONSE=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest")
if command -v jq &>/dev/null; then
  VERSION=$(printf '%s' "$API_RESPONSE" | jq -r .tag_name)
else
  VERSION=$(printf '%s' "$API_RESPONSE" | grep -o '"tag_name":"[^"]*"' | grep -o '[^"]*"$' | tr -d '"')
fi

if [ -z "$VERSION" ] || [ "$VERSION" = "null" ]; then
  echo "错误：无法获取版本号，请检查网络或仓库地址" >&2
  exit 1
fi

TARBALL="${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
URL="${BASE_URL}/${TARBALL}"
CHECKSUM_URL="${BASE_URL}/${BINARY}_${VERSION}_SHA256SUMS"

# ── 下载到临时目录 ─────────────────────────────────────────
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "下载 ${BINARY} ${VERSION} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o "$TMP/$TARBALL"
curl -fsSL "$CHECKSUM_URL" -o "$TMP/SHA256SUMS"

# ── 校验 SHA256 ────────────────────────────────────────────
if command -v sha256sum &>/dev/null; then
  grep "$TARBALL" "$TMP/SHA256SUMS" | (cd "$TMP" && sha256sum -c -)
elif command -v shasum &>/dev/null; then
  grep "$TARBALL" "$TMP/SHA256SUMS" | (cd "$TMP" && shasum -a 256 -c -)
else
  echo "警告：未找到 sha256sum 或 shasum，跳过校验" >&2
fi

# ── 解压并安装 ─────────────────────────────────────────────
tar -xzf "$TMP/$TARBALL" -C "$TMP"

if [ ! -w "$INSTALL_DIR" ]; then
  echo "需要 sudo 权限写入 ${INSTALL_DIR}..."
  sudo install -m755 "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
else
  install -m755 "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
fi

echo ""
echo "✅ ${BINARY} ${VERSION} 已安装到 ${INSTALL_DIR}/${BINARY}"
echo ""
echo "快速开始："
echo "  export OPENAI_API_KEY=\"your-key\""
echo "  cd /your/project && ${BINARY}"
