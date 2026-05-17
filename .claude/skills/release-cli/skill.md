---
name: release-cli
description: 发布 harness9 CLI 新版本。接受可选的 version 参数；若未提供，则自动将当前最新 tag 的 patch 号加 1。执行：切换到 master、拉取最新代码、创建 tag、推送 tag 触发 GoReleaser。
---

# release-cli — 发布 CLI 新版本

## 概述

将 harness9 CLI 发布为新版本。通过在 `master` 分支上创建版本 tag，触发 GitHub Actions 中的 GoReleaser 工作流，自动构建并发布二进制文件。

## 参数

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `version` | string | 否 | 要发布的版本号（如 `v0.1.5` 或 `0.1.5`）。若未提供，自动取当前最新 tag 的 patch+1 |

## 执行步骤

### 1. 确定版本号

**若用户提供了 `version` 参数：**

```bash
# 规范化：确保以 v 开头
version="v0.1.5"  # 示例，若用户输入 "0.1.5" 则补全为 "v0.1.5"
```

**若用户未提供 `version` 参数：**

```bash
# 获取当前最新 tag（按语义化版本排序）
latest=$(git tag --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)

# 若无任何 tag，从 v0.0.1 开始
if [ -z "$latest" ]; then
  version="v0.0.1"
else
  # 解析 major.minor.patch，将 patch+1
  # 例：v0.1.4 → v0.1.5
  version=$(echo "$latest" | awk -F'[v.]' '{printf "v%d.%d.%d", $2, $3, $4+1}')
fi

echo "当前最新版本：$latest"
echo "即将发布版本：$version"
```

在继续之前，**向用户展示计算出的版本号**，确认无误。

### 2. 前置检查

```bash
# 检查当前分支
git branch --show-current

# 检查工作区是否干净（有未提交内容时警告）
git status --short
```

**若当前不在 `master` 分支：**

提示用户切换：
```bash
git checkout master
```

**若工作区有未提交的改动：** 询问用户是否继续（未提交内容不影响 tag 发布，但可能意味着遗漏了提交）。

### 3. 拉取最新代码

```bash
git pull origin master
```

确认本地 `master` 与远程同步。

### 4. 确认 tag 不存在

```bash
git tag | grep "^${version}$"
```

若 tag 已存在，**停止执行**，提示用户该版本已发布，建议使用更高版本号。

### 5. 创建并推送 tag

```bash
# 创建轻量 tag
git tag "${version}"

# 推送 tag 到远程，触发 GitHub Actions release.yml
git push origin "${version}"
```

### 6. 确认发布触发

```bash
# 查看最近的 Actions 运行（需 gh CLI 已登录）
gh run list --limit 3
```

告知用户：
- tag 已推送：`${version}`
- GitHub Actions `release.yml` 已触发（由 `on: push: tags: ['v*']` 驱动）
- GoReleaser 将自动构建多平台二进制并创建 GitHub Release
- 可通过 `gh run list` 或 GitHub Actions 页面查看构建进度

## 常见错误

| 问题 | 处理 |
|------|------|
| tag 已存在 | 停止执行，建议使用更高版本号 |
| 不在 master 分支 | 提示切换到 master 后再发布 |
| push 被拒绝（无权限） | 检查 git remote 权限，确认有 push 到主仓库的权限 |
| `gh run list` 无输出 | 提示用户通过 GitHub 网页查看 Actions 执行状态 |
| 无任何历史 tag | 从 `v0.0.1` 开始，提示用户确认 |

## 发布流程图

```
确定版本号（用户指定 or patch+1）
    ↓
确认当前在 master 分支
    ↓
git pull origin master（同步最新）
    ↓
确认 tag 不存在
    ↓
git tag v{version}
    ↓
git push origin v{version}
    ↓
GitHub Actions release.yml 触发
    ↓
GoReleaser 构建多平台二进制
    ↓
GitHub Release 页面自动创建
```

## 注意事项

- 发布**必须从 `master` 分支**进行，确保发布的是经过 review 的代码
- 版本号遵循 [SemVer](https://semver.org/)：`vMAJOR.MINOR.PATCH`
- GoReleaser 配置见项目根目录 `.goreleaser.yaml`
- GitHub Actions release 工作流见 `.github/workflows/release.yml`
