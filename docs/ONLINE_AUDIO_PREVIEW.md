# 在线歌曲流播放与游戏端预览改进

本文供 OurTaikoPlayerUnity 游戏端接入使用。后端提供原始 MP3／OGG 的 HTTP 渐进下载与字节范围读取，游戏端负责缓冲、解码、试听起点、切歌取消和资源释放。后端已于 2026-10-03 部署并通过公网协议验证，详情见 [生产发布记录](DEPLOYMENT.md#在线音频预览2026-10-03)。游戏端尚未改动，真机试听尚未验证。

## 现状与参考实现

Fanmade 原有 `/api/v1/charts/{id}/audio` 已使用 `http.ServeContent`，具备 Range 能力。因此不能把“游戏没有预览”归因于后端完全不支持流。

当前游戏 `SongSelectScene.UpdatePreview` 只在 `board.Song.music != null` 时播放，没有网络预览分支。`FanmadeClient.PrepareAsync` 下载完整音频并校验 SHA-256 后才准备游玩，不适合作为选曲试听入口。

| 项目与读取版本 | 实现与结论 |
| --- | --- |
| mmfcapi 远端 dev `895e44b41eb0bae309566787202420ec5bd36a6b`，已核对远端哈希 | `src/WebApplication2/Controllers/MaiChartApi/MaiChartController.cs` 的 `GetTrack` 调用 `ReadTrackAsync`，再以 `File(..., true)` 开启 Range，并返回文件哈希。无 HLS、WebSocket 或实时转码。 |
| MajdataPlay 本地 `3975e65e180f9015c45c109ceb70044fe47c5655` | `OnlineSongDetail.GetPreviewAudioTrackAsync` 调用 `AudioManager.LoadMusicFromUriAsync`，把 track URL 直接交给音频层，与完整下载分开。 |
| MajdataPlay BASS 分支 | `BassAudioSample.CreateFromUri` 和 `BassSimpleAudioSample.CreateFromUri` 使用 `Bass.CreateStream`，但均设置 `CanSeek=false`，不能照搬为“任意试听点秒开”。 |
| MajdataPlay Unity 分支 | `UnityAudioSample.CreateAsync` 等待 `SendWebRequest` 完成，未设置 `streamAudio`，不能作为已实现边下载边播放的证据。 |
| MajdataPlay 选曲 | `PreviewSoundPlayer` 有加载延迟、取消 token、预览版本号与过期结果检查；只有可 seek 的 sample 才应用试听起点和区间循环。 |
| OurTaikoPlayerUnity 本地 `3a89e9c2c24f3fc74884351b26a09bce8ae4e817` | 本次只读检查，游戏端改造位置见下文。 |

## 后端协议

继续使用原音频地址，旧下载客户端无需更换 URL。独立 `streamAudio` 处理器只查询当前已发布版本和音频文件，避免每次分段读取都触发 SSO 昵称查询及封面等查询。通过存储根目录内的可 seek 文件句柄发送，不整首读入内存、不转码、不改变哈希。

`GET /api/v1/game/bootstrap` 新增 `audioPreviewVersion: 1`。详情、分类曲库、搜索及上传回执的 `Chart` 新增可选对象：

```json
{
  "audioPreview": {
    "url": "/api/v1/charts/<id>/audio",
    "contentType": "audio/mpeg",
    "startSeconds": 42.5,
    "durationSeconds": 15
  }
}
```

- `url` 是本站根路径，以服务器配置的 origin 解析，不能重复拼接 `/api/v1`。公开读取不要求 Cookie 或 Bearer。
- `contentType` 为 `audio/mpeg` 或 `audio/ogg`，对应原始上传容器。
- `startSeconds` 来自 TJA `DEMOSTART`；负数、非有限值或大于等于整曲时长时回退到 0。它不是音符 OFFSET，也不叠加玩家音频延迟。
- `durationSeconds` 为 `min(15, 整曲时长 - startSeconds)`，是客户端试听窗口建议，不是响应文件长度。无有效时长时省略整个对象。
- 原有 `audioSize` 是整文件字节数，`audioHash` 是整文件 SHA-256 十六进制串，`duration` 是整曲秒数。部分下载不能执行整文件哈希校验。
- 原始 `demoStart` 保留不变。没有新增数据库迁移，没有为历史歌曲生成派生文件。

**这是原始完整音频的流接口。** 没有 Range 的 GET 仍发送完整文件；服务端不会按 `startSeconds` 自动裁剪，也没有新增 `?start=` 时间参数。

| 请求或状态 | 响应 |
| --- | --- |
| 普通 GET | 200，原始字节、正确 Content-Type、Content-Length、inline disposition |
| HEAD | 200，文件头信息，无响应体 |
| 有效 Range | 206，Content-Range 与实际分段长度；支持开放末尾、后缀及 multipart ranges |
| 无法满足或格式错误的范围 | 416；越界范围携带 `Content-Range: bytes */<总长度>` |
| If-None-Match 命中 | 304，无响应体 |
| Range 与匹配的 If-Range ETag | 206 |
| If-Range 不匹配 | 200 完整文件；客户端不能直接追加到旧分段 |
| 歌曲下架、删除或不受支持 | 404 CHART_NOT_FOUND，即使附带旧 ETag 也不能得到 304 |
| 数据库不可用、文件丢失或不可读 | 503 SERVICE_UNAVAILABLE；不能把 JSON 错误体送入音频解码器 |

成功响应有 `Accept-Ranges: bytes`、强 ETag 和 `Cache-Control: public, no-cache, no-transform`。允许缓存但复用前需重验证；这不等于 no-store。`X-Accel-Buffering: no` 提示代理及时转发。Range 与条件请求由 [Go ServeContent](https://pkg.go.dev/net/http#ServeContent) 处理。

既有 `deploy/fanmade.locations.conf` 已设置 `proxy_buffering off`。上线仍需确认实际 OpenResty／CDN 保留 Range、If-Range、Content-Range、ETag，不重写音频字节，不忽略重验证要求。下架保证针对后续请求；已有连接收到的字节与离线副本无法追回。

## 游戏端修改位置

以下文件相对 `Assets/OurTaiko/`：

| 文件或新增模块 | 改进内容 |
| --- | --- |
| `Runtime/Online/FanmadeModels.cs` | 解析能力版本和可选 audioPreview；校验数值有限、非负，URL 与服务器同源且为预期音频路径，兼容字段缺失。 |
| `Runtime/Online/FanmadeClient.cs` | 增加独立预览入口；先复用已校验的本地音频，否则打开在线 URL。预览不调用 PrepareAsync、不下载 TJA，不长期占用正式下载的 Transport 锁。 |
| 新增 `Runtime/Online/OnlineAudioPreview.cs` | 持有请求、取消源、歌曲键、generation 和解码资源，负责缓冲、超时、停止与释放。 |
| `Runtime/Scenes/SongSelectScene.cs` | 在本地 music 分支之外接入在线预览；稳定停留后加载，准备好且仍是当前选曲才淡出 BGM。 |
| `Runtime/Online/OnlineManager.cs` | 对外提供预览能力与状态；试听成功不能把歌曲设置为整曲 Ready，不能绕过正式游玩前的文件哈希检查。 |

ESE 是独立后端，本次未改动它。客户端必须逐服务器探测能力，不能因为 Fanmade 支持就假设所有服务器都支持。

### 选曲与取消流程

1. 复用展开动画和停留逻辑；建议稳定停留约 500–1000 ms 后才创建请求，避免滚轮浏览下载一串歌曲。
2. 以 `(服务器, chartId, audioHash)` 识别预览。选择变更先增加 generation 并取消旧请求；异步结果回主线程后再次比较 generation。
3. 等待解码资源及足够缓冲，期间继续播放 BGM。真正开始试听后才淡出 BGM；失败不阻塞选曲和进入歌曲。
4. 窗口结束后淡出；只有已缓存窗口且支持 seek 时才循环，不支持时不反复重连下载整首。
5. 切歌、关闭歌曲框、进入加载场景、离开场景、组件禁用或销毁时，都应取消并释放临时资源。加载中也要清理，不能把当前 StopPreview 的 `!previewStarted` 早退作为唯一清理入口。
6. 停止后沿用 BGM 恢复时序。旧任务的 finally 只能清理自己拥有的资源，不能停止新任务正在使用的缓存 sample。

缓存应有容量和生命周期上限。试听分段不能写成已验证的完整 `audio.mp3`／`audio.ogg`；开始游戏仍走 PrepareAsync，刷新详情并验证完整 TJA／音频哈希。

### Unity 渐进播放与试听起点

沿用 AudioSource 时，用 `UnityWebRequestMultimedia.GetAudioClip(url, AudioType.MPEG/OGGVORBIS)`，在 SendWebRequest 前设置 `DownloadHandlerAudioClip.streamAudio = true`。不要仅在 await SendWebRequest 返回后播放，否则仍等待完整下载。

根据 downloadedBytes、经过时间、audioSize 和播放器状态判断缓冲是否足够，并设置超时。取得可用 AudioClip 且能持续播放后才交给 AudioSource；下载未完成时不要 Dispose 请求。取消时在主线程停止 AudioSource、Abort 请求，再销毁自己拥有的 AudioClip。[Unity streamAudio 文档](https://docs.unity.com/en-us/engine/6000.3/script-reference/unityengine/networking/downloadhandleraudioclip/streamaudio)要求调用方负责缓冲判断。

**从 DEMOSTART 立即试听需要单独验证。** Range 使用字节位置，不能把秒数当作字节，也不能按 `audioSize × startSeconds / duration` 粗算；VBR MP3、OGG 的解码头、帧与索引会使这种处理失败。streamAudio 本身不承诺立即跳到尚未下载的时间点。

建议先实现可验证的渐进播放：完整本地缓存准确应用 DEMOSTART；在线播放器尚不能到达目标时保持 BGM 等待，或明确采用从 0 开始的试听降级。若要求“远处 DEMOSTART 也立即起播”，需实现具备容器解析和 HTTP seek 的音频后端，或另行设计服务器生成短试听文件。后者未在本次实现；不能照搬 BASS 的 CanSeek=false 后声称支持该目标。

### 兼容与错误处理

- 能力字段缺失或版本未知时保留已有本地试听；不要强行解析未知协议。旧服务器可能支持 Range，但不代表声明了本版能力。
- 404 后重新获取详情一次；哈希变化时只为当前选曲重试，下架时停止。
- 416 后丢弃不匹配分段并重新 HEAD／GET。200 与 206 分开处理，禁止拼接两份完整文件。
- 网络断开、503 或解码失败恢复 BGM，重试有上限；切歌取消不弹错误窗。
- 公开音频请求不发送账号 token，不因预览失败触发重新登录。浏览器跨域／WebGL CORS 不在本次扩展范围。

## 验收与上线

新增 PostgreSQL 集成测试覆盖真实 MP3／OGG 样本的 GET／HEAD、prefix／suffix／open ranges、multipart ranges、416、ETag／If-Range、无需 SSO 的读取、能力声明、窗口边界及不可见歌曲。运行完整现有回归：

```sh
sh scripts/test.sh -count=1
```

该脚本创建并清理独立 PostgreSQL，运行 race 检查及 go vet。2026-10-03 本次完整运行已通过，数据库测试未跳过。本机 FFmpeg 的 Homebrew x265 链接缺失，通过仅给测试进程指定现存兼容库完成验证，没有更改系统安装：

```sh
sh -c 'export DYLD_LIBRARY_PATH=/opt/homebrew/Cellar/x265/4.2/lib; . "$0"' scripts/test.sh -count=1
```

这条兼容命令只适用于该开发机，其他环境应使用正常可执行的 ffmpeg／ffprobe。

上线后选择一首当前已发布歌曲，将真实音频地址赋给 AUDIO_URL，进行只读检查：

```sh
curl -sSI "$AUDIO_URL"
curl -sS -D - -H 'Range: bytes=0-3' "$AUDIO_URL" -o /tmp/fanmade-preview-first4.bin
```

第二个请求应为 206、Content-Length 为 4、Content-Range 总长度等于 audioSize。再用带引号的 ETag 检查 If-None-Match 得到 304，匹配 If-Range 得到 206，不匹配得到完整 200。

游戏端验收覆盖：MP3／OGG 冷热缓存、弱网下整首下载完成前起播、靠近末尾及越界 DEMOSTART、连续切歌无串音、加载中离开场景、取消后资源释放、BGM 恢复、删除后的资源 404、旧服务器兼容、预览后正式下载仍通过完整哈希校验。Unity Editor 和目标真机分别记录首声音延迟、起播时已下载字节数和选曲内存趋势。

2026-10-03 已经通过实际 HTTPS／代理链验证 MP3 与 OGG 的 HEAD、前缀／后缀／开放范围、ETag、If-Range、416 与旧版本 404；26 首公开歌曲均返回预览元数据。后端测试及上线验证不代表游戏试听已通过，完成游戏端接入和真机验证后才能关闭在线预览功能事项。

当前 schema 024 不再使用歌曲版本 ID，旧路径已经移除。S3 直连预览和成绩适配请以 [游戏端资源直连接入说明](GAME_CLIENT_RESOURCE_DOWNLOAD.md) 为准；上面的 2026-10-03 记录仅描述当时上线验证。
