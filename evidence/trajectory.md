# 插件设置热切换失败时保留旧运行实例

> **Workspace:** F:\swe\098823\project-02

---

**User**

一次插件权限保存不该让原本正常工作的歌词、scrobble 或调度服务立即中断；然而新配置对应的 Wasm 无法编译、host service 创建失败或初始化报错时，当前流程已经卸载旧实例并把不可运行的设置写入数据库。把这条设置更新改成候选提交：先用新配置和权限完整建立候选实例、全部 host services 和初始化结果，验证通过后再共同替换持久设置与活动实例；候选阶段或数据库提交失败时只清理候选资源，旧配置和旧实例继续提供原能力，管理接口返回可诊断错误。成功切换只能发布一个新活动实例，随后完整回收旧资源；同一插件的并发保存不得让迟到请求覆盖较新的成功结果或关闭错误代际。已禁用插件仍只保存设置而不启动，移除必需权限触发的正常禁用语义继续有效，服务重启只加载最后一次成功提交的配置。

**Assistant**

toolName: view_folder

status: failed

f:\swe\098823\project-02

toolName: file_search

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: view_files

status: success

filePath: C:\Users\czxy4\AppData\Local\Temp\trae-agent-toolhost\jobs\job-6cc87661e45a4edb8d32afc4c3efc71c\output.log

这是一个较大的 Go 插件系统。让我先阅读核心管理文件，理解当前的保存/重载流程。

toolName: todo_write

status: success

Todos updated: 8 items

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_loader.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_plugin.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\capability_lifecycle.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_sync.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_watcher.go

toolName: search_by_regex

status: success

toolName: file_search

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\server\nativeapi\plugin.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\model\plugin.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_test.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\persistence\plugin_repository.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\plugins_suite_test.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_loader_load_test.go

toolName: search_by_regex

status: success

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_readonly_test.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\tests\mock_plugin_repo.go

现在查看 init 相关测试与调用点、以及 loader 测试和 cmd 入口，确认初始化失败的现有语义。

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\capabilities\lifecycle.yaml

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_call.go

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_loader_test.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\cmd\plugin.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\tests\mock_plugin_manager.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_watcher_test.go

toolName: search_by_regex

status: success

让我确认 host services 的 Close 是否能完整回收 init 期间注册的资源（调度任务、task queue、websocket 等）。

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\host_scheduler.go

toolName: search_by_regex

status: success

设计已清晰。更新任务状态并开始实现。先查看 testdata 插件是否有 init 导出，便于后续测试。

toolName: todo_write

status: success

Todos updated: 8 items

toolName: search_by_regex

status: success

查看 MockDataStore 结构，便于编写提交失败测试。

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\tests\mock_data_store.go

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\testdata\test-metadata-agent\manifest.json

开始实现前，确认 CLI 下 manager 的 transient/init 路径，避免改变 CLI enable 行为。

toolName: search_by_regex

status: success

现在开始实现。先给 `plugin` 加 generation 字段，并把 init 改为返回错误。

toolName: todo_write

status: success

Todos updated: 8 items

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager_plugin.go

changes: undefined

toolName: Write

status: success

filePath: f:\swe\098823\project-02\plugins\capability_lifecycle.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\capabilities\lifecycle.yaml

changes: undefined

现在重构 [manager_loader.go](file:///f:/swe/098823/project-02/plugins/manager_loader.go)：拆出候选构建、发布、原子提交。

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager_loader.go

changes: undefined

现在替换核心的加载函数：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager_loader.go

changes: undefined

现在改 [manager.go](file:///f:/swe/098823/project-02/plugins/manager.go)：加锁/代际字段，重写三个变更入口。

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

现在重写 `EnablePlugin`、`DisablePlugin`：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

现在重写核心的 `updatePluginSettings` 与 `unloadPlugin`：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

先编译检查：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: failed

toolName: Shell

status: success

main 包需要构建标签（与本次改动无关）。查看 Makefile 中的标签：

toolName: search_by_regex

status: success

toolName: Shell

status: success

toolName: Shell

status: success

编译通过。现在编写候选提交的测试。先确认 test-users 插件的清单声明确实包含 users 权限：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\testdata\test-users\manifest.json

现在编写候选提交的测试文件：

toolName: Write

status: success

filePath: f:\swe\098823\project-02\plugins\manager_candidate_test.go

changes: undefined

修正 disabled 用例里一个多余的切片写法：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager_candidate_test.go

changes: undefined

运行 plugins 包测试：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

奇怪——损坏文件后保存竟然成功了。检查 `openPackage` 是否有缓存：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\package.go

查看失败的确切行号：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_candidate_test.go

发现根因：mock 仓储的 `Get` 返回共享指针，`updateFn` 直接改到了"库内"对象——真实 DB 仓储每次返回新行，但接口未声明该契约。在设置路径上做防御性拷贝，使正确性不依赖仓储实现：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

不需要给 model 加方法，改为内联浅拷贝（Plugin 全是值字段）：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

编辑出现了拼接错误，修正：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

重新运行该测试套件：

toolName: Shell

status: success

toolName: Shell

status: success

7 个新用例全部通过。运行整个 plugins 包测试（含竞态检测）：

toolName: Shell

status: success

趁测试运行，检查一个语义细节：CLI 的 transient 加载路径下 init 应保持原有门控。让 Enable/settings 的 init 调用与原 `loadPluginWithConfig` 门控一致：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

toolName: view_files

status: success

filePath: C:\Users\czxy4\AppData\Local\Temp\trae-agent-toolhost\jobs\job-06160e8a7159451c9fd3c4d78348cd89\output.log

趁后台测试运行，检查现有测试是否依赖旧的"重载失败即禁用并落库坏配置"行为：

toolName: search_by_regex

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\host_users_test.go

toolName: view_files

status: success

filePath: C:\Users\czxy4\AppData\Local\Temp\trae-agent-toolhost\jobs\job-06160e8a7159451c9fd3c4d78348cd89\output.log

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\core\scrobbler\play_tracker_test.go

等待竞态测试时，复查改后的 [manager.go](file:///f:/swe/098823/project-02/plugins/manager.go) 关键区段：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

toolName: view_files

status: success

filePath: C:\Users\czxy4\AppData\Local\Temp\trae-agent-toolhost\jobs\job-06160e8a7159451c9fd3c4d78348cd89\output.log

竞态测试超时挂起了。读取完整日志定位是哪个 spec 卡住、是否有死锁或 data race：

toolName: Shell

status: success

之前管道把日志截断了。先跑非 race 的完整包测试确认功能正确，同时后台重跑 race（保留完整输出）：

toolName: Shell

status: success

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\host_storage_test.go

toolName: Shell

status: success

test-storage-plugin.ndp 实际存在。查看日志中该失败的完整错误信息：

toolName: Shell

status: success

缺失的是 `test-storage-plugin-2.ndp`。查看 testdata 构建脚本如何生成它：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\testdata\Makefile

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\testdata_builder_test.go

toolName: Shell

status: success

toolName: search_by_regex

status: success

`test-storage-plugin-2` 本应是指向 `test-storage-plugin` 的符号链接（Windows 检出成了 19 字节文本），与本次改动无关。确认 .ndp 是否被 git 忽略，然后本地补建该夹具以完成验证：

toolName: Shell

status: success

两个 ndp 都是被 git 忽略的构建产物，复制仅用于本地验证。重跑完整包测试：

toolName: Shell

status: success

非 race 全包通过。后台重跑 -race（延长超时、保留完整输出），同时运行其他相关包测试：

toolName: Shell

status: success

toolName: Shell

status: success

相关包测试通过。趁 race 运行，审查完整改动：

toolName: Shell

status: success

toolName: search_by_regex

status: success

更新 README 中 init 契约的描述：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\README.md

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\README.md

changes: undefined

toolName: Shell

status: success

趁 race 仍在编译，复查 manager_loader.go 开头与 loadEnabledPlugins 是否和新函数配合正确：

toolName: view_files

status: success

filePath: f:\swe\098823\project-02\plugins\manager_loader.go

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

toolName: Shell

status: success

工作区是 CRLF（全仓如此，gofmt 对所有文件都报），git 索引为 LF，且 git diff 已正常显示。把我的文件转成 LF 副本做一次语义级 gofmt 校验：

toolName: Shell

status: success

toolName: Shell

status: success

race 运行已完成且退出码 0。确认无 data race，并查看 manager.go 的 gofmt 真实差异：

toolName: Shell

status: success

竞态测试通过（262s，0 个 data race；之前的挂起是首轮 -race 构建争用）。修正 `GetManager` 字段对齐：

toolName: edit_file_search_replace

status: success

filePath: f:\swe\098823\project-02\plugins\manager.go

changes: undefined

toolName: Shell

status: success

toolName: todo_write

status: success

Todos updated: 8 items

最终回归：再跑一次 plugins 全量测试（含新用例），以及 scrobbler 消费侧测试：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: GetDiagnostics

status: success

最后通读完整 diff 做一次自审：

toolName: Shell

status: success

toolName: Shell

status: success

toolName: search_by_regex

status: success

toolName: todo_write

status: success

Todos updated: 8 items

已完成「候选提交」改造，歌词、scrobble、调度等能力不再因一次失败的权限/配置保存而中断。

## 改动前的问题

[manager.go](file:///f:/swe/098823/project-02/plugins/manager.go) 的 `updatePluginSettings` 顺序是「先写库 → 再卸载旧实例 → 再加载新配置」，任一步失败（Wasm 编译失败、host service 创建失败）都会留下：旧实例已销毁、不可运行的设置已落库、插件被强制禁用。

## 改造后的提交流程

**1. 候选构建与提交拆分** — [manager_loader.go](file:///f:/swe/098823/project-02/plugins/manager_loader.go)
- 新增 `pluginCandidate` 与 `buildPluginCandidate()`：用新配置/权限完整编译 Wasm、创建全部 host services、探测并校验 capability，全程不触碰 `m.plugins` 和数据库；任何失败自动释放已积累的 host services 与编译产物。
- `commitCandidate()`：在 `m.mu` 内先做**指针代际校验**（活动实例必须仍是构建候选时观察到的那一个），再 `repo.Put` 落库，然后才替换 map 中的活动实例——持久设置与活动实例共同切换；DB 失败或代际冲突都只销毁候选、返回可诊断错误。
- 成功后只发布一个新实例，并在锁外对**被替换的那个确切指针**执行 `destroyPlugin()`（host closers + compiled close + GC），不会按名字误关其他代际。

**2. init 成为校验门** — [capability_lifecycle.go](file:///f:/swe/098823/project-02/plugins/capability_lifecycle.go)：`callPluginInit` 改为返回错误；初始化在候选发布前执行，失败则候选作废，旧实例继续服务（同步更新了 lifecycle.yaml 与 README 契约说明）。

**3. 并发与代际保护** — [manager.go](file:///f:/swe/098823/project-02/plugins/manager.go)
- 每个插件 ID 一把操作锁（`pluginOpLock`），enable/disable/保存全程串行；迟到请求拿锁后重新读库，基于最新成功结果重建。
- 每个发布实例带单调 `generation`，配合提交时的指针 CAS：迟到请求无法覆盖更新的成功结果，冲突时返回 `errPluginGenerationConflict` 提示重试。

**4. 保留的语义**
- 已禁用插件：只落设置、不启动实例；
- 移除 users/library 必需权限：原有禁用流程不变（抽成 `permissionLossReason`）；
- 候选失败时把诊断信息写入**旧行**的 `LastError`（重新读库，不污染旧配置），管理接口错误包含具体阶段（如 `reloading plugin with new settings: compiling plugin: ...`）；
- 数据库只保存验证通过的配置，重启自然只加载最后一次成功提交；
- 行对象先做私有拷贝，正确性不依赖仓储是否返回共享指针。

## 验证

- 新增 [manager_candidate_test.go](file:///f:/swe/098823/project-02/plugins/manager_candidate_test.go) 7 个用例：编译失败旧实例/旧配置不动且仍可调用、DB 提交失败仅弃候选、禁用只存设置、成功切换只留一个新代际、代际冲突拒绝提交、24 并发保存代际严格单调且最终实例可用、权限缺失判定。
- `go build -tags=netgo,sqlite_fts5 ./...`、`go vet` 通过；`./plugins/` 全量 675 个 spec 通过，`-race` 全量通过（0 data race）；`cmd`、`server/nativeapi`、`core/scrobbler` 测试均通过。

一个环境备注：本机 Windows 检出把 testdata 里的符号链接 `test-storage-plugin-2` 还原成了 19 字节文本，我在本地按其指向复制了一份 gitignore 的 `.ndp` 夹具用于跑测试，该文件不进入提交。
