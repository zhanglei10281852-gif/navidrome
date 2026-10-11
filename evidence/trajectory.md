# 转码配置变更后的缓存代际与播放器引用收敛

> **Workspace:** F:\swe\098823\project-03

---

**User**

客户端在管理员改过 Opus、MP3 等转码配置后继续收到修改前生成的音频，原因是落盘缓存只按媒体和输出参数区分，而播放器引用、实际命令和进行中的转码没有共享配置代际；删除仍被播放器使用的配置还会把错误拖到后续播放。配置更新或删除应在持久记录、播放器引用、决策快照和缓存命名空间之间形成一个可恢复的提交边界：每次播放从选择 profile 到启动命令始终使用同一个已提交快照，只有匹配该快照的完整缓存能够复用。切换时已经运行的旧转码可以按原快照完成或被一致取消，但迟到结果不能进入新代缓存；数据库提交失败则继续用旧配置和旧缓存服务。删除被引用的 profile 必须在管理操作时确定地拒绝，或完成明确回退并同步引用，不能留下随机播放错误；重启只恢复最后已提交代际的完整缓存。未修改 profile 的缓存命中、直接播放、未配置转码的播放器以及 Subsonic、Jellyfin、分享和下载的既有一致行为保持不变。

**Assistant**

toolName: view_folder

status: failed

f:\swe\098823\project-03

toolName: file_search

status: success

toolName: Shell

status: success

这是一个 Navidrome 代码库（Go 音乐服务器）。让我先深入了解转码相关的代码结构。

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\types.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\token.go

toolName: file_search

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\legacy_client.go

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\spread_fs.go

现在查看数据模型和持久化层中转码配置与播放器的部分。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\model\transcoding.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\model\player.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\persistence\transcoding_repository.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\model\request\request.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\subsonic\stream.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\subsonic\transcode.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\subsonic\middlewares.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\jellyfin\middlewares.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\players.go

toolName: search_by_regex

status: success

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\consts\consts.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\db\migrations\20200310181627_add_transcoding_and_player_tables.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

让我查看所有 `NewStream` 调用方、Jellyfin/public 的流式代码以及 archiver。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\jellyfin\stream.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\public\handle_streams.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\archiver.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\tests\mock_transcoding_repo.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\tests\mock_player_repo.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\nativeapi\native_api.go

toolName: Shell

status: success

toolName: Shell

status: success

toolName: view_files

status: success

filePath: C:\Users\czxy4\go\pkg\mod\github.com\deluan\rest@v0.0.0-20211102003136-6260bc399cbf\errors.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\persistence\sql_base_repository.go

toolName: search_by_regex

status: success

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: C:\Users\czxy4\go\pkg\mod\github.com\djherbis\fscache@v0.10.2-0.20260829235704-6d85d5878c22\fscache.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer_test.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\spread_fs_test.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\simple_cache.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches_test.go

现在我对架构有了完整理解。让我再确认几个事务处理和错误处理的细节。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\db\migrations\20240802044339_player_use_user_id_over_username.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\cmd\wire_gen.go

让我查看现有的相关测试和 e2e 转码测试，确认需要保持的行为契约。

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\persistence\transcoding_repository_test.go

toolName: file_search

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\players_test.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

我已经完整理解了系统。现在制定任务清单并开始实施。

toolName: todo_write

status: success

Todos updated: 7 items

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\limiter.go

现在开始实现。首先创建代际核心文件：

toolName: Write

status: success

filePath: f:\swe\098823\project-03\core\stream\generation.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\types.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\types.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\types.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

changes: undefined

现在更新 legacy_client.go，让决策产生的 Request 携带代际：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\legacy_client.go

changes: undefined

现在更新 token.go，在 JWT 快照中加入并校验代际：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\token.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\token.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\token.go

changes: undefined

让我检查 token_test.go 看是否有手工构造的 token 需要适配：

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\token_test.go

任务 2 完成。现在更新 media_streamer.go，让缓存键和 ffmpeg 命令都绑定代际：

toolName: todo_write

status: success

Todos updated: 7 items

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

changes: undefined

现在更新缓存 producer，使 ffmpeg 启动严格校验代际：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

现在实现 cache 包的代际载荷与重启收养策略。先确认 fscache 构造时何时调用 Reload：

toolName: view_files

status: success

filePath: C:\Users\czxy4\go\pkg\mod\github.com\djherbis\fscache@v0.10.2-0.20260829235704-6d85d5878c22\fscache.go

现在修改 `file_caches.go`，增加 Option、完成载荷与收养策略：

toolName: edit_file_search_replace

status: failed

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

changes: undefined

分小块编辑。先在 `NewFileCache` 前插入 Option 类型：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

changes: undefined

现在更新 NewFileCache 签名和 fileCache 结构体：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches.go

changes: undefined

现在更新 spread_fs.go，marker 支持代际载荷，Reload 按策略收养：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\spread_fs.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\spread_fs.go

changes: undefined

现在完成 `GetTranscodingCache` 的代际策略接线：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

changes: undefined

任务 3、4 完成。现在做管理操作的提交边界：

toolName: todo_write

status: success

Todos updated: 7 items

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\model\transcoding.go

changes: undefined

现在实现持久层的 `All`、`CountPlayers` 和确定性删除拒绝：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\persistence\transcoding_repository.go

changes: undefined

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\persistence\transcoding_repository.go

changes: undefined

现在加固 `players.go`，自愈历史遗留的悬空转码引用：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\players.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\players.go

changes: undefined

任务 6：更新 mock 仓库和 wire 接线。

toolName: Write

status: success

filePath: f:\swe\098823\project-03\tests\mock_transcoding_repo.go

changes: undefined

更新 wire_gen.go 的三处调用：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\cmd\wire_gen.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\cmd\wire_gen.go

changes: undefined

先编译验证目前的改动：

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\token.go

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\token.go

changes: undefined

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: Shell

status: success

构建通过。运行 gofmt 并检查 vet：

toolName: Shell

status: success

构建与格式化都通过。现在补充测试。先看测试辅助设施：

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\tests\mock_ffmpeg.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches_test.go

现在编写代际单元测试：

toolName: Write

status: success

filePath: f:\swe\098823\project-03\core\stream\generation_test.go

changes: undefined

toolName: search_by_regex

status: success

向 token_test.go 增加代际快照回放测试：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\token_test.go

changes: undefined

向 media_streamer_test.go 添加代际命名空间与陈旧快照测试：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer_test.go

changes: undefined

修正测试中一个多余的辅助调用——让 newStack 同时返回 ds：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer_test.go

changes: undefined

现在为 spread FS 的代际收养策略添加测试：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\spread_fs_test.go

changes: undefined

现在在 file_caches_test.go 增加端到端的重启代际收养测试：

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches_test.go

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches_test.go

changes: undefined

现在为持久层删除拒绝添加测试。先看持久化测试套件的辅助方法：

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\persistence\transcoding_repository_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\persistence\transcoding_repository_test.go

changes: undefined

toolName: search_by_regex

status: success

为 players.go 的悬空引用自愈添加测试：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\players_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\players_test.go

changes: undefined

格式化并编译所有测试：

toolName: Shell

status: success

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\utils\cache\file_caches_test.go

changes: undefined

toolName: Shell

status: success

编译通过。运行 cache 和 stream 包测试：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

核心测试全部通过。再跑涉及流式链路的 server 包测试：

toolName: Shell

status: success

一个既有测试断言整个 Request 结构，代际现在被正确填充，更新断言：

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\server\public\handle_streams_test.go

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\server\public\handle_streams_test.go

changes: undefined

toolName: Shell

status: success

运行 subsonic/jellyfin e2e 测试（全链路决策→token→流式）：

toolName: Shell

status: success

全链路 e2e 通过。运行全量构建和测试：

toolName: Shell

status: success

toolName: Shell

status: success

全部测试通过。让我确认管理端对 `ValidationError` 的 UI 反馈路径，确保删除拒绝能明确展示给管理员：

toolName: search_by_regex

status: success

让我确认所有 NewFileCache 调用方（图片缓存等）与新签名兼容：

toolName: search_by_regex

status: success

让我复查最终的 NewStream 完整流程：

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\media_streamer.go

检查 decider 内部测试是否直接调用了改签名的 `computeTranscodedStream`：

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\decider_test.go

在 decider 测试中补充代际断言：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider_test.go

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098823\project-03\core\stream\decider_test.go

toolName: Shell

status: success

任务 7 测试基本完成。做最终全模块 vet 检查并查看改动概览：

toolName: todo_write

status: failed

Todos updated: 7 items

toolName: todo_write

status: success

Todos updated: 7 items

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: view_files

status: success

filePath: C:\Users\czxy4\AppData\Local\Temp\trae-agent-toolhost\jobs\job-446b29f491b440d19b0e2371dccb7a22\output.log

整个仓库都显示 modified，可能是环境预存的行尾差异。验证一下未触碰文件的 diff 性质：

toolName: Shell

status: success

toolName: Shell

status: success

内容差异正常（其余是环境预存的 CRLF 基线）。运行 cmd 包测试确认 wire 相关改动：

toolName: Shell

status: success

用 `-race` 跑核心包验证并发安全：

toolName: Shell

status: success

toolName: search_by_regex

status: success

移除已无调用方的旧 `LookupTranscodeCommand`，避免两套解析路径：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-03\core\stream\decider.go

changes: undefined

toolName: Shell

status: success

toolName: todo_write

status: success

Todos updated: 7 items

toolName: GetDiagnostics

status: success

实现完成，所有测试（含 e2e 与 `-race`）通过。下面是改动总结。

## 问题根因

落盘缓存键 [streamJob.Key()](file:///f:/swe/098823/project-03/core/stream/media_streamer.go#L64-L66) 只含媒体和输出参数，不含 ffmpeg 命令；命令在缓存 MISS 时才二次查库（[原 LookupTranscodeCommand](file:///f:/swe/098823/project-03/core/stream/decider.go)），决策、Token、播放器引用、缓存文件之间没有共享同一个配置快照；且 [transcoding_repository.Delete](file:///f:/swe/098823/project-03/persistence/transcoding_repository.go#L118-L143) 不检查 player 引用（现表无外键），删除后播放行为随机失败。

## 解决方案：内容寻址的配置代际

新增 [core/stream/generation.go](file:///f:/swe/098823/project-03/core/stream/generation.go)：代际 = `sha1(targetFormat, command, defaultBitRate)`。命令不变则代际不变（缓存命中不变），命令一变代际立即变；DB 未提交成功就没有新代际，天然满足“提交失败继续用旧配置、旧缓存”。

**统一快照贯穿整条播放链路：**
- 决策：[decider.go](file:///f:/swe/098823/project-03/core/stream/decider.go#L93-L106) 解析提交行（DB 行→内置默认回退）并把代际写入 `TranscodeDecision.TargetGeneration`；[legacy_client.go](file:///f:/swe/098823/project-03/core/stream/legacy_client.go#L123-L138) 的 Subsonic/Jellyfin/分享/下载解析路径把它带入 `Request.Generation`。
- Token：[token.go](file:///f:/swe/098823/project-03/core/stream/token.go) 新增签名 claim `g`；[getTranscodeStream](file:///f:/swe/098823/project-03/core/stream/token.go#L143-L167) 回放时重放代际，配置已变/配置删除/旧版无代际 Token 一律 `410 Gone`，客户端重新协商。
- 启动命令：[media_streamer.go](file:///f:/swe/098823/project-03/core/stream/media_streamer.go#L106-L138) 在建流前和 ffmpeg 启动 producer 内双重校验代际，不匹配返回 `ErrTranscodeConfigChanged`——绝不把新命令的迟到结果写进旧代际。
- 缓存命名空间：缓存键追加代际；正在运行的旧转码继续写入旧键（可按原快照完成），新请求只哈希到新键，物理隔离。

**重启只恢复最后已提交代际的完整缓存**（[utils/cache](file:///f:/swe/098823/project-03/utils/cache/file_caches.go)）：complete marker 原子写入代际载荷（[spread_fs.go](file:///f:/swe/098823/project-03/utils/cache/spread_fs.go#L183-L210)）；启动扫描时通过 `WithAdoptionPolicy` 用 [CurrentTranscodingGenerations](file:///f:/swe/098823/project-03/core/stream/generation.go#L84-L120)（DB 全部行+内置默认的有效代际集合）裁决：命中→收养，不属于当前代际→连数据带 marker 清除，存储暂时不可读→保留磁盘但不收养（绝不上线陈旧音频）。图片缓存等未装策略的调用方行为完全不变。

**删除的确定性提交边界**（[transcoding_repository.go](file:///f:/swe/098823/project-03/persistence/transcoding_repository.go#L118-L143)）：批量删除先两阶段检查 player 引用，被引用即返回 `rest.ValidationError`（管理 API 400），整批原子不删；新增 `All()`（含 Command，不经 REST 脱敏）和 `CountPlayers()`。同时 [players.go](file:///f:/swe/098823/project-03/core/players.go#L88-L107) 对历史遗留的悬空引用做确定性回退：清除 `transcoding_id` 并同步落库，不再把错误随机带到后续播放。

接线更新：[model.TranscodingRepository](file:///f:/swe/098823/project-03/model/transcoding.go#L19-L33)、[wire_gen.go](file:///f:/swe/098823/project-03/cmd/wire_gen.go#L96) 三处、[MockTranscodingRepo](file:///f:/swe/098823/project-03/tests/mock_transcoding_repo.go)（支持自定义行）。无需数据库迁移。

## 验证

新增测试覆盖：代际稳定性/DB 与内置回退/当前代际集合、Token 代际往返与 410、改命令后旧缓存不可命中且新输出正确、未改配置命中不变、删除被引用拒绝且整批回退、悬空引用自愈、marker 载荷与重启 adopt/prune/retain。全量 `go build`、`go vet`、相关包与 subsonic/jellyfin e2e 测试通过，核心包 `-race` 通过。

一个行为说明：升级后首次重启时，升级前产生的、无法证明代际的旧转码缓存会被清理一次（预热），这是“只恢复当前已提交代际”的必然代价；直连播放、未配置转码的播放器、图片缓存及所有现有 API 行为不受影响。
