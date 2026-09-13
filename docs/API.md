# 示范版 API

基础路径 `/api/v1`。浏览器通过 Vite 的 `/api` 代理访问 Go 服务，开发环境不依赖跨域 Cookie。JSON 响应中的作品结构与前端 `src/api.ts` 对应；本次未启用 OpenAPI 类型生成。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| POST | /auth/register | JSON username/password；成功 200 并建立会话；邮箱验证暂缓 |
| POST | /auth/login | JSON username/password；返回 user / csrfToken 并设置 HttpOnly Cookie |
| POST | /auth/logout | 撤销 Session，清 Cookie |
| GET | /me | `{user, csrfToken}`；未登录返回 200，user 为 null |
| GET | /upload-rules | 校验版本、大小上限、编码与音频支持信息 |
| GET | /charts?q=&course=&page=1 | 返回 `{items,total,page,pageSize}`；每页 12 |
| GET | /me/charts | 本人作品列表，查询参数与公开列表一致 |
| POST | /charts | multipart：tja、audio、encoding（默认 utf-8）、description（可空）；首次创建 201，同请求重试 200 |
| GET | /charts/{id} | 当前已发布版本详情 |
| DELETE | /charts/{id} | 本人软删除；后续资源访问返回 404 |
| GET | /charts/{id}/versions/{version}/tja | 原始 TJA 字节，attachment |
| GET | /charts/{id}/versions/{version}/audio | OGG，支持 Range / ETag |
| GET | /charts/{id}/versions/{version}/download | ZIP，含原始 TJA 与按 WAVE 原值命名的音频 |

`GET /healthz` 为进程状态，`GET /readyz` 额外检查数据库连接。

所有写请求要求 `Origin` 等于配置 APP_ORIGIN。已登录写请求还要求 `X-CSRF-Token` 等于当前 Session 的令牌。上传要求 `Idempotency-Key`：16–80 位字母、数字或短横线，推荐随机 UUID；同键不同载荷返回 409。

普通错误：`{code,message,requestId,validationVersion}`；TJA 错误额外返回 `errors` 数组，含 code/message/line/expected/actual。常用状态码：400 请求格式，401 未登录，403 权限或来源错误，409 冲突，413 大小限制，422 谱面／音频校验失败，429 频率限制，503 暂时不可用。

数量：1 个 TJA + 1 个 OGG。TJA 最大 2 MiB，OGG 最大 100 MiB，请求最大 105 MiB，说明最大 4000 字节。文本 UTF-8 或显式 Shift-JIS。音频要求单逻辑流 Ogg Vorbis、完整 CRC 与 EOS、可完整解码，时长不超过 20 分钟。最多两个上传同时处理。

邮箱验证、修改说明、替换版本、管理员接口仍为规划项，本次未提供对应端点。
