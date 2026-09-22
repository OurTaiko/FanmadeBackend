# OurTaikoSSO 接入

## 当前行为

- 网站：`/login` → `/api/v1/auth/sso/login` → SSO 登录/授权 → 本站 callback → 原页面。采用标准 OIDC Authorization Code + S256 PKCE，校验签名、issuer、audience、nonce、state、浏览器绑定与单次回调。
- 注册、昵称、密码、安全设备管理统一在账号中心。注册在新标签页完成后回到 Fanmade 登录；账号修改后返回本站会刷新个人资料。
- YataiDON 无需改动。`POST /game/login` 仍接收 username/password，后端转接 SSO 并返回原 user/accessToken/expiresIn。其他游戏请求实时向 SSO 验证游戏令牌。
- Fanmade 数据库的 `users` 仅保留稳定 ID。谱面、上传收据、成绩继续存储于 Fanmade。网站会话保存 Cookie 哈希、CSRF 和 AES-256-GCM 加密的上游 OAuth access token；不保存账号密码或昵称。
- 网站 Cookie 最长一小时，无自动 refresh；到期重新走 SSO。游戏令牌七天。密码修改、SSO 撤销授权或禁用账号后，后续受保护请求失效。Fanmade“退出登录”仅退出本站，不退出账号中心。
- 身份服务不可用时鉴权返回 503；公开谱面/下载/排行榜仍可访问，昵称暂显示“未知用户”。带搜索条件的曲库检索需要 SSO，以保持上传者昵称搜索准确。

## 本机开发环境

目前三个入口都绑定 loopback：Fanmade 网站 `http://127.0.0.1:5173`、API `http://127.0.0.1:8080`、SSO `http://127.0.0.1:8090`。请统一使用 `127.0.0.1`，不要和 localhost 混用。

SSO 位于 `../../OurTaikoSSO`。开发用户凭据为该项目私有 `.data/dev-access.txt`；服务及 OAuth 客户端凭据为 `.data/dev-clients.json`，已写入后端私有 `.env`。前端不持有服务密钥。

本次使用独立数据库 `ourtaiko_fanmade_sso_dev` 和 `.data/sso-files` 文件副本。原 `ourtaiko_fanmade` 数据库、原资源目录保留；原环境文件备份到 `.data/pre-sso.env`。旧测试用户按原 ID 导入 SSO，原密码可继续使用。

依次在三个项目启动（PostgreSQL 需运行）：

```sh
# OurTaikoSSO
.venv/bin/python manage.py migrate
.venv/bin/python manage.py seed_dev
.venv/bin/python manage.py runserver 127.0.0.1:8090 --noreload

# Fanmade/backend
GOCACHE=/tmp/ourtaiko-sso-go-build go run ./cmd/server

# Fanmade/frontend
pnpm dev
```

## 其他环境迁移

1. 停止旧服务写入；备份业务数据库和文件目录。回退应恢复旧库及旧版本，不能把旧二进制直接连接 018 后的数据库。
2. 在 SSO 运行 `import_fanmade` 的 dry run，再 `--apply`，保留所有用户 ID、密码哈希和 Fanmade 角色。已存在 ID 不会被覆盖，因此应核对导入报告；不要在业务数据库删除账号字段之后才导入。
3. 注册 confidential OAuth 客户端，grant=authorization_code，algorithm=RS256，redirect URI 必须精确匹配 `${APP_ORIGIN}/api/v1/auth/sso/callback`；将 Fanmade GameClient.web_client_id 映射为该 client_id。
4. 配置 `.env.example` 中 SSO 参数。服务密钥和会话加密密钥是 64 位 hex；`openssl rand -hex 32` 可生成后者。密钥要持久保管并在多实例间一致。HTTPS 部署必须 COOKIE_SECURE=true；HTTP 只允许 loopback 开发地址。
5. 备份核对完成后设置 `SSO_MIGRATION_BACKUP_CONFIRMED=true`，启动或 `go run ./cmd/server -migrate`。018 事务检查全部旧 ID 在 SSO 中可查；缺失即拒绝迁移并保留原账号字段。新空库无需该确认。原谱面与成绩不会重新编号，所有旧登录会话失效。

本站废弃 `POST /auth/login`、`POST /auth/register`、`POST /auth/email-code`、`PATCH /me`，统一返回 410 SSO_REQUIRED。游戏 `/game/login` 不受影响。

## 验证

```sh
# backend；测试只创建/删除临时 schema
DATABASE_TEST_URL='postgres://localhost/ourtaiko_fanmade_sso_dev?host=/tmp&sslmode=disable' go test ./...

# frontend；创建本地临时测试账号，通过文件邮件实际验证并走 OIDC
pnpm test
pnpm lint
pnpm build
FANMADE_SSO_E2E=1 pnpm test:e2e

# OurTaikoSSO；隔离 SQLite 测试，以及实际 HTTP/PG 联调
.venv/bin/python manage.py test --settings=config.test_settings
.venv/bin/python manage.py smoke_fanmade
```

`smoke_fanmade` 仅允许本地开发副本，验证网站登录、回调重放拒绝、上传、游戏登录及成绩、昵称同步、改密撤销和退出，完成后清理其临时账号与业务记录。Playwright 的注册测试受真实注册限流约束，每 IP 每小时最多 8 次注册；不要对部署环境运行。

旧 `scripts/demo.py`、`score_smoke.py`、`metadata_smoke.py` 使用浏览器 SSO 后的测试 Cookie：FANMADE_SESSION_COOKIE；需要另一个用户时设置 FANMADE_OTHER_SESSION_COOKIE。请使用专用测试账号；脚本会创建测试作品，部分保留成绩并注销会话。不再提供脚本内密码登录或创建本地账号。

## 语言偏好

SSO 身份响应新增 `user.preferredLanguage`（`zh-hans`、`en`、`ja`、`ko`）。业务 API 通过既有实时 introspect 链路透传到 `/api/v1/me` 和游戏身份响应，不在业务数据库复制语言偏好。前端在 focus / visibility 刷新会话后应用语言；缺失或不支持的值回退简体中文。语言在 SSO 账号中心修改，改动不撤销会话。SSO 的新用户字段迁移应先于前端发布。
