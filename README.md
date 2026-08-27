# ezbook 魔改版

这是一个个人记账应用的源码发布副本，包含当前 Go/Vue 服务端和 SwiftUI 客户端。
发布副本不包含本地数据库、账单、上传文件、日志、备份、会话文件、依赖目录或密钥。

## Docker 本地运行

```bash
cp .env.example .env
# 编辑 .env，至少设置 EBK_SECRET_KEY
docker compose --env-file .env up --build -d
```

服务默认只监听 `127.0.0.1:8080`。数据、上传文件和日志写入 Docker named volumes，
不会进入源码仓库或镜像构建上下文。

## 构建并推送镜像

镜像名称必须使用小写。登录 GitHub Container Registry 后执行：

```bash
export GHCR_IMAGE=ghcr.io/<github-owner>/ezbook-magic
docker buildx build --platform linux/amd64,linux/arm64 \
  --tag "$GHCR_IMAGE:latest" --push .
```

推送到 `main` 或创建 `v*` 标签后，`.github/workflows/docker-publish.yml` 也会使用
GitHub Actions 自动构建并发布 `linux/amd64` 镜像到该仓库的 GHCR。Dockerfile 保留
ARM64 构建支持，可在 ARM64 Docker builder 上按同样命令构建。

## 目录

- `ezbookkeeping-eval/`：Go/Vue 主应用源码。
- `invest-pay-ios-work/`：SwiftUI 客户端源码。
- `Dockerfile`、`compose.yaml`：从源码构建和运行 Docker 镜像。

生产环境请使用新的随机 `EBK_SECRET_KEY`，并通过 HTTPS 或私人网络访问服务。
