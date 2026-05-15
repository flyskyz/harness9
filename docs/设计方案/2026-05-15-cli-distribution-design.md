# harness9 CLI 分发方案设计

> 日期：2026-05-15
> 状态：已批准，待实施

---

## 1. 背景与目标

harness9 当前只能从源码编译运行。本次目标是让**外部技术用户**无需 Go 环境，通过 `curl | bash` 一行命令将 `harness9` 安装到本地，并在任意工作目录下直接使用。

**成功标准：**
- 用户执行 `curl -fsSL https://raw.githubusercontent.com/harness9/harness9/master/scripts/install.sh | bash` 完成安装
- 安装后 `harness9 --version` 输出正确版本号
- 在任意目录运行 `harness9`，工具沙箱自动绑定到当前目录
- 开发者推送 `git tag v*` 后，GitHub Actions 自动完成构建、校验、发布全流程

**不在本次范围内：**
- Windows 支持
- 交互式配置向导（纯环境变量管理）
- 会话持久化、多渠道等 roadmap 功能

---

## 2. 方案选型

采用 **GoReleaser + GitHub Actions**（方案 A）。

| 方案 | 描述 | 结论 |
|------|------|------|
| A. GoReleaser + GitHub Actions | 行业标准，自动化 checksum、Homebrew formula | ✅ 采用 |
| B. 手写 GitHub Actions Matrix | 零工具依赖，但 checksum/Homebrew 需手动维护 | ❌ 弃用 |
| C. Makefile 本地构建 + 手动上传 | 最快 MVP，但不可持续 | ❌ 弃用 |

GoReleaser 是 Go 生态分发标准（Homebrew 官方核心工具同款），与 OpenCode、Codex CLI 实践一致。一次配置，`git tag + git push` 即可触发完整发布流水线。

---

## 3. 目标平台

| 平台 | 架构 | 优先级 |
|------|------|--------|
| macOS (darwin) | arm64 (Apple Silicon) | 主要 |
| macOS (darwin) | amd64 (Intel) | 主要 |
| Linux | arm64 | 次要 |
| Linux | amd64 | 次要 |

`CGO_ENABLED=0` 确保纯静态二进制，Linux 无 glibc 版本依赖。

---

## 4. 新增文件清单

```
harness9/
├── .goreleaser.yaml
├── .github/
│   └── workflows/
│       ├── ci.yml
│       └── release.yml
└── scripts/
    └── install.sh
```

**现有代码改动：** `cmd/harness9/main.go` 新增 `version` 包级变量 + `--version` flag，共 2 行。

---

## 5. 详细设计

### 5.1 `.goreleaser.yaml`

```yaml
version: 2
project_name: harness9

builds:
  - id: harness9
    main: ./cmd/harness9
    binary: harness9
    env:
      - CGO_ENABLED=0
    goos: [darwin, linux]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}

archives:
  - id: harness9
    format: tar.gz
    name_template: "harness9_{{.Version}}_{{.Os}}_{{.Arch}}"
    files: [LICENSE, README.md]

checksum:
  name_template: "harness9_{{.Version}}_SHA256SUMS"
  algorithm: sha256

release:
  github:
    owner: harness9
    name: harness9
  draft: false
  prerelease: auto

brews:
  - name: harness9
    repository:
      owner: harness9
      name: homebrew-tap
    homepage: "https://github.com/harness9/harness9"
    description: "轻量级、生产可用的 Agent Harness CLI"
    install: |
      bin.install "harness9"
    test: |
      system "#{bin}/harness9", "--version"
```

### 5.2 `.github/workflows/ci.yml`

```yaml
name: CI
on:
  push:
    branches: [master]
  pull_request:
    branches: [master]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go test ./...
      - run: go build ./cmd/harness9
```

### 5.3 `.github/workflows/release.yml`

```yaml
name: Release
on:
  push:
    tags: ["v*"]

jobs:
  release:
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          # HOMEBREW_TAP_GITHUB_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
```

### 5.4 `scripts/install.sh`

```bash
#!/usr/bin/env bash
set -euo pipefail

REPO="harness9/harness9"
BINARY="harness9"
INSTALL_DIR="/usr/local/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "不支持的操作系统: $OS" && exit 1 ;;
esac

ARCH=$(uname -m)
case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "不支持的架构: $ARCH" && exit 1 ;;
esac

VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep '"tag_name"' | sed 's/.*"tag_name": "\(.*\)".*/\1/')

TARBALL="harness9_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${TARBALL}"
CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/harness9_${VERSION}_SHA256SUMS"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "下载 harness9 ${VERSION} (${OS}/${ARCH})..."
curl -fsSL "$URL" -o "$TMP/$TARBALL"
curl -fsSL "$CHECKSUM_URL" -o "$TMP/SHA256SUMS"

cd "$TMP"
# macOS 兼容：优先用 sha256sum，降级到 shasum -a 256
if command -v sha256sum &>/dev/null; then
  grep "$TARBALL" SHA256SUMS | sha256sum -c -
else
  grep "$TARBALL" SHA256SUMS | shasum -a 256 -c -
fi

tar -xzf "$TARBALL"
install -m755 "$BINARY" "$INSTALL_DIR/$BINARY"

echo "✅ harness9 ${VERSION} 已安装到 ${INSTALL_DIR}/${BINARY}"
echo "运行 'harness9 --help' 开始使用"
```

### 5.5 `main.go` 改动

```go
var version = "dev" // goreleaser ldflags 在发布时注入

func main() {
    versionFlag := flag.Bool("version", false, "打印版本号并退出")
    feishuMode  := flag.Bool("feishu", false, "...")
    flag.Parse()

    if *versionFlag {
        fmt.Println("harness9 " + version)
        return
    }
    // 以下完全不变
}
```

---

## 6. 发布流程（运营手册）

```bash
# 1. 确保所有测试通过
go test ./...

# 2. 打 tag（遵循 semver）
git tag v0.1.0
git push origin v0.1.0

# 3. GitHub Actions 自动触发 release.yml
# 约 3-5 分钟后，GitHub Releases 页面出现：
#   - harness9_v0.1.0_darwin_arm64.tar.gz
#   - harness9_v0.1.0_darwin_amd64.tar.gz
#   - harness9_v0.1.0_linux_arm64.tar.gz
#   - harness9_v0.1.0_linux_amd64.tar.gz
#   - harness9_v0.1.0_SHA256SUMS
```

---

## 7. 用户文档片段（README 更新内容）

```markdown
## 安装

### 一键安装（推荐）
curl -fsSL https://raw.githubusercontent.com/harness9/harness9/master/scripts/install.sh | bash

### Homebrew（macOS）
brew install harness9/tap/harness9

### 配置
export OPENAI_API_KEY="your-key-here"
# 可选：
export LLM_MODEL="openai/gpt-4o"
export OPENAI_BASE_URL="https://..."

## 使用
cd /your/project
harness9
```

---

## 8. 运行时行为说明

`main.go` 已使用 `os.Getwd()` 作为默认 workDir，全局安装后：

- 用户在任意目录运行 `harness9`，工具沙箱自动绑定到当前目录
- 当前目录存在 `.env` 文件时自动加载，不存在时静默跳过
- `WORK_DIR` 环境变量可显式覆盖工作目录

**无需修改现有引擎、工具、TUI 任何逻辑。**
