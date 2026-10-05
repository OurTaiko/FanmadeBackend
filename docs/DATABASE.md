# Fanmade 后端数据库结构

本文按当前源码整理，描述完整执行迁移 001–026 后的 PostgreSQL 结构；不是生产数据库的实时巡检结果。结构依据为 [SQL 迁移](../internal/database/)、[迁移入口](../internal/database/database.go) 和 [SSO 迁移入口](../internal/database/sso.go)。

## 数据职责与总览

Fanmade 保存用户上传的谱面、音频索引、网站封面、分类、成绩和网站会话。账号资料、密码、语言偏好及应用管理员角色由 SSO 管理，本地 `users` 保存稳定 ID 与本站首次登录/最近活跃时间。昵称通过 SSO 查询，不复制进谱面或成绩。

| 数据库 | 保存内容 | 身份关联 |
| --- | --- | --- |
| OurTaikoSSO | 账号、应用、认证凭证与会话 | 账号 ID 的权威来源 |
| Fanmade | 上传作品、资源、成绩、网页会话 | `users.id` 对应 SSO 用户 ID |
| ESE | ESE 仓库曲库、历史版本、成绩 | 独立存储；不共享 Fanmade 表 |

跨库用户 ID 是应用级关联，没有指向 SSO 的 PostgreSQL 外键。Fanmade 的用户行不等于有效登录，每次受保护请求仍需 SSO 验证。

当前共 **14 张表**，按用途分组：

| 分组 | 表 | 日常用途 |
| --- | --- | --- |
| public | users、charts、chart_resources、difficulties、scores、categories、chart_categories | 7 张业务表，pgAdmin 日常查看此组 |
| auth | sessions、oidc_flows | 本站会话和登录交接 |
| internal | upload_requests、retired_score_requests、pending_objects、retired_files、schema_migrations | 上传/成绩重试、文件清理和结构迁移 |

分组仍在同一个 Fanmade 数据库内，不影响独立 SSO 数据库。程序连接使用 `public,auth,internal` 搜索路径。下文未标注“可空”的字段均为 `NOT NULL`；`PK` 为主键，`UQ` 为唯一约束，时间均为 `timestamptz`。业务 ID 通常由应用生成 32 位小写十六进制字符串，但 Fanmade 多数 `text` ID 列没有对应正则 CHECK。

```mermaid
erDiagram
  users ||--o{ sessions : has
  users ||--o{ charts : owns
  users ||--o{ scores : plays
  users ||--o{ upload_requests : submits
  charts ||--|{ chart_resources : resources
  charts ||--o{ difficulties : contains
  charts ||--o{ scores : song
  difficulties ||--o{ scores : eligible_target
  charts ||--o{ upload_requests : receipt
  charts ||--o{ chart_categories : categorized
  categories ||--o{ chart_categories : contains
```

`charts` 一行代表一首歌曲，`chart_resources` 每种资源最多一行。延迟约束触发器确保事务提交时每首歌曲都有 TJA 和音频；封面可选，S3 模式另外保存 ZIP。

## 账号与网站认证

### users

| 字段 | 类型 / 约束 | 用途 |
| --- | --- | --- |
| id | text PK | SSO 稳定用户 ID，旧账号迁移保持原值 |
| first_login_at | timestamptz，可空，无默认值 | 功能上线后首次成功登录本站的记录时间；不是 SSO 注册时间 |
| last_active_at | timestamptz，可空，无默认值 | 最近登录或成功认证请求的记录时间 |

018 已删除登录名、密码哈希、邮箱、昵称、管理员状态及注册时间。020 只增加本站活动时间，不重新保存账号资料。不要依据初始 `schema.sql` 把历史账号字段视作当前结构。

020 不回填未知历史日期：现有行两个时间保持 NULL。成功游戏登录或 OIDC 回调首次设置 first_login_at；持有旧会话访问只更新 last_active_at，不伪造首次登录。网页会话创建与首次登录写入同一事务。

鉴权成功后按用户原子 UPSERT，first_login_at 仅从 NULL 变成已记录时间；last_active_at 最多约每分钟更新一次并保持不倒退。SSO 拒绝或故障不更新时间。CHECK 要求 first_login_at 非空时，last_active_at 非空且不早于它。页面显示“暂无记录”代表尚无可用记录，不代表用户从未使用过服务。

### sessions

| 字段 | 类型 / 约束 | 用途 |
| --- | --- | --- |
| token_hash | text PK | 本站 Cookie 随机凭证的 SHA-256 |
| user_id | text FK → users.id，ON DELETE CASCADE | 会话所属用户 |
| csrf_token | text | 本站 CSRF 校验值 |
| expires_at | timestamptz | 到期时间 |
| upstream_token | bytea | AES-256-GCM 加密的 SSO OAuth access token，密文绑定 token_hash |

索引：`sessions_expiry(expires_at)`。网站会话最长一小时，且不超过上游令牌到期时间；没有自动 refresh。游戏 token 不写入此表。加密密钥在服务私有配置中，数据库备份不能替代密钥备份。

### oidc_flows

| 字段 | 类型 / 约束 | 用途 |
| --- | --- | --- |
| state_hash | text PK | OAuth state 的哈希 |
| binding_hash | text | 发起登录的浏览器绑定值哈希 |
| nonce | text | OIDC ID token nonce |
| verifier | text | PKCE verifier，敏感临时数据 |
| return_path | text | 校验后的本站返回路径 |
| expires_at | timestamptz | 创建后五分钟到期 |

索引：`oidc_flows_expiry(expires_at)`。回调通过 `DELETE … RETURNING` 原子消费，防止重复使用；发起新流程时清理过期行。本表没有用户外键。

## 作品、资源与展示

### chart_resources

| 字段 | 类型 / 约束 | 用途 |
| --- | --- | --- |
| chart_id | text FK → charts.id，ON DELETE CASCADE | 所属歌曲 |
| kind | text，tja/audio/cover/archive | 与 chart_id 组成主键，每种最多一个 |
| storage_key | text，非空，普通索引 | S3_PREFIX 下的对象键，或 STORAGE_DIR 下的相对路径 |
| original_filename | text | 下载文件名 |
| sha256 | text，64 位小写十六进制 | 实际保存字节的内容哈希，用于客户端缓存判断 |
| byte_size | bigint，> 0 | 文件大小；cover 还限制为 12–8388608 字节 |
| media_type | text | 媒体类型，cover 必须为 image/webp |

数据库只保存资源索引，文件字节在 S3 或本地资源目录。封面不再使用 bytea。允许多个歌曲引用同一对象，清理前会再次检查引用；不按哈希自动去重，也不保存历史资源。TJA 新上传仍统一保存为无 BOM UTF-8，迁移不会重写已有 TJA/音频。

### charts

| 字段 | 类型 / 默认值 / 约束 | 用途 |
| --- | --- | --- |
| id | text PK | 稳定作品 ID |
| owner_id | text FK → users.id | 上传者 |
| description | text，默认空串 | 作品说明 |
| status | text，默认 published；published/deleted/hidden | 发布、软删除、隐藏 |
| created_at | timestamptz，默认 now() | 创建时间 |
| metadata_updated_at | timestamptz，可空 | 展示元数据更新时间 |
| title | text | 文件解析的默认标题 |
| subtitle | text，默认空串 | 默认副标题 |
| title_translations / subtitle_translations | jsonb，各默认 {}，CHECK 为 object | 完整 en/ja/zh/ko 翻译，编辑直接保存于此 |
| bpm | double precision，CHECK > 0 且 < Infinity | BPM |
| offset_seconds / demo_start | double precision，各默认 0 | 偏移与试听起点，秒 |
| duration | double precision，CHECK > 0 且 ≤ 1200 | 音频时长，秒 |
| encoding | text，utf-8 / shift-jis | 实际保存的 TJA 编码 |
| wave_filename | text | 音频文件名 |

没有歌曲版本 ID、版本号、`validation_version` 或历史资源行。014 已将 maker 移到难度块。迁移 027 将旧英文及其他语言的修改合并到翻译字典，并删除四个 override 列。普通编辑直接更新字典；恢复原值时读取当前 TJA，不存储额外覆盖层。title/subtitle 保留原始 TJA 文本，API 不选择显示语言。

### difficulties

| 字段 | 类型 / 默认值 / 约束 | 用途 |
| --- | --- | --- |
| chart_id | text FK → charts.id，ON DELETE CASCADE | 所属歌曲 |
| block_index | integer | TJA 谱面块序号；与 chart_id 组成 PK |
| course | text | 新写入只允许 Easy/Normal/Hard/Oni/Edit |
| level | integer，CHECK 1–10 | 星级 |
| player | text，默认空串；空串/P1/P2 | 玩家标记 |
| style | text；Single/Double，无默认值 | 后端逐块解析的模式 |
| cloud_score_eligible | boolean STORED 生成列 | `style = 'Single' AND player = ''` |
| maker | text，默认空串 | 本难度制作者 |

额外 CHECK：`player = '' OR style = 'Double'`。UQ `difficulties_score_target(chart_id,block_index,course,cloud_score_eligible)` 供成绩复合外键引用。

012 的 course CHECK 使用 `NOT VALID`：不扫描否定历史归档数据，但仍约束后续插入/更新。历史 Tower/Dan 行可能存在。云成绩资格由数据库计算，不能直接写入；Double 可保留和游玩，不能接受云成绩。API 歌曲级 maker 由各块署名去重汇总，不另存一列。

### categories 与 chart_categories

| 表 | 字段 | 类型 / 约束 |
| --- | --- | --- |
| categories | id | text PK，匹配 `^[a-z][a-z0-9-]{0,63}$` |
| categories | title / genre | text，各非空 |
| categories | sort_order | integer UQ |
| chart_categories | category_id | text FK → categories.id |
| chart_categories | chart_id | text FK → charts.id，ON DELETE CASCADE |

关联表 PK 为 `(category_id,chart_id)`，作品可属于多个分类。额外索引 `chart_categories_chart(chart_id)`。013 将已有作品归入 Variety；017 新增 Anime，当前种子分类为 Game、Virtual Singer、Pop、Classic、Variety、Anime。数据库没有“作品至少一个分类”的 CHECK，此规则由业务流程保证。

封面以 `chart_resources.kind='cover'` 保存。单独换封面不影响成绩；新封面完成写入后，在事务中替换索引，旧对象进入清理队列。

## 成绩与幂等

### scores

| 字段 | 类型 / 默认值 / 约束 | 用途 |
| --- | --- | --- |
| id | text PK | 每局成绩 ID |
| user_id | text FK → users.id | 成绩所属账号 |
| song_id | text FK → charts.id | 作品 ID |
| block_index | integer | 对应难度块 |
| difficulty | text | Easy/Normal/Hard/Oni/Edit；CHECK 为 NOT VALID |
| cloud_score_eligible | boolean，默认 true，CHECK 为 true | 强制关联可计分难度 |
| good / ok / bad | integer，各 CHECK ≥ 0 | 良、可、不可 |
| score | bigint，CHECK 0–9007199254740991 | 总分，兼容 JavaScript 安全整数范围 |
| drumroll / max_combo | integer，各 CHECK ≥ 0 | 连打数、最大连击；max_combo 无默认值 |
| submitted_at | timestamptz，默认 now() | 服务端保存时间 |
| idempotency_key | text，可空 | 可选重试键 |
| payload_digest | text，CHECK 长度为 64 | 请求摘要 |
| clear_status | integer，NOT NULL，默认 0，CHECK 0–3 | 022 新增；API 字段 `ClearStatus`，0 无皇冠／未知，1 通关，2 全连，3 全良 |
| replay_data | jsonb，可空，CHECK 为 object | 021 新增；输入事件与本局两项延迟，未知/无效为 SQL NULL |

复合 FK：`(song_id,block_index,difficulty,cloud_score_eligible) → difficulties(chart_id,block_index,course,cloud_score_eligible)`。

UQ `(user_id,idempotency_key)`；NULL 允许多局独立提交，同用户同键同载荷返回原结果，不同载荷返回 409。每局一行，不覆盖最高分。排行榜从 `scores` 查询每人最高分，没有排行榜表或持久名次字段。数值和关联约束不等于服务端重放验分。

021 不回填历史输入；历史成绩与旧客户端新成绩的 replay_data 均为 SQL NULL。合法录制采用 version=1 对象，内含 audio_offset_ms、visual_offset_ms 和 inputs 数组。空数组表示已记录但全程未敲击；不能用空对象或 JSON null 代替 SQL NULL。校验失败先归一为 NULL 再计算摘要，无回放请求保留旧版摘要，已有幂等键升级后可继续重试。输入记录不随 bootstrap、排行榜或提交回执返回；当前无回放读取接口。

022 将所有历史成绩的 clear_status 初始化为 0，保留其余字段与原请求摘要；旧写入省略该列也默认 0。重复启动不会重置已有状态。该加列迁移随服务启动自动执行；回退旧程序可保留新增列。

### upload_requests

| 字段 | 类型 / 默认值 / 约束 | 用途 |
| --- | --- | --- |
| user_id | text FK → users.id | 请求用户 |
| idempotency_key | text | 与 user_id 组成 PK |
| payload_digest | text | 内容摘要；SQL 无长度 CHECK |
| chart_id | text FK → charts.id | 请求结果作品 |
| created_at | timestamptz，默认 now() | 收据创建时间 |
| tja_sha256 / audio_sha256 | text，各默认空串 | 请求结果文件内容哈希 |

024 将上传收据关联改为当前文件哈希。旧收据保留载荷摘要，结果已被其他文件替换时返回冲突；相同请求重试不会重复清空成绩。

### retired_score_requests

`user_id text FK → users.id ON DELETE CASCADE` 与 `idempotency_key text` 组成 PK。替换文件前保存被删除成绩的请求键，防止已接收旧成绩的网络重试重新创建记录（409 SCORE_REMOVED）。不存旧分数、文件或歌曲版本。

S3 下载 ZIP 以 `chart_resources.kind='archive'` 保存，本地模式仍按需生成。客户端资源接口字段保持 `resources.download`，与数据库 kind 命名无关。

## 维护表与索引

| 表 | 字段 | 用途 |
| --- | --- | --- |
| retired_files | storage_key text PK；created_at timestamptz 默认 now() | 已提交的对象/磁盘文件删除队列；不保留已删除资源的外键 |
| pending_objects | storage_key text PK；created_at timestamptz 默认 now() | 先记录外部写入意图，超过一天的未引用对象可重试清理 |
| schema_migrations | version integer PK；applied_at timestamptz 默认 now() | 逐项记录已经执行的迁移 |

除 PK/UQ 自动创建的索引外，显式索引如下：

| 索引 | 列 / 条件 | 用途 |
| --- | --- | --- |
| users_recent_activity | last_active_at DESC NULLS LAST, id | 用户广场最近活跃排序 |
| users_first_login | first_login_at DESC NULLS LAST, id | 首次登录从新到旧排序 |
| sessions_expiry | expires_at | 有效期查询 |
| oidc_flows_expiry | expires_at | 临时流程清理 |
| charts_published_recent | created_at DESC, id DESC；WHERE status='published' | 公开作品列表 |
| chart_resources_storage_key | storage_key | 资源引用检查与清理 |
| charts_owner | owner_id, created_at DESC | 用户作品 |
| chart_categories_chart | chart_id | 反查分类 |
| scores_user_recent | user_id, submitted_at DESC, id DESC | 个人历史 |
| scores_chart_difficulty | song_id, difficulty, submitted_at DESC | 作品难度成绩 |
| scores_leaderboard_current | song_id, difficulty, block_index, user_id, score DESC, submitted_at, id | 每人最高分及并列排序 |

PostgreSQL 不会为每个外键自动创建索引，不能把关系图当作索引清单。当前搜索主要依赖 ILIKE，没有全文搜索索引。

## 生命周期与迁移

- 修改网站展示元数据：写 charts 覆盖列，保留原文件和成绩。
- 整体替换歌曲/谱面：保留 charts.id、归属及封面；删除该作品全部旧成绩及难度，更新 charts 的解析元数据和当前资源行；清除标题覆盖；旧资源索引删除并把存储键加入 retired_files。新数据在同一事务发布，失败回滚。
- 软删除作品：status 改为 deleted，同时删除封面；歌曲数据、资源、分类关系和成绩保留，但不再公开访问。
- 文件清理：启动时和每 30 秒重试 retired_files，仍被 chart_resources 引用的键保留对象并移除队列行；无引用的对象/磁盘文件删除成功或已不存在后移除队列行。不是通用的孤儿文件扫描器。
- 删除本地用户：sessions 和 retired_score_requests 外键声明级联；已有作品、成绩、上传收据的外键通常会阻止直接物理删除。SSO 删除用户不会跨库级联删除业务数据。

| 迁移 | 变化 |
| --- | --- |
| 001 | users、sessions、files、charts、chart_versions、difficulties、upload_requests |
| 002 | P1/P2 与历史 Tower/Dan 支持 |
| 003 / 004 | style、云成绩生成列；读取 TJA 回填并修正按 COURSE 重置规则；004 在 Go 中实现 |
| 005 | scores 与成绩复合外键 |
| 006 / 007 | 原始多语言元数据与作品展示覆盖 |
| 008 / 009 / 010 | 历史管理员列、排行榜索引、max_combo |
| 011 / 012 | 历史邮箱验证表；限制新难度写入并隐藏不支持作品 |
| 013 / 014 | 多选分类；maker 迁至难度块 |
| 015 / 016 / 017 | 替换收据与文件清理队列；历史昵称；Anime 分类 |
| 018 | SSO 用户瘦身、清空旧会话、移除 registration_codes/email_send_limits、建立 oidc_flows |
| 019 | chart_covers |
| 020 | users.first_login_at / last_active_at、时间顺序约束和两个排序索引 |
| 021 | scores.replay_data 可空输入记录 |
| 022 | scores.clear_status，历史成绩统一初始化为 0，约束 0–3 |
| 023 | S3 封面元数据、chart_archives、pending_objects 与对象清理触发器 |
| 024 | 删除歌曲版本表/字段，改为 chart_data；成绩/难度/ZIP 按歌曲关联；旧文件清理及成绩请求墓碑 |
| 025 | chart_data 合并入 charts；files/封面/ZIP 合为 chart_resources；删除 validation_version；本地 bytea 封面经校验导出 |
| 026 | 在 SSO 迁移完成后将认证和维护表移动到 auth/internal |

`Migrate` 在事务与 advisory lock 内执行 001–017、019–025；随后 `MigrateSSO` 另开受保护事务执行 018 与 026。检查迁移时使用 `internal.schema_migrations`；不能只用最大编号推断 018 已执行。003/004/006 的历史回填需要与数据库配套的 TJA 文件，缺失或解析不一致会回滚。

有旧账号时，018 要求先完成备份确认及全部用户 ID 的 SSO 存在性核对，再删除账号资料列。旧资料导入不是双向同步。操作见 [SSO 文档](SSO.md)；回退 018 必须恢复迁移前业务数据库及匹配程序，不能只换二进制。

备份需包含 PostgreSQL、本地文件或 S3 对象、受限配置及会话加密密钥。S3 模式数据库不一定保存封面字节。迁移 024 保留当前成绩所有非版本字段，若存在非当前资源的历史成绩会整体拒绝迁移。旧资源仅在新事务提交后清理。回退 024 必须恢复配套旧数据库、旧镜像和需要的对象，不能只回退程序；历史迁移 SQL 与回填代码保留用于升级旧安装，不代表当前业务仍有歌曲版本。

## 用户广场与个人空间

`GET /api/v1/users` 提供昵称搜索、分页和最近活跃/首次登录排序；`GET /api/v1/users/{id}` 提供单个本地用户的公开资料与统计。昵称实时从 SSO 批量查询，不落本地表。广场包含本地 users 行（含迁移旧用户），不枚举仅在 SSO 注册的其他账号。

公开字段仅为 id、nickname、firstLoginAt、lastActiveAt、chartCount、scoreCount。chartCount 统计当前可公开访问的作品；scoreCount 统计当前可公开作品的已保存成绩行，排除隐藏/已删除作品。统计在后端从 charts/scores 聚合，不新增可能漂移的计数列；整体替换会删除旧成绩，因此不是累计历史游玩次数。

用户广场的计数、分页和统计使用同一只读 repeatable-read 快照；个人空间在一个 SQL 语句中聚合。访问他人的公开空间不更新该用户活跃时间。昵称查询失败时返回 null 和 profilesAvailable=false，仍可查看业务统计；昵称搜索依赖 SSO，故障返回 503 而非假装没有匹配用户。

个人空间作品列表通过 `GET /api/v1/charts?owner=<用户ID>` 精确筛选，保留原公开状态与支持难度检查。登录名、邮箱、权限与 token 不在公开响应中。完整参数见 [API 文档](API.md)。

025/026 升级不变更歌曲 ID、成绩、难度、分类或上传收据，不移动 S3 对象，也不更换对象键。已有本地 bytea 封面先导出并逐字节核对，才删除旧表；失败会回滚数据库。S3 后端如仍有仅存于 bytea 的封面会拒绝迁移，应先完成资源迁移。回退必须恢复升级前数据库、旧镜像及配套资源，不能只换旧镜像。首次分组要求 auth/internal 尚未被其他应用占用。

pgAdmin 只读角色需要新 schema 的 USAGE 权限。表原有 SELECT 授权随表移动保留；新表和后续表需相应默认 SELECT 授权。不要授予写权限来解决查看问题。
