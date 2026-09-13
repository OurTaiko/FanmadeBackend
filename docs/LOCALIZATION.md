# 多语言名称与谱面信息管理

2026-09-13 原地检查本机 ESE 的全部 3,087 份 UTF-8 TJA，发现以下 8 个字段。数量为字段声明次数，包含空值，也包含因其他校验原因暂不接受上传的文件。

| 语言 | 标题字段 | 次数 | 副标题字段 | 次数 |
| --- | --- | ---: | --- | ---: |
| 默认英文 | TITLE | 3087 | SUBTITLE | 3085 |
| 日文 ja | TITLEJA | 2921 | SUBTITLEJA | 2145 |
| 中文 zh | TITLEZH | 1204 | SUBTITLEZH | 743 |
| 韩文 ko | TITLEKO | 764 | SUBTITLEKO | 30 |

未发现 TITLEEN、SUBTITLEEN 或其他语言后缀。遵循项目约定，TITLE/SUBTITLE 作为默认英文；不根据字段值实际使用的文字自动猜测语言。例如部分 TITLEZH 内容仍为日文或英文，照原值保存。

后端 `tja-upload-v4` 解析六个语言字段，保留空值及 `--`、`++` 等原始前缀；同一字段不得重复，必须在首个 #START 前，每项上限 500 字节。UTF-8 与显式 Shift-JIS 都经过统一解码再提取。没有声明的语言不创建键，不自动复制英文文本充当翻译。

## 入库与返回

默认英文仍使用 `chart_versions.title`、`subtitle`，保持原有接口兼容。新增 `title_translations` 和 `subtitle_translations` 两个 JSONB 列，键为 ja/zh/ko。接口返回 `titleTranslations` 与 `subtitleTranslations`，不含 en，默认英文从 title/subtitle 读取。列表、详情、上传与修改成功响应均包含这些字段。

迁移 006 读取所有已有版本对应的原始 TJA，事务回填新增字段，不改原始文件、默认标题、文件哈希、版本 ID 或成绩；缺失文件或解析失败时回滚并指出版本，需要恢复配套资源再执行。原 validation_version 仍记录当次上传的校验版本。

## 修改名称和副标题

上传者登录后使用 `PATCH /api/v1/charts/{id}`，携带与其他写接口一致的 Cookie、Origin 和 X-CSRF-Token。例如：

```json
{
  "title": "Happy Synthesizer",
  "subtitle": "EasyPop feat. Megurine Luka & GUMI",
  "titleTranslations": {
    "ja": "ハッピーシンセサイザ",
    "zh": "快乐合成器"
  },
  "subtitleTranslations": {
    "zh": "EasyPop feat. 巡音流歌、GUMI"
  }
}
```

- 只更新传入的字段；未传的字段和语言保持原样。
- 默认名称、翻译名称不能为空；副标题允许 `""`，表示明确清空。
- 默认字段传 `null` 恢复原始 TJA 值，例如 `{"title":null}`。
- 语言值传 `null` 移除该语言的修改，恢复文件值，例如 `{"titleTranslations":{"zh":null}}`；原文件没有该语言则恢复为无此键。
- 整个 translations 字段传 `null` 恢复这一组全部语言；空对象 `{}` 不改变这一组。
- 每项最多 500 字节；修改值去除首尾空白，不接受控制字符；翻译键仅允许 ja/zh/ko。

修改内容存于 charts 的覆盖字段，与文件版本的原始解析结果分开。修改在事务中锁定作品并合并，因此并发修改不同字段不会因整体覆盖而丢失；同字段最终以最后执行的修改为准。metadata_updated_at 记录最后成功编辑时间，目前没有历史编辑审计列表。

列表、详情和搜索使用修改后的名称及副标题（含各语言），不修改 versionId、难度块、文件哈希和成绩。原始 TJA／ZIP 下载仍保持上传时的字节：网站改名不会自动改变模拟器从 TJA 内读取到的名称。游戏若要显示网站修改后的名称，应读取 API 元数据；修改原始谱面文件需要未来的资源替换版本流程。

当前已实现后端编辑接口，前端尚无编辑表单和语言选择控件。可先使用 API 调试工具调用；后续“我的作品 → 编辑信息”页面可直接接此接口，提供默认英文及日／中／韩名称、副标题和恢复原值操作。

## 验证

- `go test ./...`、`go vet ./...`。
- `ESE_ROOT=~/Documents/GitHub/ESE go test ./internal/tja` 全量解析。
- `DATABASE_TEST_URL=... go test ./internal/database ./internal/httpapi` 在临时 schema 验证迁移和服务逻辑。
- `python3 scripts/metadata_smoke.py` 用 ESE 原文件通过实际 API 验证上传解析、所有权与 CSRF、修改、搜索、恢复、成绩回执不变、ZIP 字节不变和删除后禁止编辑。测试作品软删除，源文件不改动。
