# PostgreSQL 数据结构（示范版实际实现）

数据库：`ourtaiko_fanmade`。迁移源文件为 `internal/database/schema.sql`、`002_ese.sql`、`003_cloud_score_policy.sql`；由 Go embed 编入程序并通过 `schema_migrations` 记录。迁移使用事务与 advisory lock，重复启动不会重建表。

| 表 | 关键字段 | 关系与约束 |
| --- | --- | --- |
| users | id(text)、username(text)、password_hash(text)、email(text nullable)、email_verified_at(timestamptz nullable)、created_at | 用户名唯一、3–24 位字母数字下划线；非空邮箱 lower(email) 唯一；验证时间非空要求邮箱非空 |
| sessions | token_hash(text PK)、user_id、csrf_token、expires_at | 外键关联用户；Cookie 令牌仅存 SHA-256；独立 CSRF 令牌；到期不可使用 |
| files | id、storage_key、original_filename、sha256、byte_size(bigint)、media_type | storage_key 唯一；正数大小；SHA-256 为 64 位十六进制字符串 |
| charts | id、owner_id、description、status、current_version_id、created_at | 状态 published/deleted/hidden；作品与当前版本复合外键，延迟到事务提交校验 |
| chart_versions | id、chart_id、version_number、title、subtitle、maker、bpm、offset_seconds、demo_start、duration、encoding、wave_filename、tja_file_id、audio_file_id、validation_version | 作品内版本号唯一；分别关联两份资源；时长 0–1200 秒，正数有限 BPM |
| difficulties | version_id、block_index(integer)、course、level(integer)、player、style、cloud_score_eligible | 版本+块序号复合主键；支持 Easy/Normal/Hard/Oni/Edit/Tower/Dan；level 1–10；player 空/P1/P2；style 为 Single/Double；资格为数据库生成列 |
| upload_requests | user_id、idempotency_key、payload_digest、chart_id、created_at | 用户+请求键复合主键；服务端计算载荷摘要，幂等冲突返回 409 |
| schema_migrations | version(integer PK)、applied_at | 已应用版本为 1、2、3、4 |

业务 ID 由服务端密码学随机数生成，为 32 位十六进制文本。时间戳使用 timestamptz。文本编码、BPM 和难度均从服务端实际读取的 TJA 中提取，不信任客户端元数据。

`cloud_score_eligible` 为 STORED 生成列，表达式为 `style = 'Single' AND player = ''`，不能直接赋值。约束禁止带 P1/P2 的记录标为 Single。Double 谱面保留资源与难度记录，只排除未来云端成绩和排行榜；混合文件中的 Single 块不受影响。

迁移 003 从 `STORAGE_DIR` 中的原始 TJA 重新解析所有历史版本（包括已隐藏／删除作品），逐块回填 style，覆盖没有 P1/P2 标记的 `STYLE:Double`。缺少文件、解析失败或块不一致时整笔迁移回滚，不猜测资格；恢复配套资源后重试。迁移 004（`database.go` 中的数据迁移）使用按 COURSE 重置 STYLE 的规则再次回填，纠正已执行 003 时可能产生的跨难度误判。原始文件、版本 ID、文件哈希和原 `validation_version` 不改写。新上传使用 `tja-upload-v2`，新增记录必须显式提供后端解析的 style。

当前尚无 scores 表或成绩提交接口；此字段只代表 Single/Double 范围内的业务资格，不代表游戏兼容性、成绩校验或防作弊已实现。未来成绩接口必须查询数据库中的目标谱面资格并拒绝 Double，不能信任客户端的布尔值。

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
