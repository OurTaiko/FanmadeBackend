# 示范版 API

基础路径 `/api/v1`。浏览器通过 Vite 的 `/api` 代理访问 Go 服务，开发环境不依赖跨域 Cookie。JSON 响应中的作品结构与前端 `src/api.ts` 对应；本次未启用 OpenAPI 类型生成。

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
| GET | /charts?q=&course=&page=1 | 返回 `{items,total,page,pageSize}`；每页 12 |
| GET | /me/charts | 本人作品列表，查询参数与公开列表一致 |
| POST | /charts | multipart：tja、audio、encoding（默认 utf-8）、description（可空）、categoryIds、difficultyMakers（可选 JSON）；首次创建 201，同请求重试 200 |
| PUT | /charts/{id}/files | 作者或管理员整体替换 TJA／音频，删除全部旧版本、文件及成绩，返回 200 |
| GET | /charts/{id} | 当前已发布版本详情 |
| PATCH | /charts/{id} | 作者或管理员修改默认英文及 ja/zh/ko 名称／副标题，返回更新后的作品 |
| DELETE | /charts/{id} | 本人软删除；后续资源访问返回 404 |
| GET | /charts/{id}/versions/{version}/tja | 原始 TJA 字节，attachment |
| GET | /charts/{id}/versions/{version}/audio | OGG 或 MP3，支持 Range / ETag；Content-Type 分别为 audio/ogg、audio/mpeg |
| GET | /charts/{id}/versions/{version}/download | ZIP，含原始 TJA 与按 WAVE 原值命名的音频 |

`GET /healthz` 为进程状态，`GET /readyz` 额外检查数据库连接。

所有写请求要求 `Origin` 等于配置 APP_ORIGIN。已登录写请求还要求 `X-CSRF-Token` 等于当前 Session 的令牌。上传要求 `Idempotency-Key`：16–80 位字母、数字或短横线，推荐随机 UUID；同键不同载荷返回 409。

普通错误：`{code,message,requestId,validationVersion}`；TJA 错误额外返回 `errors` 数组，含 code/message/line/expected/actual。常用状态码：400 请求格式，401 未登录，403 权限或来源错误，409 冲突，413 大小限制，422 谱面／音频校验失败，429 频率限制，503 暂时不可用。

数量：1 个 TJA + 1 个 OGG 或 MP3 音频。TJA 最大 2 MiB，音频最大 100 MiB，请求最大 105 MiB，说明最大 4000 字节。文本输入接受 UTF-8（默认）或显式 Shift-JIS；后端独立严格解码并统一保存为无 BOM 的 UTF-8。原输入和转换后均限制 2 MiB。网站自动识别常见编码，在上传前转换为 UTF-8。TJA 的哈希、字节数及下载内容均对应实际保存的 UTF-8 字节；已有作品不自动改写。音频接受单音轨 Ogg Vorbis 或 MPEG Layer III（MP3），扩展名必须与真实格式一致。OGG 检查 CRC 与 EOS；MP3 检查帧边界及截断，支持 CBR/VBR、MPEG-1/2/2.5、ID3 和 APE 标签，允许内嵌封面，不接受 free-format MP3。两种格式均需可完整解码，时长不超过 20 分钟。原音频不转码，WAVE 与上传文件名仍须 NFC 后严格匹配（区分大小写）。最多两个上传同时处理。

邮箱验证码、名称／副标题编辑和歌曲文件替换已提供。独立修改投稿说明、管理员管理界面仍未提供；替换歌曲时可同时提交新的说明。

## 支持的难度

仅接受 Easy / Normal / Hard / Oni / Edit；TJA 的 COURSE 值支持数字 0–4 和大小写变体。Tower / Dan / 5 / 6 返回 422 `TJA_COURSE_UNSUPPORTED`，`errors[].line` 指向 COURSE 行。只要出现不支持的 COURSE，整份 TJA 拒绝，不能只上传其中的普通难度块。

列表 `course` 参数只允许五种规范英文名，其他非空值返回 400 `DIFFICULTY_INVALID`。成绩接口拒绝不支持的难度（422），排行榜查询同样拒绝（400）。已有不支持的当前版本由迁移 012 下架，列表和游戏曲库不返回，详情、编辑和所有文件下载返回 404；游戏快照排除不支持版本的全部成绩。数据仍保存在数据库和资源目录中。

## 谱面块的云端成绩资格

上传成功、作品详情、公开列表与本人列表中的 `difficulties[]` 增加 `style` 和 `cloudScoreEligible`，已有 `course`、`level`、`blockIndex`、`player` 字段保留。例如：

```json
[
  {"course":"Oni","level":7,"blockIndex":0,"player":"","style":"Single","cloudScoreEligible":true},
  {"course":"Oni","level":7,"blockIndex":1,"player":"P1","style":"Double","cloudScoreEligible":false},
  {"course":"Oni","level":7,"blockIndex":2,"player":"P2","style":"Double","cloudScoreEligible":false}
]
```

后端解析 STYLE 值时不区分大小写，支持 Single/0、Double/1 和 ESE 中的 Duet（按 Double）。STYLE 在当前 COURSE 内持续生效直到下一次 STYLE 声明，每次 COURSE 声明恢复默认 Single；P1/P2 块无论 STYLE 如何均按 Double。未知 STYLE 或在谱面块内部声明 STYLE 返回 422 `TJA_STRUCTURE_INVALID`，包含行号。前后端校验标识为 `tja-upload-v6`，都拒绝不支持的难度；STYLE、重复单人难度等规则仍由后端权威校验。

每个 TJA 的同一难度最多一个 Single 块。重复时在上传阶段返回 422 `TJA_DIFFICULTY_DUPLICATE`，`errors[0].line` 指向重复块的 #START，message 指出首次声明行号。难度名与数字别名归一化后比较，修改 LEVEL 不会使重复难度合法；P1/P2 和显式 Double 块不参与这个单人重复检查。

Double 允许正常上传、试听和下载原始文件，`cloudScoreEligible:false` 不是上传错误。同一 TJA 的每个块独立判定，不能因含 Double 而禁用整个作品的 Single 块。资格从数据库返回，上传请求不接受客户端指定 style 或资格。

成绩提交接口已实现，目标必须是服务端查到的 Single 块。排行榜及游戏客户端专用鉴权均已实现。

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
  "max_combo": 350
}
```

示例 songId 需替换为作品 API 返回的 `id`，不是文件哈希或 versionId。八个字段全部必填，`max_combo` 为最大连击：good 为良、ok 为可、bad 为不可、score 为总分、drumroll 为连打数。六个数字不接受 null、字符串或小数；计数为 0–2147483647，总分为 0–9007199254740991（JSON/JavaScript 安全整数范围）。`max_combo` 为必填的 0–2147483647 整数；省略或 null 返回 422，非整数返回 400，负数或越界值返回 422。这些是存储边界，不是玩法理论上限。难度仅接受 Easy、Normal、Hard、Oni、Edit，不区分大小写、忽略首尾空白；Ura 归一为 Edit。

后端锁定当前已发布版本，按难度寻找唯一有资格的单人块。同一难度含一个 Single 和若干 Double 时只选 Single；只有 Double 时拒绝。重复 Single 已在新上传时拦截，成绩接口仍对历史异常数据返回歧义错误。浏览器请求省略 versionId 时按提交时的当前版本归属；原生游戏接口要求 versionId 并校验版本一致。服务端保存客户端上报值，未根据游玩过程重算成绩。

成功返回 201，响应包含上述八个成绩字段（difficulty 已归一化），以及服务端生成的 `id`、`userId`、`versionId`、`blockIndex` 和 `submittedAt`（UTC RFC3339）。每次新提交保留一条游玩记录，不覆盖最高分；排行榜读取时选出每人的最高分记录。歌曲更名或软删除不删除已保存成绩；下架后拒绝新成绩。

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

## 原生游戏接入（Fanmade v1）

原生客户端使用独立 Bearer 会话，不发送浏览器的 Origin、Cookie 或 CSRF token。浏览器接口保留原有 Origin/CSRF 校验；两类 token 不能互换。所有非 GET 请求仍有每 IP 每分钟 40 次限制。公网部署使用 HTTPS。

- `POST /api/v1/game/login`：JSON `{ "username": "...", "password": "..." }`，返回 `user`、`accessToken`、`expiresIn`（604800 秒）。无 Set-Cookie。后续原生请求使用 `Authorization: Bearer <accessToken>`。
- `GET /api/v1/game/bootstrap`：返回 `{ "user": {...}, "categories": [{ "id": "game", "title": "Game", "genre": "GAME", "chartCount": 1 }, ...], "chartCount": 1, "scores": [...] }`，在同一个 PostgreSQL repeatable-read 快照中读取分类、各分类数量、服务器去重数量和当前用户的历史成绩，不返回全部谱面内容。`GET /api/v1/game/categories/{categoryId}/charts` 按需返回分类内谱面；详情见 [分类协议](CATEGORIES.md)。历史版本成绩保留；当前难度展示最佳成绩时按 songId/versionId/difficulty 筛选。
- 原始文件继续使用 `GET /api/v1/charts/{id}/versions/{version}/{tja|audio}`。加载前重新获取 `GET /api/v1/charts/{id}` 核对 versionId 与哈希，文件下载后必须核对 SHA-256。
- `POST /api/v1/game/scores`：八项成绩字段（含必填 `max_combo`）加**必填** `versionId`，并使用每次游玩固定的 `Idempotency-Key`。字段示例：`{"songId":"<32 hex>","versionId":"<32 hex>","difficulty":"Oni","good":300,"ok":10,"bad":2,"score":900000,"drumroll":50,"max_combo":250}`。DOUBLE 仍不支持云端成绩。版本变化返回 `409 CHART_VERSION_CHANGED`，不将旧成绩写到新版本。临时失败重试时保持请求体和 key 不变。

`POST /api/v1/scores` 接受可选 `versionId`，但同样要求 `max_combo`。提交回执、`game/bootstrap` 中的成绩以及排行榜记录都必须包含 `max_combo`。原生会话由 SSO GameSession 持有，按业务客户端隔离；Fanmade 只保存浏览器会话，迁移版本为 018。

游客无需登录即可使用 `game/bootstrap`、`game/categories/{categoryId}/charts`、谱面详情和文件下载。bootstrap 无 Authorization 时返回相同的公开分类与数量，但 `user: null`、`scores: []`，不查询个人成绩，浏览器 Cookie 不会改变游客身份。携带 Bearer token 时仍严格校验原生会话；无效或过期 token 返回 401，客户端可重新登录。`POST /api/v1/game/scores` 仍必须携带有效原生 Bearer token，游客返回 401。游戏端需同步升级，才能移除旧版的账号必填限制；无需新增数据库迁移。
# 谱面排行榜

`GET /api/v1/charts/{id}/leaderboard?difficulty=Oni&page=1&versionId=<当前版本>` 公开读取，无需登录。

- 未传难度时按 Oni → Edit → Hard → Normal → Easy 回退，其余难度按谱面块顺序选择。接受难度大小写和 `ura` 别名。
- 仅统计当前发布版本的单人谱；每位用户取总分最高的一次，同一用户同分时取最早提交记录。良／可／不可／连打／最大连击均来自该次游玩，不单独拼接历史最大连击。
- 按总分降序，同分并列（如 1、1、3），同分行按提交时间及成绩 ID 稳定排序。每页 20 人，页码为 1–10000；`total` 为上榜人数。
- 返回 `{songId, versionId, difficulty, supported, items, total, page, pageSize}`；`items` 中包含成绩字段以及 `nickname`、`rank`，不暴露邮箱或认证信息。
- DOUBLE 难度返回 `supported: false` 与空列表。不存在的歌曲／难度返回 404，旧版本参数返回 409 `CHART_VERSION_CHANGED`，有歧义的单人难度返回 409。
- 修改展示标题和副标题不影响成绩；谱面版本更新后新旧成绩分开统计。原始成绩记录保留。

邮箱验证和邮件配置统一由 OurTaikoSSO 管理，见 [SSO 接入](SSO.md)。

上传规则接口的 `audioCodecs` 为 `["vorbis", "mp3"]`，`audioExtensions` 为 `[".ogg", ".mp3"]`。multipart 音频字段仍为 `audio`。

## 分类曲库协议更新

`Chart` 新增 `categoryIds`；上传和 PATCH 支持多选分类。游戏 bootstrap 的 `charts` 已替换为 `categories`，谱面改从 `GET /api/v1/game/categories/{categoryId}/charts` 按分类取得。公开分类列表为 `GET /api/v1/categories`。完整请求格式、默认值、迁移及客户端配套要求见 [分类说明](CATEGORIES.md)。

## 各难度制作者

上传表单可传 `difficultyMakers`，其值为 JSON 数组，例如：

```json
[{"blockIndex":0,"maker":"A"},{"blockIndex":1,"maker":"B"},{"blockIndex":2,"maker":"A"}]
```

传入时必须覆盖后端解析出的每个谱面块，blockIndex 不能重复或越界，maker 必须是字符串；空字符串表示不署名。署名去掉两端空白，最长 500 UTF-8 字节，不接受控制字符；整个字段最长 64 KiB。校验失败返回 422 `DIFFICULTY_MAKERS_INVALID`。省略该字段时，每块均采用原 TJA 的 MAKER（没有则为空），兼容已有上传客户端。署名变更参与幂等校验。

作品详情、列表、本人列表和游戏分类曲库均返回 `difficulties[].maker`；歌曲级 `maker` 为所有难度署名按块顺序去重的汇总，如上述示例返回 `A | B`。空署名不参与汇总，不以上传者代替署名。搜索谱师匹配任一难度的 maker。下载的原 TJA 保持原始内容；游戏使用 API 汇总 maker 覆盖运行缓存的 MAKER。


## 整体替换歌曲与谱面

`PUT /charts/{id}/files` 使用与上传相同的 multipart、会话、CSRF、Origin、Idempotency-Key 校验，只允许上传者或管理员操作。歌曲 ID、上传者和创建时间保留；成功后生成全新的 `versionId`，只保留新版本。

字段：

- `tja`：必填，包含本次希望保留的全部难度；按普通上传规则重新校验。
- `audio`：可选。省略时复制当前音频到新版本，仍删除旧音频对象；新 TJA 的 WAVE 必须与沿用的音频文件名匹配。提供时按普通上传规则校验新音频。
- `expectedVersionId`：必填，更新页面加载时的版本 ID。版本已变化返回 409 `CHART_VERSION_CHANGED`，不会再次删除数据。
- `confirmReset`：必须为字符串 `true`，表示已确认清空全体玩家的旧成绩；缺失返回 400 `REPLACEMENT_CONFIRMATION_REQUIRED`。
- `encoding`、`description`、`categoryIds`、`difficultyMakers`：与新上传含义相同。前端回显并提交原说明和分类；新难度的制作者默认取新 TJA 的 MAKER。

新文件校验通过后，在同一数据库事务中切换当前版本、删除该歌曲的**全部成绩和历史版本**（包括难度记录）、移除旧文件元数据，并保存新版本。A 更新为 A+B 时，A 的旧成绩也全部清空。名称、副标题及多语言覆盖值清空，使用新 TJA 内容；说明和分类取本次提交值。仅 PATCH 编辑展示信息时仍保留版本与成绩。

旧文件删除任务与事务一起落库，提交后立即执行；若文件系统暂时不可用或进程退出，服务器启动时及每 30 秒重试。旧文件资源接口从版本切换起立即返回 404。普通软删除接口的行为不变。迁移 015 只增加请求版本标识和文件删除队列，部署本身不删除歌曲或成绩。

请求收据保留歌曲、结果版本和载荷摘要，以支持网络失败后的安全重试，不保留旧谱面或旧成绩。相同键和载荷重试返回首次保存的当前版本，**不会再次清空新成绩**；载荷不同或该结果已被后续更新替换时返回 409。并发更新只有一个可以基于同一个 `expectedVersionId` 成功。

游戏端无需额外删除逻辑：刷新曲库得到新 `versionId` 后，旧版本的成绩缓存不再匹配；重新获取 `/game/bootstrap` 不会返回已删除成绩。旧版本的在途成绩提交返回 409，不会计入新版本。


## 用户名、昵称与个人资料

Fanmade `users` 只保存 SSO 用户 ID。昵称、登录名、邮箱状态、本站管理员角色来自受服务凭证保护的 SSO 接口；每次鉴权都实时确认会话。公开谱面和排行榜批量读取昵称，不返回登录名。SSO 不可用时公开数据继续返回，昵称显示“未知用户”；涉及身份或搜索昵称的请求返回 503，不使用旧身份绕过鉴权。

网站用 OIDC，YataiDON 仍用原 `/game/login`、`/game/bootstrap`、`/game/scores`，请求与响应字段不变。原生登录由 Fanmade 转接 SSO，游戏令牌由 SSO 签发、按应用隔离；Fanmade 不存密码或游戏令牌。旧会话在迁移时失效，需要重新登录。网站会话最长一小时，过期后重新通过 SSO 登录；本版不使用刷新令牌。

详见 [SSO 接入与迁移](SSO.md)。

## 网站歌曲封面

- `POST /api/v1/charts`：新增可选 multipart 文件字段 `cover`，允许 `.jpg` / `.png` / `.webp`（仅静态图片）（不区分大小写），最大 8 MiB、1600 万像素，单边最多 8192px。文件内容必须与扩展名匹配。服务端完整解码并移除元数据，转换成 WebP，保持比例、最长边不超过 1600px，不放大小图。封面与歌曲在同一事务发布；幂等摘要包含原封面内容和文件名。
- 网站曲库列表、详情、上传响应新增可选 `coverHash`（WebP SHA-256）；无封面时省略。游戏 category/bootstrap 响应保持原协议，不新增此字段。
- `GET /api/v1/charts/{id}/cover`：公开读取已发布且受支持歌曲的封面，返回 `image/webp` 二进制，支持 HEAD、Range、ETag / If-None-Match。未知、已下架歌曲或无封面返回 404。可加 `?v=<coverHash>`；过期 hash 返回 404。`Cache-Control: public, no-cache` 要求每次重新验证，替换后旧 URL 不再返回原图。
- `PUT /api/v1/charts/{id}/cover`：浏览器网站会话、正确 Origin 与 `X-CSRF-Token` 必需；只有 owner 可写（管理员身份不越过此限制）。multipart 仅允许一个 `cover` 文件；规则与新投稿相同。返回 `{ "coverHash": "..." }`。失败保留原封面；成功在同一事务中 DELETE 旧封面行再 INSERT 新行，不保存历史图片、原 JPG/PNG 或磁盘文件。
- `PUT /api/v1/charts/{id}/files` 保留封面，不接受 `cover` 字段；独立封面替换不改变歌曲版本、谱面、音频或成绩。删除歌曲同时删除封面行。
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
| scoreCount | integer | 当前公开作品版本的已保存成绩条数 |

不返回登录名、邮箱、邮箱验证状态、应用权限、语言偏好或认证凭证。旧用户未知时间不以迁移时间/上传时间填充。公开 GET 查询不更新被查看用户的活跃时间。

profilesAvailable=false 表示本次昵称查询失败，统计仍返回；个别 ID 无对应公开昵称时也可能为 null。带 q 的搜索遇到 SSO 故障返回 503 SERVICE_UNAVAILABLE，不返回误导性的空结果。

成绩数量从后端数据库聚合，包含每局保存的记录，并非去重后的最高分数、在线状态或历史总游玩次数。隐藏/删除作品及旧版本不计入；替换歌曲删除旧成绩后统计随之变化。

### GET /api/v1/charts?owner=<id>

现有公开作品列表增加可选 owner 参数（32 位小写十六进制 ID），可与 q/course/page 组合，返回原 ChartList。非法 owner 返回 400；没有公开作品返回空列表。`/me/charts` 始终使用会话用户，忽略请求中的 owner，不能借此读取他人的非公开作品。个人空间使用路径中的用户 ID 作为 owner，不信任页面 URL 中的同名覆盖参数。

### 活动记录

游戏登录成功记录首次登录与最近活跃；OIDC 回调在创建本站会话的同一事务中记录。后续 `/me`、上传、成绩及带有效游戏 Bearer 的认证请求刷新最近活跃；网页切换页面/重新聚焦时通过 `/me` 验证会话。无效凭证、SSO 故障和纯游客访问不更新时间，不缓存认证结论。
