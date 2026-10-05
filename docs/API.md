# 示范版 API

基础路径 `/api/v1`。浏览器通过 Vite 的 `/api` 代理访问 Go 服务，开发环境不依赖跨域 Cookie。当前歌曲结构见本文；既有网页前端 `src/api.ts` 需要同步适配 schema 024 的歌曲 ID 协议。本次未启用 OpenAPI 类型生成。

| 方法 | 路径 | 行为 |
| --- | --- | --- |
| GET | /auth/sso/login?returnTo=/upload | 开始 OIDC Authorization Code + PKCE 登录 |
| GET | /auth/sso/callback | 验证 state、浏览器绑定、nonce、签名、issuer、audience 后建立本站会话 |
| GET | /auth/account/register | 跳转 SSO 注册页 |
| GET | /auth/account/profile | 跳转 SSO 账号中心 |
| POST | /auth/email-code、/auth/register、/auth/login | 已迁移，返回 410 SSO_REQUIRED |
| POST | /auth/logout | 撤销 Session，清 Cookie |
| GET | /me | `{user, csrfToken}`；user 含自己的 username 和 nickname；未登录返回 200，user 为 null |
| PATCH | /me | 已迁移，返回 410 SSO_REQUIRED；在 SSO 管理昵称 |
| POST | /scores | JSON 提交单人谱成绩；首次保存 201，幂等重试 200 |
| GET | /upload-rules | 校验版本、大小上限、编码与音频支持信息 |
| GET | /charts?q=&course=&level=&order=default&page=1 | 返回 `{items,total,page,pageSize}`；每页 12 |
| GET | /me/charts | 本人作品列表，查询参数与公开列表一致 |
| POST | /charts | multipart：tja、audio、encoding（默认 utf-8）、description（可空）、categoryIds、difficultyMakers（可选 JSON）；首次创建 201，同请求重试 200 |
| PUT | /charts/{id}/files | 作者或管理员整体替换 TJA／音频，删除旧文件及成绩，只保存当前资源，返回 200 |
| GET | /charts/{id} | 当前已发布歌曲详情 |
| PATCH | /charts/{id} | 作者或管理员修改 en/ja/zh/ko 名称／副标题，返回更新后的作品 |
| DELETE | /charts/{id} | 本人软删除；后续资源访问返回 404 |
| GET | /charts/{id}/tja | 原始 TJA 字节，attachment |
| GET / HEAD | /charts/{id}/audio | 原始 OGG 或 MP3 流，支持 Range、ETag、If-Range；Content-Type 分别为 audio/ogg、audio/mpeg |
| GET | /charts/{id}/download | ZIP，含原始 TJA 与按 WAVE 原值命名的音频 |

`GET /healthz` 为进程状态，`GET /readyz` 额外检查数据库连接。

在线预览：`GET /game/bootstrap` 新增 `audioPreviewVersion: 1`；歌曲 `Chart` 新增可选 `audioPreview: {url, contentType, startSeconds, durationSeconds}`。URL 指向原始音频，起点为规范化后的 DEMOSTART，建议时长最多 15 秒且不超过剩余音频时长；无有效整曲时长时省略。响应仍为完整原文件或请求的字节范围，不是服务端裁剪的试听片段。

音频成功响应设置 `Cache-Control: public, no-cache, no-transform`，缓存复用前需重验证。旧版本、下架或删除歌曲仍返回 404。完整 HTTP 契约、MajdataPlay／mmfcapi 分析和游戏端修改说明见 [在线歌曲流播放与游戏端预览改进](ONLINE_AUDIO_PREVIEW.md)。

所有写请求要求 `Origin` 等于配置 APP_ORIGIN。已登录写请求还要求 `X-CSRF-Token` 等于当前 Session 的令牌。上传要求 `Idempotency-Key`：16–80 位字母、数字或短横线，推荐随机 UUID；同键不同载荷返回 409。

普通错误：`{code,message,requestId,validationVersion}`；TJA 错误额外返回 `errors` 数组，含 code/message/line/expected/actual。常用状态码：400 请求格式，401 未登录，403 权限或来源错误，409 冲突，413 大小限制，422 谱面／音频校验失败，429 频率限制，503 暂时不可用。

数量：1 个 TJA + 1 个 OGG 或 MP3 音频。TJA 最大 2 MiB，音频最大 100 MiB，请求最大 105 MiB，说明最大 4000 字节。文本输入接受 UTF-8（默认）或显式 Shift-JIS；后端独立严格解码并统一保存为无 BOM 的 UTF-8。原输入和转换后均限制 2 MiB。网站自动识别常见编码，在上传前转换为 UTF-8。TJA 的哈希、字节数及下载内容均对应实际保存的 UTF-8 字节；已有作品不自动改写。音频接受单音轨 Ogg Vorbis 或 MPEG Layer III（MP3），扩展名必须与真实格式一致。OGG 检查 CRC 与 EOS；MP3 检查帧边界及截断，支持 CBR/VBR、MPEG-1/2/2.5、ID3 和 APE 标签，允许内嵌封面，不接受 free-format MP3。两种格式均需可完整解码，时长不超过 20 分钟。原音频不转码，WAVE 与上传文件名仍须 NFC 后严格匹配（区分大小写）。最多两个上传同时处理。

邮箱验证码、名称／副标题编辑和歌曲文件替换已提供。独立修改投稿说明、管理员管理界面仍未提供；替换歌曲时可同时提交新的说明。

## 支持的难度

仅接受 Easy / Normal / Hard / Oni / Edit；TJA 的 COURSE 值支持数字 0–4 和大小写变体。Tower / Dan / 5 / 6 返回 422 `TJA_COURSE_UNSUPPORTED`，`errors[].line` 指向 COURSE 行。只要出现不支持的 COURSE，整份 TJA 拒绝，不能只上传其中的普通难度块。

列表 `course` 参数允许五种规范英文名及其 _1p/_2p 形式，其他非空值返回 400 `DIFFICULTY_INVALID`。成绩接口拒绝不支持的难度（422），排行榜查询同样拒绝（400）。已有不支持的当前版本由迁移 012 下架，列表和游戏曲库不返回，详情、编辑和所有文件下载返回 404；游戏快照排除不支持版本的全部成绩。数据仍保存在数据库和资源目录中。

## 歌曲模式

作品的 isSingle 表示整份文件的模式；difficulties 每项只有 course、level、maker。单人使用五种基础难度名，双人使用带 _1p/_2p 后缀的名称，详见文末协议说明。所有有效难度都支持成绩。

STYLE 不区分值大小写，支持 Single/0、Double/1、Duet；每次 COURSE 声明恢复默认 Single。P1/P2 标记按双人解析，显式 Double 必须标明 P1/P2。同一文件混合模式、重复完整 course、块内 STYLE 和未知 STYLE 均被拒绝。校验标识为 tja-upload-v8。

## 提交成绩

`POST /api/v1/scores`，`Content-Type: application/json`。先通过 `/auth/sso/login` 完成浏览器登录，再调用 `/me` 取得 CSRF，保留 `ourtaiko_session` Cookie；请求携带配置的 `Origin`（本地为 `http://127.0.0.1:5173`）和登录返回的 `X-CSRF-Token`。用户 ID 取自会话，不接受客户端指定。

```json
{
  "songId": "11111111111111111111111111111111",
  "difficulty": "Oni",
  "good": 470,
  "ok": 10,
  "bad": 8,
  "score": 900000,
  "drumroll": 50,
  "max_combo": 350,
  "ClearStatus": 1
}
```

示例 songId 需替换为作品 API 返回的 `id`，不是文件哈希。八个基础字段全部必填，`max_combo` 为最大连击：good 为良、ok 为可、bad 为不可、score 为总分、drumroll 为连打数。六个数字不接受 null、字符串或小数；计数为 0–2147483647，总分为 0–9007199254740991（JSON/JavaScript 安全整数范围）。`max_combo` 为必填的 0–2147483647 整数；省略或 null 返回 422，非整数返回 400，负数或越界值返回 422。这些是存储边界，不是玩法理论上限。难度仅接受 Easy、Normal、Hard、Oni、Edit，不区分大小写、忽略首尾空白；Ura 归一为 Edit。

`ClearStatus` 为可选整数，取值 `0`（无皇冠／状态未知）、`1`（普通通关）、`2`（全连）、`3`（全良）。字段名大小写按此示例；省略或 `null` 按 `0` 保存，兼容旧客户端。负数或大于 3 返回 `422 SCORE_INVALID`；字符串、小数、布尔值返回 `400 REQUEST_INVALID`。服务端保存上报状态，不根据分数、不可数量或回放推断通关。

后端锁定当前已发布歌曲，按难度寻找唯一有资格的单人块。同一难度含一个 Single 和若干 Double 时只选 Single；只有 Double 时拒绝。重复 Single 已在新上传时拦截，成绩接口仍对历史异常数据返回歧义错误。浏览器和原生游戏接口都只按 songId 归属，不接收歌曲版本字段。服务端保存客户端上报值，未根据游玩过程重算成绩。

成功返回 201，响应包含上述八个基础成绩字段（difficulty 已归一化）和 `ClearStatus`（即使为 0 也返回），以及服务端生成的 `id`、`userId` 和 `submittedAt`（UTC RFC3339）。每次新提交保留一条游玩记录，不覆盖最高分；排行榜读取时选出每人的最高分记录。歌曲更名或软删除不删除已保存成绩；下架后拒绝新成绩。

可选请求头 `Idempotency-Key` 为 16–80 位字母、数字或短横线，推荐每局生成 UUID 并在网络重试时复用。相同用户、相同 key、相同归一化载荷返回原成绩及 200；同 key 不同载荷返回 409。不同用户的 key 互不影响；省略 key 时每次请求都是新游玩。已成功提交的幂等重试即使歌曲后来下架也返回原回执，不重新选择版本或写入成绩。

| 状态码 | code | 原因 |
| --- | --- | --- |
| 400 | REQUEST_INVALID | JSON 格式错误、未知字段、非整数、多个 JSON 值 |
| 400 | IDEMPOTENCY_KEY_INVALID | 请求标识格式错误 |
| 401 | UNAUTHORIZED | 未登录／会话过期 |
| 403 | CSRF_INVALID / ORIGIN_INVALID | 缺少正确的会话令牌／来源 |
| 404 | CHART_NOT_FOUND | 歌曲不存在、隐藏或删除 |
| 404 | DIFFICULTY_NOT_FOUND | 当前版本无此难度 |
| 409 | IDEMPOTENCY_CONFLICT | 同请求标识用于不同成绩 |
| 415 | CONTENT_TYPE_INVALID | 非 JSON 媒体类型 |
| 422 | SCORE_INVALID | 字段缺失、负数、超限、无效歌曲 ID／难度 |

当前保存的是登录用户上报值：未校验判定数量与谱面音符数、未重算总分、未验证回放、自动演奏或计分模式，不作为已验证竞技成绩。已有基础限流与上传接口共用（来源 IP 每分钟 40 次写请求）。

## 多语言名称与编辑

作品响应始终完整返回 `titleTranslations`、`subtitleTranslations` 字典（支持 en/ja/zh/ko），英文也在 en 键内。`title`、`subtitle` 保留 TJA 原文，不代表当前显示语言。后端不按 Accept-Language 或请求语言筛选；由前端和游戏客户端选择显示语言及回退。缺失翻译不伪造对应键，显式空值保留为字符串 `""`。GET 列表的 q 搜索原文和所有翻译。

`PATCH /api/v1/charts/{id}` 接受 titleTranslations、subtitleTranslations 和 categoryIds；旧 title/subtitle 写入仅作为 en 翻译的兼容别名，与相应字典 en 同时提交会被拒绝。登录上传者可以部分修改，缺省字段保持不变；null 恢复原文件值，副标题空字符串表示清空。JSON 请求需要现有 Cookie、Origin 与 X-CSRF-Token；成功 200 返回更新后的完整作品。返回 401 未登录、403 无所有权／CSRF／来源不正确、404 不存在或已下架、400 请求格式或未知字段错误、415 非 JSON、422 `METADATA_INVALID` 内容／语言／长度不合法。完整请求示例、逐语言恢复规则和存储边界见 [多语言与管理说明](LOCALIZATION.md)。

编辑仅改变网站展示与搜索，下载仍是原始 TJA，已保存成绩和歌曲 ID 不变。前端详情页已提供作者／管理员可见的编辑弹窗。

用户对象新增 `isAdmin` 布尔值，来自数据库。PATCH 编辑权限为作者或管理员，其余登录用户返回 403；匿名返回 401。管理员同样要求 Origin 与 CSRF。角色不能在注册或编辑请求中指定，管理方式见 [管理员说明](ADMIN.md)。

## 原生游戏接入（Fanmade v1）

原生客户端使用独立 Bearer 会话，不发送浏览器的 Origin、Cookie 或 CSRF token。浏览器接口保留原有 Origin/CSRF 校验；两类 token 不能互换。所有非 GET 请求仍有每 IP 每分钟 40 次限制。公网部署使用 HTTPS。

- `POST /api/v1/game/login`：JSON `{ "username": "...", "password": "..." }`，返回 `user`、`accessToken`、`expiresIn`（604800 秒）。无 Set-Cookie。后续原生请求使用 `Authorization: Bearer <accessToken>`。
- `GET /api/v1/game/bootstrap`：返回 `{ "user": {...}, "categories": [{ "id": "game", "title": "Game", "genre": "GAME", "chartCount": 1 }, ...], "chartCount": 1, "scores": [...] }`，在同一个 PostgreSQL repeatable-read 快照中读取分类、各分类数量、服务器去重数量和当前用户的历史成绩，不返回全部谱面内容。`GET /api/v1/game/categories/{categoryId}/charts` 按需返回分类内谱面；详情见 [分类协议](CATEGORIES.md)。`songIdOnly: true` 声明本文的歌曲 ID 协议；最佳成绩按服务器/账号/songId/difficulty 筛选。重新获取 bootstrap 时替换该服务器账号的在线成绩快照，不能只追加，因为替换资源会清空旧成绩。
- 当前原始文件使用 `GET /api/v1/charts/{id}/{tja|audio|download}`。S3 直连使用 `GET /api/v1/charts/{id}/resources`；清单返回各资源的 SHA-256、大小和临时 GET/HEAD URL。加载前核对详情和清单哈希，完整下载后核对实际 SHA-256。
- `POST /api/v1/game/scores`：八项基础成绩字段（含必填 `max_combo`），建议每次游玩使用固定的 `Idempotency-Key`。示例：`{"songId":"<32 hex>","difficulty":"Oni","good":300,"ok":10,"bad":2,"score":900000,"drumroll":50,"max_combo":250,"ClearStatus":1}`。双人成绩使用 difficulty=Oni_1p/Oni_2p 等带后缀的规范值，不提交额外 player 或 blockIndex。临时失败重试保持载荷和 key 不变。已接收的成绩若被资源替换删除，再用原 key 重试返回 `409 SCORE_REMOVED`，客户端停止重试。

`POST /api/v1/scores` 与原生接口使用相同成绩字段。两者均不接收 `versionId`（未知 JSON 字段返回 400），也不在回执、bootstrap、排行榜中返回它。`ClearStatus` 可选 0–3，省略为 0；回执和成绩列表总是返回 `max_combo`、`ClearStatus`。SSO 会话行为不变。客户端在待上传记录中保存游玩时的 TJA/audio 哈希，上传前核对当前哈希；不匹配或旧队列缺少可信哈希时不自动上传。服务端没有歌曲版本或游玩快照标识，无法识别从未接收过的离线旧成绩；哈希检查与提交之间也有竞态，不承诺严格排除旧内容成绩。完整实现要求见 [游戏接入文档](GAME_CLIENT_RESOURCE_DOWNLOAD.md)。

游客无需登录即可使用 `game/bootstrap`、`game/categories/{categoryId}/charts`、谱面详情和文件下载。bootstrap 无 Authorization 时返回相同的公开分类与数量，但 `user: null`、`scores: []`，不查询个人成绩，浏览器 Cookie 不会改变游客身份。携带 Bearer token 时仍严格校验原生会话；无效或过期 token 返回 401，客户端可重新登录。`POST /api/v1/game/scores` 仍必须携带有效原生 Bearer token，游客返回 401。游戏端需同步升级，才能移除旧版的账号必填限制；无需新增数据库迁移。
# 谱面排行榜

`GET /api/v1/charts/{id}/leaderboard?difficulty=Oni&page=1` 公开读取，无需登录。

- 未传难度时按 Oni → Edit → Hard → Normal → Easy 回退，双人依同样基础难度顺序，再按 _1p、_2p 选择。接受难度大小写和 `ura` 别名。
- 统计当前已发布歌曲指定难度（含 _1p/_2p）；每位用户取总分最高的一次，同一用户同分时取最早提交记录。良／可／不可／连打／最大连击均来自该次游玩，不单独拼接历史最大连击。
- 按总分降序，同分并列（如 1、1、3），同分行按提交时间及成绩 ID 稳定排序。每页 20 人，页码为 1–10000；`total` 为上榜人数。
- 返回 `{songId, difficulty, supported, items, total, page, pageSize}`；`items` 中包含成绩字段以及 `nickname`、`rank`，不暴露邮箱或认证信息。
- 有效单人/双人难度均返回 `supported: true`；P1/P2 分别排行。不存在的歌曲／难度返回 404。
- 修改展示标题和副标题不影响成绩；替换资源会清空该歌曲全部成绩。

邮箱验证和邮件配置统一由 OurTaikoSSO 管理，见 [SSO 接入](SSO.md)。

上传规则接口的 `audioCodecs` 为 `["vorbis", "mp3"]`，`audioExtensions` 为 `[".ogg", ".mp3"]`。multipart 音频字段仍为 `audio`。

## 分类曲库协议更新

`Chart` 新增 `categoryIds`；上传和 PATCH 支持多选分类。游戏 bootstrap 的 `charts` 已替换为 `categories`，谱面改从 `GET /api/v1/game/categories/{categoryId}/charts` 按分类取得。公开分类列表为 `GET /api/v1/categories`。完整请求格式、默认值、迁移及客户端配套要求见 [分类说明](CATEGORIES.md)。

## 各难度制作者

上传表单可传 `difficultyMakers`，其值为 JSON 数组，例如：

```json
[{"course":"Hard","maker":"A"},{"course":"Oni","maker":"B"},{"course":"Edit","maker":"A"}]
```

传入时必须覆盖后端解析出的每个谱面块，course 必须精确匹配全部已解析难度且不能重复，maker 必须是字符串；空字符串表示不署名。署名去掉两端空白，最长 500 UTF-8 字节，不接受控制字符；整个字段最长 64 KiB。校验失败返回 422 `DIFFICULTY_MAKERS_INVALID`。省略该字段时，每块均采用原 TJA 的 MAKER（没有则为空），兼容已有上传客户端。署名变更参与幂等校验。

作品详情、列表、本人列表和游戏分类曲库均返回 `difficulties[].maker`；歌曲级 `maker` 为所有难度署名按块顺序去重的汇总，如上述示例返回 `A | B`。空署名不参与汇总，不以上传者代替署名。搜索谱师匹配任一难度的 maker。下载的原 TJA 保持原始内容；游戏使用 API 汇总 maker 覆盖运行缓存的 MAKER。


## 整体替换歌曲与谱面

`PUT /charts/{id}/files` 使用与上传相同的 multipart、会话、CSRF、Origin、Idempotency-Key 校验，只允许上传者或管理员操作。歌曲 ID、上传者和创建时间保留，只保存一份当前资源。

- `tja` 必填，包含希望保留的全部难度，按普通上传规则校验。
- `audio` 可选，省略时沿用当前音频内容（复制后删除旧对象）；新 TJA 的 WAVE 必须匹配音频文件名。
- `confirmReset` 必须为字符串 `true`，确认清空该曲全体玩家旧成绩，否则返回 400 `REPLACEMENT_CONFIRMATION_REQUIRED`。
- `encoding`、`description`、`categoryIds`、`difficultyMakers` 与新上传含义相同。未提供封面时保留原封面。不要提交已删除的 `expectedVersionId` 字段。

新资源校验、保存成功后，在同一数据库事务中更新 `chart_data`、替换难度并清空该曲全部成绩。A 更新为 A+B 时 A 的旧成绩也清空。名称及翻译覆盖值重置，说明和分类取本次提交值。仅 PATCH 展示信息或修改封面不清空成绩。

旧文件及 ZIP 在数据库提交后删除；失败进入持久队列，启动及每 30 秒重试。先保存新文件再删旧文件，失败不会丢掉当前可用资源。独立上传请求按歌曲串行执行，后成功的请求成为当前资源，不提供历史文件选择。

请求收据保存歌曲、结果文件哈希和载荷摘要。同 key 同载荷重试且文件哈希仍匹配时返回现有作品，不再次清空成绩；载荷变化返回 `IDEMPOTENCY_CONFLICT`，结果文件已经变化返回 `CHART_FILES_CHANGED`，均为 409。重发同一次请求必须保留 key；换 key 表示一次新的替换，即使字节相同也会重新清空成绩。

迁移 024 删除歌曲版本表及所有业务版本 ID 列，保留当前资源和现有当前歌曲成绩。若数据库还存在非当前文件的历史成绩，迁移拒绝并回滚，需先人工核对，不能偷偷把它们重标为当前成绩。数据库升级本身不会重置 ClearStatus。


## 用户名、昵称与个人资料

Fanmade `users` 只保存 SSO 用户 ID。昵称、登录名、邮箱状态、本站管理员角色来自受服务凭证保护的 SSO 接口；每次鉴权都实时确认会话。公开谱面和排行榜批量读取昵称，不返回登录名。SSO 不可用时公开数据继续返回，昵称显示“未知用户”；涉及身份或搜索昵称的请求返回 503，不使用旧身份绕过鉴权。

网站用 OIDC，YataiDON 仍用原 `/game/login`、`/game/bootstrap`、`/game/scores`，登录行为不变，成绩字段以本文为准。原生登录由 Fanmade 转接 SSO，游戏令牌由 SSO 签发、按应用隔离；Fanmade 不存密码或游戏令牌。旧会话在迁移时失效，需要重新登录。网站会话最长一小时，过期后重新通过 SSO 登录；本版不使用刷新令牌。

详见 [SSO 接入与迁移](SSO.md)。

## 网站歌曲封面

- `POST /api/v1/charts`：新增可选 multipart 文件字段 `cover`，允许 `.jpg` / `.jpeg` / `.png` / `.webp`（仅静态图片）（不区分大小写），最大 8 MiB、1600 万像素，单边最多 8192px。文件内容必须与扩展名匹配。服务端完整解码并移除元数据，转换成 WebP，保持比例、最长边不超过 1600px，不放大小图。封面与歌曲在同一事务发布；幂等摘要包含原封面内容和文件名。
- 网站曲库列表、详情、上传响应新增可选 `coverHash`（WebP SHA-256）；无封面时省略。游戏 category/bootstrap 响应保持原协议，不新增此字段。
- `GET /api/v1/charts/{id}/cover`：公开读取已发布且受支持歌曲的封面，返回 `image/webp` 二进制，支持 HEAD、Range、ETag / If-None-Match。未知、已下架歌曲或无封面返回 404。可加 `?v=<coverHash>`；过期 hash 返回 404。`Cache-Control: public, no-cache` 要求每次重新验证，替换后旧 URL 不再返回原图。
- `PUT /api/v1/charts/{id}/cover`：浏览器网站会话、正确 Origin 与 `X-CSRF-Token` 必需；只有 owner 可写（管理员身份不越过此限制）。multipart 仅允许一个 `cover` 文件；规则与新投稿相同。返回 `{ "coverHash": "..." }`。失败保留原封面；成功在同一事务中 DELETE 旧封面行再 INSERT 新行，不保存历史图片、原 JPG/PNG 或磁盘文件。
- `PUT /api/v1/charts/{id}/files` 保留封面，不接受 `cover` 字段；独立封面替换不改变谱面、音频或成绩。删除歌曲同时删除封面行。
- 错误：400 字段/扩展名无效或重复；401 未登录；403 非 owner / CSRF / Origin 无效；413 超大小；422 内容损坏、类型不符或像素超限；503 编码器/数据库不可用或上传繁忙。

运行依赖新增 `cwebp`（WebP tools）；migration 019 使用 `chart_covers.webp bytea` 存储转换后的图片，数据库备份包含封面。


## 用户广场与个人空间（migration 020）

以下接口无需登录，只展示曾在 Fanmade 本地登记的用户，不枚举 SSO 全部账号。

### GET /api/v1/users

| 参数 | 规则 |
| --- | --- |
| q | 可选公开昵称搜索；去首尾空白后最多 200 UTF-8 字节；不搜索登录名或邮箱 |
| sort | active（默认，最近活跃）或 newest（首次登录从新到旧）；未知时间放最后，相同时间按 ID 排序 |
| page | 1–10000 的整数，默认 1；每页固定 12 |

返回 `{items: PublicUser[], total, page, pageSize: 12, profilesAvailable}`，空列表是 `[]`。搜索只返回 SSO 昵称匹配结果与 Fanmade users 的交集；参数非法返回 400 QUERY_INVALID。列表内计数和统计在同一数据库快照中读取。

### GET /api/v1/users/{id}

返回 `{user: PublicUser, profilesAvailable}`。用户不在本地 users 表或 ID 格式无效，返回 404 USER_NOT_FOUND。前端路由为 `/users/:id`。

PublicUser 为明确的公开字段集合，与 `/me` 的认证 User 不同：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | string | 稳定 SSO 用户 ID |
| nickname | string 或 null | 当前公开昵称；缺失/上游不可用时为 null |
| firstLoginAt | RFC3339 时间或 null | 上线后记录的首次成功登录，不是账号注册时间 |
| lastActiveAt | RFC3339 时间或 null | 登录/成功认证请求的最近记录时间，约每分钟写一次 |
| chartCount | integer | 当前公开且支持的作品数量 |
| scoreCount | integer | 当前公开作品的已保存成绩条数 |

不返回登录名、邮箱、邮箱验证状态、应用权限、语言偏好或认证凭证。旧用户未知时间不以迁移时间/上传时间填充。公开 GET 查询不更新被查看用户的活跃时间。

profilesAvailable=false 表示本次昵称查询失败，统计仍返回；个别 ID 无对应公开昵称时也可能为 null。带 q 的搜索遇到 SSO 故障返回 503 SERVICE_UNAVAILABLE，不返回误导性的空结果。

成绩数量从后端数据库聚合，包含每局保存的记录，并非去重后的最高分数、在线状态或历史总游玩次数。隐藏/删除作品不计入；替换歌曲删除旧成绩后统计随之变化。

### GET /api/v1/charts?owner=<id>

现有公开作品列表增加可选 owner 参数（32 位小写十六进制 ID），可与 q/course/page 组合，返回原 ChartList。非法 owner 返回 400；没有公开作品返回空列表。`/me/charts` 始终使用会话用户，忽略请求中的 owner，不能借此读取他人的非公开作品。个人空间使用路径中的用户 ID 作为 owner，不信任页面 URL 中的同名覆盖参数。

### 活动记录

游戏登录成功记录首次登录与最近活跃；OIDC 回调在创建本站会话的同一事务中记录。后续 `/me`、上传、成绩及带有效游戏 Bearer 的认证请求刷新最近活跃；网页切换页面/重新聚焦时通过 `/me` 验证会话。无效凭证、SSO 故障和纯游客访问不更新时间，不缓存认证结论。


## 成绩附带输入记录（migration 021）

`POST /api/v1/game/scores` 和 `POST /api/v1/scores` 均接受可选 `replay_data`：

```json
{
  "version": 1,
  "audio_offset_ms": -20,
  "visual_offset_ms": 10,
  "inputs": [[-15.5, 0], [1234.5, 1], [1234.5, 2], [1200, 3]]
}
```

- 输入条目为 `[游戏时间毫秒, 输入类型]`；0=左 Kat、1=左 Don、2=右 Don、3=右 Kat。时间为客户端传给判定系统的 `ms_from_start`，不是 Unix 时间或音频播放头，包含游戏起始延时/谱面时间轴。按消费顺序保存，不排序或去重；同帧输入可同时间，音频硬同步也可能使时间倒退。
- 两项 offset 为本局开局时的有符号 32 位整数设置，单位毫秒。version 必须为 1，inputs 必须为数组；合法空数组保留。录制最多 100,000 个事件，时间必须是有限数且绝对值不超过 86,400,000 ms。
- **缺少 replay_data、显式 null、类型/字段/事件错误、未知格式版本、录制超过 4 MiB，均保存 SQL NULL，正常成绩仍返回 201；同键重试返回 200。** 整个请求不是有效 JSON 时仍返回 400，成绩自身错误仍按原规则处理；整个请求上限为 8 MiB + 8 KiB，超过返回 413。
- 回放归一化之后参与幂等摘要；无效/缺失/null 等价，旧请求摘要不变。相同 key 下改变有效输入或延迟值返回 409。
- `GET /api/v1/game/bootstrap` 增加 `scoreReplayVersion: 1`。新客户端仅对声明版本 1 的服务器发送附加字段，未声明或不支持的版本仍使用旧成绩格式。上传队列一旦写入，就保持请求体和 key 不变。
- 回执、bootstrap 中的 scores 及排行榜保持原成绩结构，不返回完整回放数据。当前只归档输入和延迟，不实现回放下载、请求轮询、播放或服务端重放验分；也不承诺仅靠这份 v1 数据能还原随机 modifiers 等所有玩法。

本地完整验证：`sh scripts/test.sh` 自动创建并清理私有 PostgreSQL 测试集群，运行 `go test -race ./...` 和 `go vet ./...`。可通过 PG_BINDIR 指定 PostgreSQL 工具目录；指定 DATABASE_TEST_URL 时应指向独立测试库。


### 游戏搜索

`GET /api/v1/game/search?q=&course=&level=&order=default` 默认无需登录，一次返回完整有序结果 `{items,total}`，不含 page/pageSize。items 使用 Chart 结构，可继续通过详情及当前资源接口下载游玩；游戏不需要的封面与上传者昵称不补查（coverHash 省略、uploader 为空）。网页继续使用 `/api/v1/charts`，保持 `{items,total,page,pageSize}`、每页 12 条及原有展示信息。

- `q` 可省略或为空，最多 200 UTF-8 字节；仅匹配公开标题、副标题、所有多语言翻译及难度署名，不匹配上传者昵称。与 `/charts` 共用相同搜索规则，不调用 SSO 昵称搜索。
- `course` 可省略，或为 Easy / Normal / Hard / Oni / Edit；其他值返回 400。
- `level` 可省略，或为 1–10 的整数；非整数和越界返回 400。course 与 level 必须匹配同一 difficulties 行，避免 Easy 的星数误匹配 Oni。网站列表同样支持该参数。
- 游戏不发送 page；网页 page 从 1 开始，最大 10000。只返回已发布、受支持的常规谱面，隐藏、删除和混有不支持难度的作品不返回。
- 两条接口在 `internal/httpapi/search.go` 共用参数校验、筛选和完成状态排序。游戏仅执行一次列表查询，total 取本次结果数量，避免重复 COUNT 和逐页查询。

搜索 `order` 可省略，或设为 `default`（默认）、`unfc`（未全连优先）、`unperfect`（未全良优先）。仅非默认顺序验证当前会话：网站 Cookie、游戏 Bearer。未登录或会话失效时忽略顺序参数，返回默认顺序；身份服务故障返回 503。已登录时依据本人当前歌曲、符合难度/星数条件的历史成绩，将尚未达成者排前，再按创建时间和 ID 倒序；网页最后分页，游戏一次返回全部结果。不删除已达成谱面。全连要求零 bad 且存在判定，全良额外要求零 ok。默认顺序不校验登录。

游戏和两种后端须同步升级；本次按要求不保留旧分页响应的兼容分支。个人顺序每次完整搜索只校验一次会话，不缓存身份来绕过撤销。

## 通关状态（migration 022）

`POST /api/v1/scores`、`POST /api/v1/game/scores` 接受 `ClearStatus`，提交回执、`GET /api/v1/game/bootstrap` 的 `scores` 和 `GET /api/v1/charts/{id}/leaderboard` 的 `items` 返回相同字段。数据库历史成绩统一初始化为 `0`，不根据旧判定推算皇冠。新客户端需要在上传时传入本局状态，并在展示时读取它。

省略、`null` 和显式 `0` 使用相同的归一化请求摘要，保留升级前幂等键的重试兼容性（包括附带回放的成绩）。非零状态参与摘要；同一幂等键改变状态返回 `409 IDEMPOTENCY_CONFLICT`。排行榜仍按原最高分规则选择成绩，返回该局的通关状态。

## 歌曲级模式与 JSON 难度（028）

歌曲响应增加 isSingle，difficulties 为包含 course、level、maker 的数组。一次上传仅可包含 Single 或 Double；混合文件返回 422 TJA_MODE_MIXED，Double 无 P1/P2 标记返回 TJA_PLAYER_REQUIRED，重复 course 返回 TJA_DIFFICULTY_DUPLICATE。

单人 course 为 Easy、Normal、Hard、Oni、Edit；双人则为 Easy_1p/Easy_2p 等。TJA 原文件依然使用 COURSE:Oni 和 #START P1/P2，由解析器生成 API 后缀，不将 COURSE:Oni_1p 写入 TJA。

列表及游戏 search 的 course 筛选接受 15 种规范值；成绩 difficulty 与排行榜 difficulty 接受相同值，仍兼容大小写与 ura 别名。响应不再包含 blockIndex、player、style、cloudScoreEligible。bootstrap 的 courseKeyedDifficulties=true 声明此协议。每首歌曲 JSON 内 course 唯一，双人两侧独立提交、缓存和排行。
