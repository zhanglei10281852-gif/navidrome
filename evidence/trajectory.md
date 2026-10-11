# 让上传封面在文件、实体引用与解析状态间同成败提交

> **Workspace:** F:\swe\098823\project-01

---

**User**

用户替换艺术家、电台或播放列表封面时，如果上传流中途失败或实体保存返回错误，当前请求可能报错而旧图已经消失，甚至留下能被读取的半张图片。请让这些现有入口共享一次可恢复的封面提交：新内容完整写入且实体图片引用成功持久化之前，客户端和 artwork 解析都继续看到旧封面；提交成功后只发布完整新图，并让后续解析与读取收敛到同一引用。删除封面也必须避免数据库仍指向已删除文件。文件写入、数据库更新或解析刷新分别失败时应执行与所处阶段相符的恢复，候选文件和不再被引用的旧文件最终得到清理，重试或并发替换不能误删后来成功的图片。首次上传、现有大小与格式校验、没有自定义封面时的解析来源及成功请求的响应语义保持不变。

**Assistant**

我先了解一下项目结构和相关代码，再制定修改方案。

toolName: view_folder

status: failed

f:\swe\098823\project-01

toolName: file_search

status: success

toolName: Shell

status: success

这是一个 Go 项目（看起来是 gonic 音乐服务器）。让我搜索封面上传相关的代码。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

核心代码在 `core/artwork/`。让我读取关键文件。

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\image_upload.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\artists.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\radios.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\native_api.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\model\image.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\model\artwork.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\playlists.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

现在读取 artwork 解析相关代码，了解上传图片如何被解析和读取。

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\resolve.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\processor.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader_test.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\housekeeping.go

让我查看图片读取/服务路径，以及 e2e 测试中对上传流程的预期。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\artwork.go

让我查看 artwork 包的其余文件、wire 装配和 e2e 测试，理解刷新、清理机制。

toolName: view_folder

status: success

f:\swe\098823\project-01\core\artwork

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\wire_providers.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\prune.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\image_store.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\e2e\artist_test.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists_test.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\worker.go

toolName: search_by_regex

status: success

让我查看 cmd/root.go 中 artwork 相关启动流程和调度，以及 tests 里的 mock 仓储。

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\cmd\root.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\cmd\root.go

现在查看各仓储接口（artist/radio/playlist）以及 tests 中的 mock 实现，确认清理扫描如何获取被引用的文件名集合。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\model\artist.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\model\radio.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\model\playlist.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\tests\mock_data_store.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\tests\mock_artist_repo.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\tests\mock_radio_repository.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\tests\mock_playlist_repo.go

toolName: todo_write

status: success

Todos updated: 6 items

toolName: search_by_regex

status: success

查看 mock artwork 仓储，然后开始实现。

toolName: search_by_regex

status: success

现在检查潜在的包循环依赖，然后开始实现。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

toolName: search_by_regex

status: success

没有生产代码的循环依赖。现在开始实现。首先重写 [uploader.go](file:///f:/swe/098823/project-01/core/artwork/uploader.go)：

toolName: todo_write

status: success

Todos updated: 6 items

toolName: Write

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

现在把最终清理扫描追加到 uploader.go：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

接入 prune 周期任务：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\prune.go

changes: undefined

现在改造三个入口。先是播放列表服务：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

changes: undefined

更新播放列表的 SetImage/RemoveImage/Delete：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

changes: undefined

现在改造 artist 和 radio 入口：

toolName: Write

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\artists.go

changes: undefined

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

先确认当前基线能否构建（特别是原有代码里的 `new(time.Now())`）：

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: Shell

status: success

`new(time.Now())` 在当前 Go 工具链下可以编译（等价于取值地址）。继续更新 radios.go：

toolName: Write

status: success

filePath: f:\swe\098823\project-01\server\nativeapi\radios.go

changes: undefined

构建整个项目检查编译：

toolName: Shell

status: success

main 包需要构建标签。单独构建相关包：

toolName: Shell

status: success

核心包编译通过。现在重写 uploader 测试，覆盖新的提交/恢复语义：

toolName: Write

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader_test.go

changes: undefined

修正测试中的笔误：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader_test.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader_test.go

changes: undefined

运行 artwork 包测试：

toolName: Shell

status: success

需要消除 `fileMtime` 重名，并查看 MockArtworkRepo 的错误开关：

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\tests\mock_artwork_repo.go

修复重名问题——复用 resolve.go 里已有的 `mtimeOf`（返回 int64）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

更新测试中的 mock 错误字段：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader_test.go

changes: undefined

toolName: Shell

status: success

artwork 包测试通过。运行播放列表与 nativeapi 测试，并用 `-race` 验证并发：

toolName: Shell

status: success

nativeapi 测试需要 FTS5 构建标签。查一下 Makefile 的测试标签：

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: Shell

status: success

toolName: Shell

status: success

运行其余受影响包的测试：

toolName: Shell

status: success

运行 artwork/subsonic 的 e2e 测试套件验证端到端解析与服务：

toolName: Shell

status: success

全部通过。简化 playlists 中一个小问题（多余的 ctx 参数），然后跑 vet：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\playlists\playlists.go

changes: undefined

toolName: Shell

status: success

再优化两点：给 stage 传 ctx 用于日志；即使状态行清除失败也尝试入队（worker 的 upsert 同样会把状态刷新到新引用，加快收敛）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

让 `discard` 也接收 ctx 以保证日志上下文一致：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

changes: undefined

补充一个测试：状态清除失败时仍然入队（驱动收敛）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader_test.go

changes: undefined

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\tests\mock_artwork_queue_repo.go

toolName: Shell

status: success

toolName: Shell

status: success

最终通读一遍成品文件：

toolName: view_files

status: success

filePath: f:\swe\098823\project-01\core\artwork\uploader.go

检查 prune 调度常量确实存在（保证“最终清理”会周期执行）：

toolName: search_by_regex

status: success

每日 prune + 1h grace 窗口确认。做最后的格式检查与带标签全量构建：

toolName: Shell

status: success

toolName: Shell

status: success

整个文件都在 diff 中，疑似行尾符（CRLF）问题。检查一下：

toolName: Shell

status: success

prune.go 和 playlists.go 被转成了 CRLF（仓库规范为 LF）。转回 LF：

toolName: Shell

status: success

格式已全部合规。确认 diff 正常并重新跑全部受影响测试：

toolName: Shell

status: success

toolName: Shell

status: success

diff 精准无杂散改动。最后一轮完整测试：

toolName: Shell

status: success

toolName: Shell

status: success

全部通过。检查 IDE 诊断：

toolName: GetDiagnostics

status: success

toolName: todo_write

status: success

Todos updated: 6 items

已完成。三个封面入口现在共享同一个可恢复的封面提交，旧图在新图完整落盘且数据库引用持久化之前不会被破坏，失败按阶段恢复，垃圾文件最终清理。

## 改动说明

**核心：统一的可恢复提交** — [uploader.go](file:///f:/swe/098823/project-01/core/artwork/uploader.go)

- 新接口以 `CoverCommit{Current, Persist}` 回调驱动实体行，文件与 artwork 状态的顺序全部由 artwork 包统一掌控：
  1. **暂存**：上传字节完整写入同目录唯一候选文件 `.<最终名>.tmp<随机>`（非图片后缀，解析永远不会读到），关闭后才继续；流中途失败只删候选，旧图原封不动。
  2. **发布 + 持久化的双阶段排序**：
     - 新旧路径不同（改名/换格式）→ 先原子 `rename` 发布（旧文件未动，客户端与解析继续看旧封面），再写 DB 引用；
     - 路径相同（同名同格式）→ 先持久化引用（引用仍指向完整旧图），再原子替换，杜绝半张图被旧 mtime 校验提前放出。
  3. **刷新解析**：清除 `item_artwork` 状态并入队 Bump；清状态失败仍尝试入队（worker 的 upsert 会把状态收敛到新引用）。
  4. **回收旧文件**：仅在状态已清除、DB 实时引用不等于旧路径、且旧文件 mtime 未变时才删除。
- **删除封面**：先持久化空引用 → 刷状态 → 最后删文件，数据库不可能再指向已删文件；测试验证了 Persist 执行时文件仍存在。
- **并发**：按 `实体类型+ID` 的进程内互斥锁串行化提交；回滚/回收带“DB 引用复核 + mtime 指纹”双保险，后来成功提交的图片不会被重试或并发请求误删。
- **兜底清理**：`sweepUploadedImages` 随每日 `artwork prune` 调度执行（[prune.go](file:///f:/swe/098823/project-01/core/artwork/prune.go#L62-L71)），复用 1 小时 grace 窗口，清理崩溃遗留候选和不再被任何实体引用的旧封面；即时删除失败（如 Windows 文件被占用）也由它兜底。

**入口改造**

- [artists.go](file:///f:/swe/098823/project-01/server/nativeapi/artists.go)、[radios.go](file:///f:/swe/098823/project-01/server/nativeapi/radios.go)：改为传入 `Current/Persist` 钩子，404/授权语义和列更新方式不变。
- [playlists.go](file:///f:/swe/098823/project-01/core/playlists/playlists.go#L332-L379)：`SetImage/RemoveImage` 共用同一提交（Jellyfin 图片接口自动受益）；`Delete` 播放列表时也调整为先删实体行再删封面文件，避免删除失败留下悬空引用。

**保持不变**：首次上传的文件名（`id_name.ext`）与目录、大小/格式校验（`MaxBytesReader` + `DecodeConfig`）、无自定义封面时的解析来源链、成功响应 `{"status":"ok"}`。

**测试**：[uploader_test.go](file:///f:/swe/098823/project-01/core/artwork/uploader_test.go) 覆盖流中断、持久化失败回滚、同名替换失败一致性、后来提交防误删、删除顺序、状态清除失败恢复、4 路并发替换（`-race` 下验证无混合字节、无候选残留）及扫描清理；`core/artwork`（含 e2e）、`core/playlists`、`server/nativeapi`、jellyfin/subsonic e2e、`cmd`、`persistence` 全部通过。

如果希望这套“暂存→原子发布→引用持久化→刷新→回收”的封面提交流程沉淀为可复用技能，我可以为该工作区创建一个。
