# OurTaiko Fanmade API

独立的 Go + PostgreSQL 后端仓库。本地示范版已实现账号会话、TJA / OGG 上传校验、作品查询、试听资源、原文件 / ZIP 下载和本人作品删除。前端位于相邻的 `../frontend` 仓库，由 pnpm 管理。

## 本地启动

环境：Go 1.26+、PostgreSQL 18、FFmpeg（含 ffprobe）、Python 3（仅演示导入和 API 测试）。开发机已安装这些工具。

```sh
# 若本机集群尚未启动（不要重新 initdb）：
/opt/homebrew/opt/postgresql@18/bin/pg_ctl -D /opt/homebrew/var/postgresql@18 -l /tmp/ourtaiko-postgresql.log start

# 仅首次创建数据库；已创建时跳过：
createdb ourtaiko_fanmade

# 在本仓库目录执行：
go mod download
go run ./cmd/server
```

API 默认监听 `http://127.0.0.1:8080`，前端默认地址 `http://127.0.0.1:5173`。请统一使用 127.0.0.1；Origin / Cookie 校验会区分 localhost 和 127.0.0.1。应用启动会事务性执行尚未应用的迁移；只执行迁移可用 `go run ./cmd/server -migrate`。

配置项见 `.env.example`，应用读取进程环境变量，不会自动读取 `.env`。默认连接字符串是 `postgres://localhost/ourtaiko_fanmade?host=/tmp&sslmode=disable`，使用本地当前用户。可通过 `DATABASE_URL`、`APP_ORIGIN`、`LISTEN_ADDR`、`STORAGE_DIR`、`COOKIE_SECURE` 覆盖。

本机现有 PostgreSQL 使用 trust，本次未改认证文件或创建开机启动项。示范版默认仅监听 loopback。正式部署应配置数据库最小权限账号、密码认证、HTTPS 与安全 Cookie。

## ESE 演示数据

```sh
# API 启动后运行，从本地 ESE 原地读取 6 组 TJA / OGG：
python3 scripts/demo.py seed
```

默认读取 `~/Documents/GitHub/ESE`，可以设置 `ESE_ROOT`。脚本通过正常注册和上传 API 导入，实际执行后端校验，不修改源文件；幂等键保证重复运行不会重复导入。演示账号随机密码保存在 `.data/demo-credentials.json`，不会进入 Git。也可以在前端创建自己的演示账号。

当前选用 Happy Synthesizer、Destr0yer、Natsumatsuri New Audio Chart、God-ish、Aiai、Silent Night。源文件与上传的资源均不提交 Git；资源存储于忽略的 `.data/files`。数据库只保存元数据、资源键和哈希。

## 测试

```sh
go test ./...
go vet ./...
ESE_ROOT=~/Documents/GitHub/ESE go test ./internal/tja -v
# API 运行中，读取真实 ESE 文件，创建测试账号并在结束时软删除测试作品：
python3 scripts/demo.py test
```

共享规则测试位于 `contracts/validation.json`。前端保存同内容副本；可用 `cmp contracts/validation.json ../frontend/contracts/validation.json` 检查一致性。

## 设计与边界

- [项目规划](docs/PROJECT_PLAN.md)
- [数据库结构](docs/DATABASE.md)
- [API 说明](docs/API.md)
- [ESE 兼容性](docs/ESE_COMPATIBILITY.md)
- [验收记录](docs/VERIFICATION.md)

正式注册要求绑定并验证邮箱，当前明确暂缓：只实现用户名 / 密码注册，`email` 与 `email_verified_at` 均保持空值，无发送邮件、验证链接、找回密码或已验证账号权限限制。当前不面向公网开放。

本示范版支持一个 TJA + 一个 Vorbis OGG，TJA 中可有多个难度、P1 / P2 和 Tower。`#NEXTSONG` 多音频谱暂不支持；不承诺完整游戏命令语义。试听在负数 DEMOSTART 时从 0 开始，不改写 TJA。

后续功能：邮箱验证、替换资源版本、管理员界面、评论收藏、游戏接入、自动清理与备份。示范版软删除后停止公开访问，保留数据库和资源供开发检查。上传失败通常自动清理；数据库 COMMIT 结果不确定或进程崩溃时保留文件供核对，暂未实现后台孤儿文件回收。请同时备份数据库和 `.data/files`。

所有变更使用 Conventional Commits。当前仓库只在本地初始化，未配置远程或推送。
