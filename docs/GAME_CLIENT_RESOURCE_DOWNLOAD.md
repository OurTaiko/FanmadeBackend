# OurTaikoPlay 游戏端资源直连接入说明

本文交给游戏客户端 agent 实施。目标是让游戏向 Fanmade 后端查询歌曲当前资源及哈希，然后直接从 S3 下载文件；以后后端返回 CloudFront 链接时，客户端仍使用相同流程。正式环境已部署此接口；游戏端需实现本文逻辑。

用户已明确：新资源下载流程只使用歌曲 ID，不传版本 ID，不提供历史版本选择。文件是否更新由 SHA-256 判断。不要使用歌名定位歌曲，也不要根据歌曲 ID 自行拼接 S3 对象路径。

## 正式与测试入口

正式服务器 base URL：`https://fanmade.ourtaiko.org`。正式库已经取消歌曲版本字段，成绩请求不能再提交 `versionId`；资源清单包含 SHA-256。

旧 `/s3-test` 服务已停用，测试数据库已删除，不再作为接入入口。使用正式入口；缓存和待上传成绩仍必须按服务器身份隔离。

本次游戏端需要完成整首 TJA 和音频直连、哈希缓存、链接刷新、下载进度和取消，并适配现有在线音频预览。如果客户端已有封面下载，也使用新的封面资源链接。ZIP 是可选下载资源，正常进歌不需要同时下载 ZIP、TJA 和音频。不要为此次适配新增无关的封面界面或 ZIP 导入功能。

本次不需要 AWS SDK、AWS 密钥、桶列表权限或客户端自行签名。登录、曲库、歌曲详情、成绩上传仍使用 Fanmade API。不要修改服务器、桶配置或 CloudFront 配置。

当前后端已全面取消歌曲版本：资源、歌曲详情、成绩、排行榜都以歌曲 ID 关联，不再接收或返回 `versionId`。上传替换文件只保留新资源，同时清空该曲旧成绩。不要把版本字段改成空值或固定值，应从新协议请求体和缓存身份中移除。

## 能力识别

连接服务器时，照常请求：

```text
GET {baseUrl}/api/v1/game/bootstrap
```

先读取布尔值 `songIdOnly`：`true` 表示歌曲、下载和成绩都采用本文的新协议；缺失时属于旧协议。此能力与存储方式独立，本地存储后端也可以为 true。兼容其他旧服务器的代码必须按服务器分别分支，不能向新版发送版本字段。

再读取整数 `resourceDownloadVersion`：

| 值 | 客户端行为 |
| --- | --- |
| `1` | 启用本文的资源清单和直接下载流程 |
| 缺失或 `0` | 若 songIdOnly=true，使用 `/charts/{id}/tja`、`/audio`；否则保持旧服务器下载流程 |
| 其他值或类型错误 | 不假定协议兼容；采用既有兼容流程或明确报告不支持 |

这个数字是下载协议能力号，不是歌曲版本号。每个服务器分别保存能力，重连时重新读取，不能把测试服的能力应用到其他服务器。

API 请求地址要保留用户配置的 base URL 路径前缀。当前客户端以 `baseUrl.TrimEnd('/') + path` 组合地址；不要改成会把根路径替换掉的 URI 组合方式。

## 当前资源接口

```text
GET {baseUrl}/api/v1/charts/{chartId}/resources
```

请求不包含 `versionId`。只返回该公开歌曲当前可用的资源；不存在、已下架或不可玩的歌曲返回 404。当前接口公开可读，不需要为了查询链接新增登录要求。

下面的 JSON 是字段示例，URL、哈希和时间均为占位数据，不是可用下载链接：

```json
{
  "chartId": "0123456789abcdef0123456789abcdef",
  "expiresAt": "2026-10-05T01:15:00Z",
  "resources": {
    "tja": {
      "url": "https://storage.example/chart?signature=example-get",
      "headUrl": "https://storage.example/chart?signature=example-head",
      "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "size": 12345,
      "contentType": "application/octet-stream"
    },
    "audio": {
      "url": "https://storage.example/audio?signature=example-get",
      "headUrl": "https://storage.example/audio?signature=example-head",
      "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "size": 4567890,
      "contentType": "audio/ogg"
    }
  }
}
```

实际响应的资源键如下；示例省略了 ZIP 和封面对象，它们的字段结构与上面相同。

| 资源键 | 内容 | 是否存在 |
| --- | --- | --- |
| `tja` | 原始 TJA | 必有 |
| `audio` | 原始 OGG 或 MP3 | 必有 |
| `download` | 包含 TJA 和音频的 ZIP | 必有，按需使用 |
| `cover` | WebP 封面 | 可选，没有封面时不返回这个键 |

字段语义：

- `chartId` 必须等于正在请求的歌曲 ID。
- `url` 用于 GET，包括音频单段 Range GET。
- `headUrl` 用于 HEAD。GET 与 HEAD 的签名不同，不能对 `url` 直接发 HEAD，也不能对 `headUrl` 发 GET。
- `sha256` 是完整文件内容的 SHA-256，当前为 64 位小写十六进制。文件更新判断和完整下载后的校验均使用它。
- `size` 是完整文件的字节数，用 64 位整数处理，并先检查业务大小上限。
- `contentType` 是文件类型。音频为 `audio/ogg` 或 `audio/mpeg`；不能根据 URL 后缀猜格式。
- `expiresAt` 是这批链接的 UTC 到期时间，当前有效期约 15 分钟。链接到期不代表本地文件失效。

将 URL 视为不透明字符串：不改主机、路径或查询参数，不删签名，不添加业务参数，也不从路径中的 UUID 推导歌曲版本。生产下载只接受合法 HTTPS URL，保留 TLS 校验和禁止自动重定向的现有策略。测试 fixture 可通过测试专用配置使用 loopback HTTP，不能因此放宽正式请求规则。

## 成绩上传、在线成绩与旧缓存迁移

新接口仍为 `POST {baseUrl}/api/v1/game/scores`，API Bearer 鉴权不变。请求示例：

```json
{"songId":"0123456789abcdef0123456789abcdef","difficulty":"Oni","good":300,"ok":10,"bad":2,"score":900000,"drumroll":50,"max_combo":250,"ClearStatus":1}
```

- 不发送 `versionId`，服务器对未知字段返回 400。`songId` 是详情的 `id`；`difficulty` 取 Easy/Normal/Hard/Oni/Edit。`ClearStatus` 为 0 无皇冠/未知、1 通关、2 全连、3 全良，必须使用本局真实状态，不从判定计数推测普通通关。可继续按 `scoreReplayVersion` 附带 `replay_data`。
- 每局固定一个 Idempotency-Key。超时或临时故障重试保留 key 和成绩数据，不能把失败重试当作新游玩。200 表示已保存收据重放，201 表示新记录。
- `409 SCORE_REMOVED` 表示这局已接收过的成绩在文件替换时被清除：从待上传队列终止重试，不换 key 重传。`409 IDEMPOTENCY_CONFLICT` 表示 key 被用于其他载荷，同样不能自动换 key 绕过。401 走现有登录恢复，404 歌曲/难度不可用不能无限重试。
- 本地待上传记录保存游玩时 TJA/audio 哈希。发送前刷新当前详情/资源哈希，任一不匹配则保留为本地历史并标记不再自动上传；不要改成新哈希后强行上传。哈希不作为成绩 API 新字段发送。
- 边界：服务端没有游玩时的文件标识，无法识别从未收到过的旧谱面离线成绩。客户端的预检也不能完全消除检查后、提交前的文件替换竞态；本方案不承诺严格阻止这类成绩。
- 收据、bootstrap scores、排行榜记录均不再有版本字段；在线最佳成绩按服务器、账号、songId、difficulty 归属。成功刷新 bootstrap 后以完整响应替换该服务器/账号的在线成绩快照，不能只 merge 追加。替换后旧成绩已从服务端删除，旧皇冠也必须移除。本地离线游玩历史可保留，但不能当作当前在线最佳成绩显示。
- 升级现有本地缓存时，不按旧版本目录盲目信任内容。可扫描复用实际 SHA-256 和大小均匹配的文件，之后写入新缓存索引。对旧待上传记录，只有能从原记录或可信旧资源对应关系取得游玩时哈希时才转换为新协议；转换保留原 key 与成绩值。没有可信哈希时保留本地记录并停止自动上传，不静默删除，也不凭当前详情补齐旧局身份。
- 本地写回待上传记录需要原子迁移，重启后不得重复生成 key。服务端可识别迁移前已接收成绩的旧摘要，转换后同一局仍返回原收据，不能人为构造新 key。
- 排行榜使用 `GET /api/v1/charts/{id}/leaderboard?difficulty=Oni&page=1`，不加歌曲版本查询参数；下载代理路由为 `/api/v1/charts/{id}/{tja|audio|download}`。

## 完整歌曲准备流程

建议新增资源清单模型和统一解析方法，整首下载、试听和封面共同复用；不要在每个分块请求中重新请求资源清单。

1. 刷新歌曲详情，取得当前 TJA 元数据、难度和音频名称。
2. 用歌曲 ID 获取资源清单，校验必需字段、URL、哈希、大小、到期时间和 `chartId`。
3. 对照详情中的 `tjaHash`、`audioHash` 与清单中的哈希。若不一致，可能刚好有人替换了歌曲，重新获取详情和清单，最多重试一次；仍不一致就报告歌曲正在更新，不使用新音频配旧谱面元数据。详情与资源接口不是一个原子请求，客户端必须处理这个窗口。
4. 分别检查 TJA 和音频缓存。实际文件大小和 SHA-256 均匹配时直接复用，不发送对应资源的 GET。不要只信磁盘上保存的“已验证”标记而跳过损坏检测。
5. 对缺失或哈希不同的文件，使用清单中的完整 `url` 直接下载到临时文件或受限临时缓冲区。保留现有下载进度、取消、超时及大小限制。
6. 完整下载必须成功得到 200；最终字节数必须等于 `size`，计算出的 SHA-256 必须等于清单值。校验失败时丢弃本次临时内容，不覆盖已有有效文件。
7. 所有必要文件均通过校验后，原子发布新的本地缓存记录，沿用现有流程生成 `play.tja`，再允许进歌。不要让其他读取方看到一半更新的 TJA 和音频组合。
8. 将本次游玩的 songId、TJA/audio 哈希随成绩本地保存，按下节规则处理在线/离线上传。不要保存临时下载 URL 作为成绩身份。

伪代码如下，表示行为要求，不限定具体类名：

```text
if endpoint.resourceDownloadVersion != 1:
    return PrepareViaAPI(endpoint.songIdOnly)

chart, manifest = FetchMatchingChartAndResources(chartId, maxRefresh = 1)

for kind in [tja, audio]:
    resource = manifest.resources[kind]
    if LocalFileMatches(resource.sha256, resource.size):
        MarkCached(kind)
        continue

    temporary = DownloadExternal(resource.url, cancellation, progress, sizeLimit)
    RequireExactSizeAndSHA256(temporary, resource)
    StageVerifiedFile(kind, temporary)

PublishCompleteCacheAndBuildPlayableTja(chart, manifest)
```

网络重试和链接刷新应包在上述下载步骤外层，并遵守下一节的有界重试规则。

## 缓存与文件名

新流程的缓存身份以服务器身份、歌曲 ID、资源类型和内容哈希构成。例如：

```text
objects/<endpointId>/<chartId>/tja/<sha256>/original.tja
objects/<endpointId>/<chartId>/audio/<sha256>/audio.ogg
objects/<endpointId>/<chartId>/audio/<sha256>/audio.mp3
```

这是建议目录结构，可按现有工程调整，但不要再要求 `versionId` 才能找到新资源缓存。endpointId 继续沿用项目现有的服务器及账号隔离方式，不能只按歌曲 ID 全局缓存。生成的 `play.tja` 还依赖歌曲详情，不能仅因为原 TJA 哈希没变就跳过重新生成。

哈希相同的缓存继续有效，即使下载链接、签名或到期时间改变。只有音频哈希变化时，只重下音频；只有 TJA 变化时，只重下 TJA。旧目录中的文件可以在验证哈希后导入新布局，不要一次性删除玩家全部缓存。

不把完整签名 URL 持久保存到缓存索引、服务器配置、日志或待上传队列；只缓存文件和必要的非敏感元数据。允许在内存短期复用同一份清单，但启动新的歌曲准备流程时要刷新清单，不能用“链接还没过期”推断歌曲一定未更新。

TJA 本地使用 `.tja`；音频根据经过核对的详情或 `contentType` 选择 `.ogg` / `.mp3`。桶内对象可能没有扩展名，这不影响原始格式。清单没有 `filename` 字段，不要依赖不存在的字段或把整个带查询参数的 URL 当作文件名。

## 外部下载通道

当前 `FanmadeEndpoint.RequestBytesAsync` 会拼接 API base URL，并加入游戏 Bearer token。因此新增一个明确接收绝对资源 URL 的下载通道，不能直接把完整 S3 URL 传给这个旧方法。

外部通道应做到：

- 不携带游戏 `Authorization`、Cookie、`Idempotency-Key` 或登录信息；仅发送下载所需的请求头。
- 不使用 `AuthorizedAsync` 的 401 重新登录逻辑处理 S3 错误。
- 保留用户选择的网络代理、取消信号、下载超时、响应和实际读取字节上限、进度回调。
- 避免在 Unity 主线程执行大文件读取、哈希计算或同步等待网络；复用现有后台准备方式。
- 没有 Content-Length 时仍按实际读取量限制大小；清单的 `size` 可作为进度总量。不能依照不受限的响应长度直接分配大数组。
- 使用流式写临时文件或沿用已有限制的缓冲方案；不要因为此次接入引入比现有下载更多的大文件内存副本。

新能力服务器的正常下载路径必须真的访问返回的外部 URL。不要在遇到任何错误时悄悄改用旧后端转发接口，否则流量仍经过后端，测试也无法发现适配失败。旧接口用于没有新能力的服务器兼容。

## 链接刷新与失败处理

每次开始新的外部请求前检查 `expiresAt`，建议预留 30–60 秒时钟误差。即使本机时间不准，外部 403 也应触发一次资源清单刷新。到期时间用于决定是否需要新链接，不用于中断一个已经开始并正常传输的下载。AWS 在请求开始时判断链接有效性；连接断开后重试可能需要新链接。[AWS 预签名链接说明](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html)

| 情况 | 必须采取的行为 |
| --- | --- |
| 链接即将到期 | 按同一歌曲 ID 重新请求资源清单 |
| 外部 GET 或 HEAD 返回 403 | 刷新清单并最多重试一次；403 也可能是权限或对象问题，第二次仍失败就报告，不能无限重试或重新登录游戏 |
| 外部对象返回 404 | 按同一歌曲 ID 刷新一次清单，核对当前哈希；仍失败则报告资源不可用 |
| 资源清单返回歌曲 404 | 提示歌曲不存在或已下架，停止下载，不将它当成旧服务器能力缺失 |
| 刷新后哈希不变 | 复用已验证完整文件，使用新链接重试缺失文件 |
| 刷新后 TJA 或音频哈希变化 | 丢弃不匹配的临时数据，重新执行详情与清单一致性检查；不能混合旧新分块 |
| 网络超时或 5xx | 保留原错误提示和有限重试；支持取消，不无限循环 |
| 大小或哈希不匹配 | 不发布缓存，清理临时文件并报告下载完整性失败 |
| 用户切歌或取消 | 取消资源清单和文件请求，停止重试，不让旧请求完成后更新新歌曲 UI |

限制的是整个准备操作的刷新次数，不是每一层各自重试一次后相乘。避免清单刷新、文件重试、预览重试互相递归。不要记录带完整签名查询参数的异常 URL。

## 在线音频预览

现有在线预览只读取解码器需要的音频分块，应继续保持这一行为。不要为了适配 S3 先下载整首音频，也不要宣称部分分块已经通过完整文件 SHA-256 校验。

新能力服务器的预览建议流程：

1. 按歌曲 ID 解析资源清单，选取 `resources.audio`。实际请求前准备好清单，不在音频实时回调里访问 API。
2. 使用 `headUrl` 发 HEAD，核对文件总长度并取得对象实际 ETag。它是对象响应头的值，不是清单中的 SHA-256。
3. 使用 `url` 发单段 `Range: bytes=start-end`。若使用条件请求保护已观察到的对象，使用实际 ETag 的 `If-Match`；不要把旧后端的 `If-Range: "<audioHash>"` 原样套到 S3 请求。S3 文档定义了 `If-Match` 和单段 Range；条件不匹配返回 412。[AWS GetObject 接口](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)
4. 必须检查 206、Content-Range 的起点终点总长度，以及实际返回字节数。范围请求若返回 200，停止此次预览，不偷偷接收整首文件。
5. 将每次预览会话固定到清单中的音频哈希和长度。分块缓存也必须关联该音频哈希；不能只用歌曲 ID 和字节偏移作为跨歌曲更新的缓存键。
6. 遇到 403 刷新链接后，只有哈希和大小仍相同才可以保留已有分块继续读取；内容改变、412 或 ETag 不一致时，丢弃旧预览会话并重新准备，或停止播放并提示。
7. `DEMOSTART` 和试听时长继续交给原解码/播放逻辑处理，不能把秒数直接当作 MP3/OGG 字节偏移。

旧服务器保持原有预览分支。当前本地预览实现把 `chart.AudioHash` 包在双引号中作为 ETag，仅适用于原后端响应约定；新外部通道必须与它分开。

当前测试环境尚未配置 CloudFront。未来可以继续使用清单返回的 GET 和 HEAD URL；届时还需要对实际 CDN 的 Range、条件请求和响应头执行同一套验收，不能仅凭更换域名宣称已通过。

## WebGL 兼容

原生客户端直接 HTTP 下载不需要浏览器 CORS。当前桶未为此次接入配置浏览器跨域，不能把原生平台验证结果当作 WebGL 已可用。若项目需要 WebGL 支持，应单独说明所需的后端和存储 CORS 配置，并保留既有可用路径；不要由游戏端 agent 擅自公开桶或修改 AWS 权限。WebGL 的独立外部请求同样不得发送游戏 token。

## 推荐修改位置

游戏仓库根目录：

```text
<OurTaikoPlayerUnity 仓库根目录>
```

以下均相对该根目录。先读取该仓库自己的 AGENTS.md 和实际工作区状态，再按现有工程组织实现。

| 文件 | 需要处理的部分 |
| --- | --- |
| `Assets/OurTaiko/Runtime/Online/FanmadeClient.cs` | ConnectAsync 能力解析；PrepareAsync 的清单、缓存、校验和更新竞态处理 |
| `Assets/OurTaiko/Runtime/Online/FanmadeEndpoint.cs` | 保存服务器能力；保留原 API 通道，增加或配合独立的绝对 URL 下载通道 |
| `Assets/OurTaiko/Runtime/Online/FanmadeModels.cs` 或新增同目录文件 | 资源清单模型及字段校验 |
| `Assets/OurTaiko/Runtime/Online/FanmadeEndpoint.Preview.cs` | 外部 Range 请求，独立鉴权行为及实际 ETag 处理 |
| `Assets/OurTaiko/Runtime/Online/PreviewRangeStream.cs` | 改为使用解析后的音频资源描述，分块缓存与哈希绑定 |
| `Assets/OurTaiko/Runtime/Audio/OnlinePreviewDecoder.cs` | 预览资源准备与解码的交接，保持取消和后台执行 |
| `Assets/OurTaiko/Runtime/Online/FanmadeEndpoint.Web.cs` | 如涉及 WebGL，明确单独通道及 CORS 边界 |
| `Assets/OurTaiko/Tests/Shared/FanmadeFixture.cs` | 模拟 API 与独立资源主机、签名过期和资源变化 |
| `Assets/OurTaiko/Tests/EditMode/FanmadeClientTests.cs` | 能力、整首下载、缓存、错误和队列兼容 |
| `Assets/OurTaiko/Tests/EditMode/OnlinePreviewTests.cs` | Range、真实 ETag、分块和链接刷新 |
| `Assets/OurTaiko/Tests/PlayMode/OnlinePreviewFlowTests.cs` | 切歌取消、预览流量及播放流程 |
| `Assets/OurTaiko/Tests/PlayMode/ServerLoginFlowTests.cs` | 登录保持可用，成绩上传使用无版本协议 |

新增 Unity 脚本及资源按仓库约定生成对应 `.meta`。无需为了此次下载适配修改歌曲选择 UI、音频引擎或评分逻辑。

## 验收要求

用两个独立测试服务模拟 API 与资源源站，检查实际收到的 URL、HTTP 方法和请求头；仅让 API fixture 返回字节无法证明完成直连。

- 新服务器下载时，API 只收到详情和 `/charts/{chartId}/resources` 请求，整首文件和预览字节从独立资源主机获取。
- 外部请求没有游戏 Bearer、Cookie、幂等键；签名参数完整保留，HEAD 使用 `headUrl`。
- 资源请求路径不包含歌曲版本 ID。清单缺必需资源、错误歌曲 ID、非法哈希/大小/URL 时明确失败。
- 首次下载成功；第二次仍查询当前清单，但哈希相同则不发送文件 GET。只更换音频时仅音频重下。
- 本地文件损坏，即使缓存索引声称哈希匹配，也会被检测并重新下载。
- 缺少扩展名的对象 URL 能正确保存和播放 MP3、OGG、TJA。
- 过期或首次 403 可以刷新后恢复；连续 403 有界失败，不触发登录或无限循环。
- 403 刷新过程中歌曲变更、详情与清单不一致、外部 404、部分下载、超限响应、哈希不符都有测试；失败不会覆盖已有完整缓存。
- 原生预览仅下载需要的 Range，正确处理实际 ETag、206、412、错误 Content-Range、意外 200、链接刷新和内容变化。切歌后旧请求停止，旧分块不混入新预览。
- 没有 `resourceDownloadVersion` 的旧服务器仍可下载和游玩。旧服务器使用独立兼容分支；新服务器验证 songIdOnly、无版本成绩上传、ClearStatus、回放、幂等重试、SCORE_REMOVED、旧待上传队列迁移以及在线成绩删除同步。
- 连接上述 HTTPS 测试服进行实际进歌、试听、退出再进歌、缓存命中、取消和成绩上传验证。使用用户正常登录流程，不读取或硬编码玩家密码。
- 报告分别列出自动测试、Unity 编译/构建、真实网络、实机或 Editor 交互验证；未执行的部分明确写出。当前没有 CloudFront，不声称验证过 CloudFront。

## 实施交付

请游戏端 agent 按本文完成实现和必要测试，提供改动摘要、测试结果及未验证的平台边界。不要只做方案或仅修改服务器地址。按仓库惯例保存代码；任何需要后端变更的阻塞单独列出，不擅自修改生产环境。

后端契约源码为 `internal/httpapi/resource_links.go`，服务端部署说明见同目录的 [S3_STORAGE.md](S3_STORAGE.md)。本接入契约以歌曲 ID 资源接口为准，替代早期按歌曲版本寻址和上传成绩的说明。

## 歌曲名称与全部翻译

后端完整返回所有已有翻译，不决定显示语言。英文与其他语言统一放在字典中：

```json
{
  "title": "TJA 原始标题",
  "subtitle": "TJA 原始副标题",
  "titleTranslations": {"en": "English name", "ja": "日本語名", "zh": "中文名", "ko": "한국어 이름"},
  "subtitleTranslations": {"en": "English subtitle", "zh": "中文副标题"}
}
```

游戏 DTO 应将两个 Translations 字段解析为语言键到字符串的字典，完整保留。英文也从 en 读取，不能继续把 title 当成编辑后的英文名。客户端可采用“选定语言 → en → 原文”的回退顺序；标题和副标题分别选择，缺失、空或纯空白值继续回退。中文语言码 zh-Hans 对应字典 zh。副标题 --/++ 标记仅在显示时去除。

切换语言只重新选择本地字典，不要求重新请求接口，也不要将显示字符串覆盖进原始数据。旧服务器没有字典时可回退原文。翻译编辑不会改变资源 SHA-256，因此刷新曲库/详情时应更新文本元数据，不能因音频或 TJA 哈希未变而跳过翻译更新；文件下载缓存仍按各资源哈希判断。
