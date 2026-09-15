# 分类与游戏按需曲库

## 数据模型

迁移 `013_categories.sql` 只新增 `categories` 和 `chart_categories`：分类保存显示名称和游戏 genre，关联表以 `(category_id, chart_id)` 为主键，引用稳定的 `charts.id`。同一作品可属于多个分类。既有谱面、版本、文件、用户及成绩行不改写；迁移为已有作品新增 Variety 关联，包含已隐藏／删除作品，但公开接口仍按原有可见性规则过滤。

| ID | 名称 | box.def GENRE |
| --- | --- | --- |
| game | Game | GAME |
| virtual-singer | Virtual Singer | VOCALOID |
| pop | Pop | J-POP |
| classic | Classic | CLASSICAL |
| variety | Variety | VARIETY |

分类是作品层级的信息，不属于某个文件版本。改变分类不会更新谱面 ID、版本 ID、下载文件、哈希或成绩归属。物理删除作品时关联级联删除；软删除保留关联。

## 网站 API

- `GET /api/v1/categories`：公开接口，返回 `{ "items": [{ "id": "game", "title": "Game", "genre": "GAME" }, ...] }`，按配置顺序排列。
- `Chart` 对象新增 `categoryIds: string[]`，包括列表、详情、上传与编辑响应。
- `POST /api/v1/charts`：multipart 新增可选文本字段 `categoryIds`，值是 JSON 数组，例如 `["game","pop"]`。不传、`[]`、`null` 均自动归入 `variety`。无效类型或不存在的分类返回 `422 CATEGORIES_INVALID`；重复 ID 自动去重。非默认分类参与上传幂等摘要，数组顺序不影响重试；默认选择保持旧客户端上传摘要兼容。
- `PATCH /api/v1/charts/{id}`：JSON 新增 `categoryIds`。传入时替换全部关联；`[]` 或 `null` 归入 `variety`；**省略字段保留原分类**。分类和名称编辑在同一个事务中提交，无效分类不会造成部分修改。
- 编辑继续要求作者或现有网站管理员身份、浏览器会话、Origin 与 CSRF；非作者不能自行修改关联。

上传页及信息编辑弹窗从服务器取得选项，支持多选、加载中提示、失败后重试；上传时不选默认 Variety。没有新增分类管理界面。

## 游戏协议与目录

`GET /api/v1/game/bootstrap` 继续要求原生 Bearer token，返回：

```json
{"user":{},"categories":[{"id":"game","title":"Game","genre":"GAME"}],"scores":[]}
```

**不再返回 `charts`，也不查询全量谱面。** 游戏启动只生成服务器和分类的 `box.def`。目录呈现为 `服务器 → 分类 → 谱面`。

打开某个分类时，游戏工作线程请求：

```text
GET /api/v1/game/categories/{categoryId}/charts
Authorization: Bearer ...
```

返回 `{ "categoryId": "game", "charts": [...] }`；只含该分类内可公开访问且难度受支持的谱面。有效空分类返回空数组；不存在分类返回 404；未登录返回 401。服务器不是通过传送或修改原始谱包来实现分类；客户端根据分类响应生成展示用 `box.def`。

成功加载的分类在本次游戏进程内复用；失败保留返回入口和状态提示，重新进入可重试，部分列表会清理。跨分类同一歌曲的展示路径不同，下载缓存和成绩仍按服务器／作品／版本标识共用。网页修改分类后，已缓存的游戏分类列表需重启游戏刷新；进入游玩时仍按现有流程刷新作品详情。

## 上线和容量边界

- 后端迁移由正常启动或 `go run ./cmd/server -migrate` 执行。先部署后端迁移与分类 API，再部署网页，并同步更新游戏客户端。
- 这是原生 bootstrap 响应的协议变更：旧游戏依赖 `charts`，新版游戏依赖 `categories`，两端版本必须配套。不提供退回全量曲库的兼容分支。
- 本次消除启动时的全量谱面请求。按需求，单个分类仍返回该分类全部谱面，成绩仍在 bootstrap 一次读取。若单个分类或成绩继续增长，仍需要分页／增量同步；游戏单次 API 响应现有限制为 64 MiB。
- 本次只在独立临时数据库执行迁移和联调，未迁移或部署线上服务。

## 验证

`categories_test.go` 覆盖五类目录、默认值、多选去重、非法 ID、幂等、原生认证、分类过滤、作者权限、事务回滚、版本／文件哈希／成绩不变和软删除。`database_test.go` 比较迁移 013 前后完整业务行，验证旧谱面归入 Variety 以及重复迁移。`supported_courses_test.go` 确认分类接口继续排除不支持的难度。

前端 `e2e/categories.spec.ts` 使用真实 API 验证分类重试、多选上传、默认值、作者编辑、刷新回显和 390px 手机弹窗；可使用现有邮箱夹具或独立测试账号运行：

```sh
PLAYWRIGHT_BASE_URL=http://127.0.0.1:15173 FANMADE_CATEGORY_TEST_USER=TEST_USER FANMADE_CATEGORY_TEST_PASSWORD=TEST_PASSWORD pnpm exec playwright test categories.spec.ts
```

游戏协议检查位于 YataiDON 的 `tests/fanmade/`，包含不预载谱面、进入时加载、重复进入复用、空分类与失败重试、多分类共享文件及成绩，以及原有代理、下载完整性与成绩重试回归。
