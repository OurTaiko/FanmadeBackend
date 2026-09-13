# PostgreSQL 数据结构（示范版实际实现）

数据库：`ourtaiko_fanmade`。迁移源文件为 `internal/database/schema.sql` 和 `internal/database/002_ese.sql`；由 Go embed 编入程序并通过 `schema_migrations` 记录。迁移使用事务与 advisory lock，重复启动不会重建表。

| 表 | 关键字段 | 关系与约束 |
| --- | --- | --- |
| users | id(text)、username(text)、password_hash(text)、email(text nullable)、email_verified_at(timestamptz nullable)、created_at | 用户名唯一、3–24 位字母数字下划线；非空邮箱 lower(email) 唯一；验证时间非空要求邮箱非空 |
| sessions | token_hash(text PK)、user_id、csrf_token、expires_at | 外键关联用户；Cookie 令牌仅存 SHA-256；独立 CSRF 令牌；到期不可使用 |
| files | id、storage_key、original_filename、sha256、byte_size(bigint)、media_type | storage_key 唯一；正数大小；SHA-256 为 64 位十六进制字符串 |
| charts | id、owner_id、description、status、current_version_id、created_at | 状态 published/deleted/hidden；作品与当前版本复合外键，延迟到事务提交校验 |
| chart_versions | id、chart_id、version_number、title、subtitle、maker、bpm、offset_seconds、demo_start、duration、encoding、wave_filename、tja_file_id、audio_file_id、validation_version | 作品内版本号唯一；分别关联两份资源；时长 0–1200 秒，正数有限 BPM |
| difficulties | version_id、block_index(integer)、course、level(integer)、player | 版本+块序号复合主键；支持 Easy/Normal/Hard/Oni/Edit/Tower/Dan；level 1–10；player 空/P1/P2 |
| upload_requests | user_id、idempotency_key、payload_digest、chart_id、created_at | 用户+请求键复合主键；服务端计算载荷摘要，幂等冲突返回 409 |
| schema_migrations | version(integer PK)、applied_at | 已应用版本为 1、2 |

业务 ID 由服务端密码学随机数生成，为 32 位十六进制文本。时间戳使用 timestamptz。文本编码、BPM 和难度均从服务端实际读取的 TJA 中提取，不信任客户端元数据。

```mermaid
erDiagram
  USERS ||--o{ SESSIONS : has
  USERS ||--o{ CHARTS : uploads
  CHARTS ||--|{ CHART_VERSIONS : contains
  CHART_VERSIONS ||--|{ DIFFICULTIES : contains
  FILES ||--o{ CHART_VERSIONS : tja_or_audio
  USERS ||--o{ UPLOAD_REQUESTS : submits
  CHARTS ||--o{ UPLOAD_REQUESTS : result
```

查询索引覆盖发布作品时间顺序、用户作品列表、Session 过期时间和唯一邮箱。示范版搜索使用 ILIKE，分页使用 page/pageSize（每页 12）；全文索引与游标分页留到规模增长后。

邮箱验证码／验证链接令牌表尚未创建。后续需要新增只存 token_hash 的单次限时验证表，并为未验证账号限制投稿；不把示范版用户自动视为邮箱已验证。
