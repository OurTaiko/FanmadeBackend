# 歌曲名称与翻译

## 存储及完整返回

`charts.title` / `subtitle` 保留当前 TJA 的原文。所有语言翻译统一保存在 `title_translations` / `subtitle_translations` JSONB 字典，支持 `en`、`ja`、`zh`、`ko`。没有 override 列或显示标题覆盖层。

列表、详情、上传和编辑响应完整返回 `titleTranslations` / `subtitleTranslations`，不因 Accept-Language、用户偏好或请求参数改变内容。网页和游戏自行选择语言及回退，不把选择后的字符串写回原文。

解析器 `tja-upload-v7` 支持 TITLEEN/JA/ZH/KO 和 SUBTITLEEN/JA/ZH/KO。未提供 EN 时按既有项目约定由 TITLE/SUBTITLE 初始化 en；不猜测文本语言。其他缺失语言不创建键。显式空字符串和副标题 --/++ 前缀保留。重复字段、谱面开始后的语言字段及超过 500 字节的值被拒绝；UTF-8 与显式 Shift-JIS 统一解码。

## 编辑

作者或网站管理员通过 `PATCH /api/v1/charts/{id}` 更新字典，需现有 Cookie、Origin 和 X-CSRF-Token：

```json
{
  "titleTranslations": {"en": "English name", "ja": "日本語名", "zh": "中文名", "ko": "한국어 이름"},
  "subtitleTranslations": {"en": "English subtitle", "zh": "中文副标题"}
}
```

- 缺省语言保持不变；副标题空串表示清空。
- 单个语言传 null，从当前 TJA 恢复此语言；文件没有该语言则删除键。整个字典传 null，恢复文件中的全部翻译。源文件读取失败时不保存修改。
- 每项最多 500 字节，输入去除首尾空白、不接受控制字符，标题不得为空。
- 旧 title/subtitle 写入只作为 en 翻译兼容别名；不得与字典内对应 en 同时提交。读取时 title/subtitle 始终为原文。
- 编辑只改变字典和 metadata_updated_at，不重写 TJA、音频、资源哈希或成绩。搜索匹配原文和所有翻译。
- 替换上传整个歌曲文件时，从新 TJA 重新生成原文和全部翻译，不继承旧翻译。

## 迁移 027

为英文建立 en 键，依次合并原字典、旧语言修改、旧英文修改，保留用户编辑值和显式空副标题；然后删除 title_override、subtitle_override、title_translation_overrides、subtitle_translation_overrides。歌曲 ID、资源键、文件哈希、成绩及其他业务数据不变。

网页编辑器对四种语言使用相同字典请求，显示时依次尝试用户语言、en、原文。游戏端按自己的显示策略选择，详见 [客户端说明](GAME_CLIENT_RESOURCE_DOWNLOAD.md#歌曲名称与全部翻译)。
