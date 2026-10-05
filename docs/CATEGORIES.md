# 分类与游戏按需曲库

## 数据模型

分类使用后端 `CategoryFlags` 位标记枚举，数据库只保存 `charts.category_flags` integer。分类 ID、title、genre 与枚举值固定绑定，按下表顺序返回目录。

| bit 值 | ID | 名称 | box.def GENRE |
| --- | --- | --- | --- |
| 1 | game | Game | GAME |
| 2 | virtual-singer | Virtual Singer | VOCALOID |
| 4 | pop | Pop | J-POP |
| 8 | classic | Classic | CLASSICAL |
| 16 | variety | Variety | VARIETY |
| 32 | anime | Anime | ANIME |

同一作品的分类通过按位 OR 合并，例如 Game + Anime 保存为 33。位值不得重新编号或复用；新增分类需要修改枚举、元数据和数据库约束。API 不暴露整数，仍使用原有字符串 ID 和元数据。

迁移 `029_category_flags.sql` 将旧 `chart_categories` 的全部关联逐首转成位标记，然后删除 `categories` 和 `chart_categories`。隐藏／删除作品同样保留分类；历史无分类作品保存为 0，新增作品默认 Variety（16）。未知分类 ID 会中止并回滚整个迁移，要求先明确映射。

分类是作品层级的信息。改变分类不会更新歌曲 ID、下载文件、哈希或成绩归属；分类随歌曲行物理删除，软删除时保留。数据库 CHECK 拒绝负数及未定义的高位。

## 网站 API

- `GET /api/v1/categories`：公开接口，返回 `{ "items": [{ "id": "game", "title": "Game", "genre": "GAME" }, ...] }`，按固定枚举目录顺序排列。
- `Chart` 对象新增 `categoryIds: string[]`，包括列表、详情、上传与编辑响应。
- `POST /api/v1/charts`：multipart 新增可选文本字段 `categoryIds`，值是 JSON 数组，例如 `["game","pop"]`。不传、`[]`、`null` 均自动归入 `variety`。无效类型或不存在的分类返回 `422 CATEGORIES_INVALID`；重复 ID 自动去重。非默认分类参与上传幂等摘要，数组顺序不影响重试；默认选择保持旧客户端上传摘要兼容。
- `PATCH /api/v1/charts/{id}`：JSON 新增 `categoryIds`。传入时替换全部分类位；`[]` 或 `null` 归入 `variety`；**省略字段保留原分类**。分类和名称编辑在同一个事务中提交，无效分类不会造成部分修改。
- 编辑继续要求作者或现有网站管理员身份、浏览器会话、Origin 与 CSRF；非作者不能自行修改分类。

上传页及信息编辑弹窗从服务器取得选项，支持多选、加载中提示、失败后重试；上传时不选默认 Variety。没有新增分类管理界面。

## 游戏协议与目录

`GET /api/v1/game/bootstrap` 允许匿名读取曲库；携带有效原生 Bearer token 时同时返回账号及成绩：

```json
{"user":{},"categories":[{"id":"game","title":"Game","genre":"GAME","chartCount":1}],"chartCount":1,"scores":[]}
```

游客返回相同的分类和数量，以及 `user: null`、`scores: []`，不读取个人成绩。浏览器 Cookie 不用于原生身份识别；显式携带无效或过期 token 的 bootstrap 请求仍返回 401。

**不返回 `charts` 或读取全量谱面内容。** 从模式选择进入选曲的过场中，游戏取得分类、数量，并仅在登录成功时取得成绩，生成服务器和分类的 `box.def`。分类的 `chartCount` 按关联计数，顶层 `chartCount` 按作品去重；两者使用与分类谱面接口相同的公开状态和难度过滤，并与成绩处于同一个数据库快照。目录呈现为 `服务器 → 分类 → 谱面`。

每次打开服务器文件夹时，游戏工作线程依次请求该服务器所有分类：

```text
GET /api/v1/game/categories/{categoryId}/charts
```

返回 `{ "categoryId": "game", "charts": [...] }`；只含该分类内可公开访问且难度受支持的谱面。有效空分类返回空数组；不存在分类返回 404；游客与登录用户获得相同曲库。谱面详情与原始 TJA／音频下载也允许匿名访问，成绩上传仍要求登录。服务器不是通过传送或修改原始谱包来实现分类；客户端根据分类响应生成展示用 `box.def`。

全部请求成功后一起发布展示目录与数量；进入分类直接读取已生成的列表。每次重新进入服务器都会重新取列表，更新新增／移除的归属以及空分类数量。失败保留上次完整缓存，界面仅提供返回入口和错误提示，重新进入服务器重试，避免展示部分更新。跨分类同一歌曲的展示路径不同，下载缓存和成绩仍按服务器／作品共用，以文件哈希校验资源内容。分类定义与其他设备成绩在下一次从模式选择进入选曲时更新，进入游玩时仍按现有流程刷新作品详情。

## 上线和容量边界

- 后端迁移由正常启动或 `go run ./cmd/server -migrate` 执行。029 只改变内部存储；网页和游戏继续使用现有接口，无需配套修改。
- 升级前停写并备份数据库；回退必须恢复升级前数据库和旧程序，不能只回退镜像。
- 继续按分类加载谱面，单个分类仍返回该分类全部谱面，成绩仍在 bootstrap 一次读取。若单个分类或成绩继续增长，仍需要分页／增量同步；游戏单次 API 响应现有限制为 64 MiB。

## 验证

`category_flags_test.go` 验证 028→029 的全部 64 种组合、历史不支持的难度、未知分类回滚、重复迁移、旧表删除及歌曲／资源／成绩／幂等收据不变。`categories_test.go` 覆盖六类目录、Anime 上传／编辑及游戏分类响应、默认值、多选去重、非法 ID、幂等、原生认证、分类过滤、作者权限、事务回滚、歌曲 ID／文件哈希／成绩不变和软删除。`database_test.go` 比较迁移 013 前后完整业务行，并验证从 016 升级到 017 后保留原分类、归属和业务数据以及重复迁移。`supported_courses_test.go` 确认分类接口继续排除不支持的难度。

前端 `e2e/categories.spec.ts` 使用真实 API 验证分类重试、多选上传、默认值、作者编辑、刷新回显和 390px 手机弹窗；可使用现有邮箱夹具或独立测试账号运行：

```sh
PLAYWRIGHT_BASE_URL=http://127.0.0.1:15173 FANMADE_CATEGORY_TEST_USER=TEST_USER FANMADE_CATEGORY_TEST_PASSWORD=TEST_PASSWORD pnpm exec playwright test categories.spec.ts
```

游戏协议检查位于 YataiDON 的 `tests/fanmade/`，包含 Anime API 元数据到游戏样式的映射、模式过场不预载谱面、服务器加载全部分类、重复进入刷新、空分类归零、部分失败保留完整旧快照、去重计数、多分类共享文件及成绩，以及原有代理、下载完整性与成绩重试回归。
