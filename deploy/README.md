# Fanmade Docker 后端

本仓库构建 `ourtaiko-fanmade` 应用镜像，仅包含 Go API、FFmpeg/ffprobe、cwebp 和运行依赖。
PostgreSQL 的部署和实例维护属于 OurTaikoSSO；本仓库仍负责 `ourtaiko_fanmade` 的表结构和迁移。

每日业务库自动备份：见 [安装、保留策略与恢复说明](backup/README.md)。默认每天东京时间 04:00，
服务器和私有 S3 各保留 30 天，维护任务独立于 API 容器。

`compose.yml` 只启动一个 `fanmade` 服务，连接已经由 SSO 栈建立的共享 Docker 网络。
程序以非 root 用户运行，根文件系统只读，音频/TJA 放在 `/data/files` 持久卷，WebP 封面放在业务库。

完整的本地原生环境 → Docker → 新 Docker 卷恢复演练，见相邻仓库
[`OurTaikoSSO/deploy/DOCKER.md`](../../../OurTaikoSSO/deploy/DOCKER.md)。
独立克隆时可查看 [SSO 部署文档](https://github.com/OurTaiko/OurTaikoSSO/blob/main/deploy/DOCKER.md)。

```sh
docker build -t ourtaiko-fanmade:local .
# STACK_DIR 中准备 fanmade.env，OURTAIKO_NETWORK 指向已存在的共享网络。
docker compose -f deploy/compose.yml up -d --wait
```

必需的运行配置：`DATABASE_URL`（连接自己的数据库）、`APP_ORIGIN`、SSO 的 issuer/服务凭据/OAuth 凭据/
精确回调 URI，以及稳定的 `SESSION_ENCRYPTION_KEY`。本地 HTTP 使用 `COOKIE_SECURE=false`；生产使用 HTTPS。
`SSO_CONNECT_ADDRESS` 是可选的内部 TCP 路由，保留公开 issuer 和 TLS 校验，默认不需要设置。

备份需要同时包含业务数据库、文件卷和私有配置。应用镜像不会包含这些数据。
不得运行旧 `deploy/compose.postgres.yml` 再建一个数据库实例；该配置已迁入 SSO 仓库，
现有生产实例的兼容文件为 `deploy/compose.postgres.legacy.yml`。生产使用 `compose.production.yml`。
