# OurTaiko Fanmade API

独立的 Go + PostgreSQL 后端仓库。本地示范版已实现账号会话、TJA + OGG / MP3 上传校验、作品查询、试听资源、原文件 / ZIP 下载和本人作品删除、登录用户成绩提交。前端位于相邻的 `../frontend` 仓库，由 pnpm 管理。

## 本地启动

环境：Go 1.26+、PostgreSQL 18、FFmpeg（含 ffprobe）、Python 3（仅演示导入和 API 测试）。开发机已安装这些工具。

```sh
# 若本机集群尚未启动（不要重新 initdb）：
/opt/homebrew/opt/postgresql@18/bin/pg_ctl -D /opt/homebrew/var/postgresql@18 -l /tmp/ourtaiko-postgresql.log start

# 仅首次创建数据库；已创建时跳过：
createdb ourtaiko_fanmade

# 在本仓库目录执行：
go mod download
# 如果还没有 .env，复制模板并填写 SMTP_PASSWORD：
cp -n .env.example .env
go run ./cmd/server
```

API 默认监听 `http://127.0.0.1:8080`，前端默认地址 `http://127.0.0.1:5173`。请统一使用 127.0.0.1；Origin / Cookie 校验会区分 localhost 和 127.0.0.1。应用启动会事务性执行尚未应用的迁移；只执行迁移可用 `go run ./cmd/server -migrate`。

配置项见 [.env.example](.env.example)。应用启动时通过 godotenv 自动加载工作目录中的 `.env`，已经设置的进程环境变量优先；`.env.example` 不会被读取。真实密码仅放在 Git 忽略的 `.env`，模板保持空密码。默认数据库连接字符串是 `postgres://localhost/ourtaiko_fanmade?host=/tmp&sslmode=disable`，使用本地当前用户。

本机现有 PostgreSQL 使用 trust，本次未改认证文件或创建开机启动项。示范版默认仅监听 loopback。正式部署应配置数据库最小权限账号、密码认证、HTTPS 与安全 Cookie。

## 局域网真机测试

在本仓库目录用以下命令监听局域网：

```sh
LISTEN_ADDR=0.0.0.0:8080 go run ./cmd/server
```

手机和 Mac 连接同一个局域网，游戏配置的 `base_url` 填 `http://<Mac 的局域网 IP>:8080`。`0.0.0.0` 是监听地址，不能作为真机的服务器地址；真机上的 `127.0.0.1` 指向真机自己。Mac 可用 `ipconfig getifaddr en0` 查询 Wi-Fi IPv4 地址。

真机浏览器打开 `http://<Mac IP>:8080/readyz`，返回 `{"ok":true}` 表示 API 和数据库可用。游戏使用 `/api/v1/game/login` 和 Bearer token，不发送浏览器 Origin/CSRF；网站仍可通过本机 Vite 代理访问。数据库连接继续使用本机 Unix socket。

当前开发服务在终端运行，可用 Ctrl+C 停止；没有新增开机启动项。

## ESE 演示数据

```sh
# API 启动后运行，从本地 ESE 原地读取 6 组 TJA / OGG：
python3 scripts/demo.py seed
```

默认读取 `~/Documents/GitHub/ESE`，可以设置 `ESE_ROOT`。脚本优先登录已有账号；新账号会提示输入邮箱与收到的验证码，再通过正常注册和上传 API 导入。它实际执行后端校验，不修改源文件；幂等键保证重复运行不会重复导入。演示账号随机密码保存在 `.data/demo-credentials.json`，不会进入 Git。

当前选用 Happy Synthesizer、Destr0yer、Natsumatsuri New Audio Chart、God-ish、Aiai、Silent Night。源文件与上传的资源均不提交 Git；资源存储于忽略的 `.data/files`。数据库只保存元数据、资源键和哈希。

## 测试

```sh
go test ./...
go vet ./...
DATABASE_TEST_URL='postgres://localhost/ourtaiko_fanmade?host=/tmp&sslmode=disable' go test ./internal/database ./internal/httpapi -v
ESE_ROOT=~/Documents/GitHub/ESE go test ./internal/tja -v
# API 运行中，读取真实 ESE 文件；新测试账号交互输入邮箱与验证码，结束时软删除测试作品：
python3 scripts/demo.py test
python3 scripts/score_smoke.py
python3 scripts/metadata_smoke.py
```

共享规则测试位于 `contracts/validation.json`。前端保存同内容副本；可用 `cmp contracts/validation.json ../frontend/contracts/validation.json` 检查一致性。

## 设计与边界

- [项目规划](docs/PROJECT_PLAN.md)
- [数据库结构](docs/DATABASE.md)
- [API 说明](docs/API.md)
- [多语言名称与编辑](docs/LOCALIZATION.md)
- [ESE 兼容性](docs/ESE_COMPATIBILITY.md)
- [验收记录](docs/VERIFICATION.md)
- [服务器部署与更新](docs/DEPLOYMENT.md)

新注册已要求绑定并验证邮箱：先获取 6 位数字验证码，验证成功后创建账号、写入 email/email_verified_at 并登录。验证码 10 分钟有效、单次使用、最多错误 5 次；重发间隔 60 秒，每邮箱每小时最多 5 次、每连接 IP 每小时最多 10 次。现有演示账号保留原登录能力，不会被自动标记为已验证；找回密码、已有账号补绑与换绑另行实现。[邮件配置与接口](docs/EMAIL_VERIFICATION.md)。

本示范版支持一个 TJA + 一个 Vorbis OGG 或 MP3 音频，TJA 中可有 Easy / Normal / Hard / Oni / Edit 多个难度及 P1 / P2。Tower（塔）和 Dan（段位），包括数字 5 / 6，双端整份拒绝；混合普通难度也不能上传。迁移 012 会下架当前版本含不支持难度的已有作品，保留文件和成绩，所有公开展示及游戏曲库接口排除这些作品。`#NEXTSONG` 多音频谱暂不支持；不承诺完整游戏命令语义。试听在负数 DEMOSTART 时从 0 开始，不改写 TJA。

DOUBLE 谱面不记录云端成绩，但仍可上传与下载。后端按块解析 STYLE 和 P1/P2，API 返回 `style` 与 `cloudScoreEligible`；混合文件的单人块保留资格。迁移 003 会读取 `STORAGE_DIR` 的已有 TJA 回填资格，因此迁移时必须提供配套资源目录；失败会事务回滚。已提供 `POST /api/v1/scores`，提交歌曲 ID、难度、良／可／不可、总分、连打数及最大连击 `max_combo`；复用登录会话与 CSRF，可选幂等键防止重试重复保存，详情见 API 文档。当前记录客户端上报值，未重算成绩；排行榜按每位用户的最高分记录展示。每个 TJA 的同一难度最多一个单人谱，重复在上传时返回带行号的 422。数据库测试仅创建并清理临时 schema，不修改应用表。

后续功能：邮箱补绑与找回密码、替换资源版本、管理员界面、评论收藏、自动清理与备份。示范版软删除后停止公开访问，保留数据库和资源供开发检查。上传失败通常自动清理；数据库 COMMIT 结果不确定或进程崩溃时保留文件供核对，暂未实现后台孤儿文件回收。请同时备份数据库和 `.data/files`。

所有变更使用 Conventional Commits。远端仓库：[OurTaiko/Fanmade_Backend](https://github.com/OurTaiko/Fanmade_Backend)，使用 `ourtaiko` 远端管理。

后端已解析 TITLE/SUBTITLE 的 JA、ZH、KO 字段并回填旧谱面；默认字段按英文保存。作者或管理员可用 `PATCH /api/v1/charts/{id}` 修改默认及多语言名称、副标题，支持 null 恢复原值。修改只影响网站元数据和搜索，原始 TJA、文件版本与成绩不变；前端详情页已提供编辑弹窗。[管理员配置说明](docs/ADMIN.md)。
