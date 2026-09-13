# 示范版 API

基础路径 `/api/v1`。浏览器通过 Vite 的 `/api` 代理访问 Go 服务，开发环境不依赖跨域 Cookie。JSON 响应中的作品结构与前端 `src/api.ts` 对应；本次未启用 OpenAPI 类型生成。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| POST | /auth/register | JSON username/password；成功 200 并建立会话；邮箱验证暂缓 |
| POST | /auth/login | JSON username/password；返回 user / csrfToken 并设置 HttpOnly Cookie |
| POST | /auth/logout | 撤销 Session，清 Cookie |
| GET | /me | `{user, csrfToken}`；未登录返回 200，user 为 null |
| POST | /scores | JSON 提交单人谱成绩；首次保存 201，幂等重试 200 |
| GET | /upload-rules | 校验版本、大小上限、编码与音频支持信息 |
| GET | /charts?q=&course=&page=1 | 返回 `{items,total,page,pageSize}`；每页 12 |
| GET | /me/charts | 本人作品列表，查询参数与公开列表一致 |
| POST | /charts | multipart：tja、audio、encoding（默认 utf-8）、description（可空）；首次创建 201，同请求重试 200 |
| GET | /charts/{id} | 当前已发布版本详情 |
| PATCH | /charts/{id} | 作者或管理员修改默认英文及 ja/zh/ko 名称／副标题，返回更新后的作品 |
| DELETE | /charts/{id} | 本人软删除；后续资源访问返回 404 |
| GET | /charts/{id}/versions/{version}/tja | 原始 TJA 字节，attachment |
| GET | /charts/{id}/versions/{version}/audio | OGG，支持 Range / ETag |
| GET | /charts/{id}/versions/{version}/download | ZIP，含原始 TJA 与按 WAVE 原值命名的音频 |

`GET /healthz` 为进程状态，`GET /readyz` 额外检查数据库连接。

所有写请求要求 `Origin` 等于配置 APP_ORIGIN。已登录写请求还要求 `X-CSRF-Token` 等于当前 Session 的令牌。上传要求 `Idempotency-Key`：16–80 位字母、数字或短横线，推荐随机 UUID；同键不同载荷返回 409。

普通错误：`{code,message,requestId,validationVersion}`；TJA 错误额外返回 `errors` 数组，含 code/message/line/expected/actual。常用状态码：400 请求格式，401 未登录，403 权限或来源错误，409 冲突，413 大小限制，422 谱面／音频校验失败，429 频率限制，503 暂时不可用。

数量：1 个 TJA + 1 个 OGG。TJA 最大 2 MiB，OGG 最大 100 MiB，请求最大 105 MiB，说明最大 4000 字节。文本 UTF-8 或显式 Shift-JIS。音频要求单逻辑流 Ogg Vorbis、完整 CRC 与 EOS、可完整解码，时长不超过 20 分钟。最多两个上传同时处理。

邮箱验证、修改说明、替换版本、管理员接口仍为规划项，本次未提供对应端点。名称／副标题编辑已提供，详见文末。

## 谱面块的云端成绩资格

上传成功、作品详情、公开列表与本人列表中的 `difficulties[]` 增加 `style` 和 `cloudScoreEligible`，已有 `course`、`level`、`blockIndex`、`player` 字段保留。例如：

```json
[
  {"course":"Oni","level":7,"blockIndex":0,"player":"","style":"Single","cloudScoreEligible":true},
  {"course":"Oni","level":7,"blockIndex":1,"player":"P1","style":"Double","cloudScoreEligible":false},
  {"course":"Oni","level":7,"blockIndex":2,"player":"P2","style":"Double","cloudScoreEligible":false}
]
```

后端解析 STYLE 值时不区分大小写，支持 Single/0、Double/1 和 ESE 中的 Duet（按 Double）。STYLE 在当前 COURSE 内持续生效直到下一次 STYLE 声明，每次 COURSE 声明恢复默认 Single；P1/P2 块无论 STYLE 如何均按 Double。未知 STYLE 或在谱面块内部声明 STYLE 返回 422 `TJA_STRUCTURE_INVALID`，包含行号。后端校验版本为 `tja-upload-v4`；前端当前仍执行 v1 基础预检，新增规则由后端权威校验。

每个 TJA 的同一难度最多一个 Single 块。重复时在上传阶段返回 422 `TJA_DIFFICULTY_DUPLICATE`，`errors[0].line` 指向重复块的 #START，message 指出首次声明行号。难度名与数字别名归一化后比较，修改 LEVEL 不会使重复难度合法；P1/P2 和显式 Double 块不参与这个单人重复检查。

Double 允许正常上传、试听和下载原始文件，`cloudScoreEligible:false` 不是上传错误。同一 TJA 的每个块独立判定，不能因含 Double 而禁用整个作品的 Single 块。资格从数据库返回，上传请求不接受客户端指定 style 或资格。

成绩提交接口已实现，目标必须是服务端查到的 Single 块。排行榜和游戏客户端专用鉴权尚未实现。

## 提交成绩

`POST /api/v1/scores`，`Content-Type: application/json`。先用现有 `/auth/login` 登录，保留 `ourtaiko_session` Cookie；请求携带配置的 `Origin`（本地为 `http://127.0.0.1:5173`）和登录返回的 `X-CSRF-Token`。用户 ID 取自会话，不接受客户端指定。

```json
{
  "songId": "11111111111111111111111111111111",
  "difficulty": "Oni",
  "good": 470,
  "ok": 10,
  "bad": 8,
  "score": 900000,
  "drumroll": 50
}
```

示例 songId 需替换为作品 API 返回的 `id`，不是文件哈希或 versionId。七个字段全部必填：good 为良、ok 为可、bad 为不可、score 为总分、drumroll 为连打数。五个数字不接受 null、字符串或小数；计数为 0–2147483647，总分为 0–9007199254740991（JSON/JavaScript 安全整数范围）。这些是存储边界，不是玩法理论上限。难度接受 Easy、Normal、Hard、Oni、Edit、Tower、Dan，不区分大小写、忽略首尾空白；Ura 归一为 Edit。

后端锁定当前已发布版本，按难度寻找唯一有资格的单人块。同一难度含一个 Single 和若干 Double 时只选 Single；只有 Double 时拒绝。重复 Single 已在新上传时拦截，成绩接口仍对历史异常数据返回歧义错误。当前只传歌曲 ID 和难度，因此按提交时的当前版本归属；还不能证明客户端实际游玩了哪个本地文件，后续替换版本／游戏清单接入时需要扩展版本凭据。

成功返回 201，响应包含原七个字段（difficulty 已归一化），以及服务端生成的 `id`、`userId`、`versionId`、`blockIndex` 和 `submittedAt`（UTC RFC3339）。每次新提交保留一条游玩记录，不覆盖最高分，也不自动计算排行榜。歌曲更名或软删除不删除已保存成绩；下架后拒绝新成绩。

可选请求头 `Idempotency-Key` 为 16–80 位字母、数字或短横线，推荐每局生成 UUID 并在网络重试时复用。相同用户、相同 key、相同归一化载荷返回原成绩及 200；同 key 不同载荷返回 409。不同用户的 key 互不影响；省略 key 时每次请求都是新游玩。已成功提交的幂等重试即使歌曲后来下架也返回原回执，不重新选择版本或写入成绩。

| 状态码 | code | 原因 |
| --- | --- | --- |
| 400 | REQUEST_INVALID | JSON 格式错误、未知字段、非整数、多个 JSON 值 |
| 400 | IDEMPOTENCY_KEY_INVALID | 请求标识格式错误 |
| 401 | UNAUTHORIZED | 未登录／会话过期 |
| 403 | CSRF_INVALID / ORIGIN_INVALID | 缺少正确的会话令牌／来源 |
| 404 | CHART_NOT_FOUND | 歌曲不存在、隐藏或删除 |
| 404 | DIFFICULTY_NOT_FOUND | 当前版本无此难度 |
| 409 | DIFFICULTY_AMBIGUOUS | 历史数据存在多个同难度单人块 |
| 409 | IDEMPOTENCY_CONFLICT | 同请求标识用于不同成绩 |
| 415 | CONTENT_TYPE_INVALID | 非 JSON 媒体类型 |
| 422 | SCORE_INVALID | 字段缺失、负数、超限、无效歌曲 ID／难度 |
| 422 | DOUBLE_SCORE_UNSUPPORTED | 目标难度只有 DOUBLE 谱面 |

当前保存的是登录用户上报值：未校验判定数量与谱面音符数、未重算总分、未验证回放、自动演奏或计分模式，不作为已验证竞技成绩。已有基础限流与上传接口共用（来源 IP 每分钟 40 次写请求）。

## 多语言名称与编辑

作品响应新增 `titleTranslations`、`subtitleTranslations` 对象，键为 ja/zh/ko，默认英文仍为 `title`、`subtitle`。缺失语言不返回对应键，显式空值保留为字符串 `""`。GET 列表的 q 同时搜索默认和多语言名称、副标题。

`PATCH /api/v1/charts/{id}` 只接受四个字段：title、subtitle、titleTranslations、subtitleTranslations。登录上传者可以部分修改，缺省字段保持不变；null 恢复原文件值，副标题空字符串表示清空。JSON 请求需要现有 Cookie、Origin 与 X-CSRF-Token；成功 200 返回更新后的完整作品。返回 401 未登录、403 无所有权／CSRF／来源不正确、404 不存在或已下架、400 请求格式或未知字段错误、415 非 JSON、422 `METADATA_INVALID` 内容／语言／长度不合法。完整请求示例、逐语言恢复规则和存储边界见 [多语言与管理说明](LOCALIZATION.md)。

编辑仅改变网站展示与搜索，下载仍是原始 TJA，已保存成绩和 versionId 不变。前端详情页已提供作者／管理员可见的编辑弹窗。

用户对象新增 `isAdmin` 布尔值，来自数据库。PATCH 编辑权限为作者或管理员，其余登录用户返回 403；匿名返回 401。管理员同样要求 Origin 与 CSRF。角色不能在注册或编辑请求中指定，管理方式见 [管理员说明](ADMIN.md)。
