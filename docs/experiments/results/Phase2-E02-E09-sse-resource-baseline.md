# 阶段 2：SSE、取消和资源生命周期本机基线

> 实验编号：E2 + E9
> 状态：E2-01~04 和 E9 四类负载已执行；Gateway 业务代码未修改
> 执行日期：2026-11-02~04
> Gateway 业务代码版本：`3cdaa6535246973a2878c5f0d97e3fc930035afe`
> 关联计划：`docs/experiments/nerox-gateway-experiment-plan.md`

## 实验范围

本文记录阶段 2 的 E2 取消实验和 E9 后续资源实验入口。macOS 与 Windows 的 E13 环境及性能/资源参考基线已统一放在 [Phase1-E01-E13-upstream-baseline.md](Phase1-E01-E13-upstream-baseline.md)，供各自在同一台机器上比较，不把跨机器数字当作相同阈值。

E2 的首字节前、首个 frame 后、多个 frame 后和并发客户端断开均已完成。E9 的四类资源负载也已完成，结果集中记录在本文件的 E9 区域。

| 区域 | 关注点 | 当前状态 |
|---|---|---|
| E2 | 客户端断开后的取消、重试、缓存/扣费和资源释放 | E2-01~04 已完成，发现取消被误判为成功或 Provider 故障 |
| E9 | 慢上游、慢客户端、批量断开和持续并发下的资源增长与回落 | 四类负载已完成，未观察到无界 goroutine/fd/TCP 增长 |

## E2：客户端主动断开 SSE

### 实验目标

**要回答的问题**：客户端已经断开后，Gateway 是否停止向客户端写入、停止读取 Provider，并结束上游请求、goroutine 和连接？取消错误是否被误判为 Provider 故障，从而触发无意义的重试或 fallback？

### 实验设计

使用持续输出的 Fake Provider，客户端分别在以下时机主动关闭连接：

1. Gateway 收到首字节前。
2. 收到第一个 SSE chunk 后。
3. 收到多个 SSE chunk 后。
4. 多个客户端并发断开。

### 共同观察项

客户端最终收到的内容和状态；Fake Provider 是否感知取消；Provider 调用次数和 Key；是否发生 fallback；Gateway 是否继续写缓存、扣费或写成功日志；goroutine、fd 和 TCP 是否回落。

### 完成判断

每个断开时机都有客户端、Fake Provider、Gateway 日志、Redis/MySQL 和资源快照。能解释取消发生在哪个阶段、是否重试、是否产生残缺缓存或错误扣费；如果取消被当作普通 Provider 故障，记录最小修复并用同一场景复验。

### E2-01：首字节之前断开

#### 实验目的

验证客户端在 Gateway 收到首字节前断开时，取消是否能停止上游请求，并确认 Gateway 是否错误触发 fallback。

#### 实验设置

- 用例 ID：`e2-header-cancel-20261102-01`。
- primary Fake Provider：`header-delay`，响应头延迟 6000ms。
- fallback Fake Provider：`normal`。
- 客户端：`curl --max-time 1`，请求发出约 1 秒后主动关闭；请求体使用唯一消息，避免缓存命中。
- 执行前：清空 Fake Provider 统计和当前用户限流 key；记录用户余额、日志最大 ID 和目标内容缓存 key。

#### 预期

客户端在首字节前断开后，Gateway 的 `Request.Context()` 应被取消。代码侧预期 `client.Do` 返回 context canceled，并把它分类为 `ErrRetryNextModel`；Gateway 可能进入 fallback 循环，但带着已取消的 context 的第二次请求不应真正到达 fallback Provider。

#### 实际结果

- 客户端：`curl` 退出码 28，HTTP code=000，TTFB=0，耗时 1.001505s，收到 0 bytes。
- Gateway 日志：先记录 primary `context canceled`，随后又记录 fallback `context canceled`；最终 Gin 记录 HTTP 500、耗时约 1s。
- Fake Provider：2 秒时 `primary=1、fallback=0`，7 秒时仍为 `primary=1、fallback=0`；说明 Gateway 尝试了 fallback，但取消的上下文阻止了真正的 fallback 网络调用。
- MySQL：新增两条失败日志：id=3284（primary，status=500，latency=996ms）和 id=3285（fallback，status=500，latency=0ms）；两条都没有 token usage，`cache_hit=0`。
- 余额：用户 1 从 99958 保持为 99958，没有扣费。
- Redis：本次唯一内容的 `cache:content:<hash>` 不存在，没有写入残缺内容缓存。
- 资源：监控期间 goroutine 最高 20，断开后约 2 秒为 17、7 秒为 17；fd 从 53 降到 51~52，TCP `ESTABLISHED` 从 43 降到 40~41，未见单次请求造成的持续增长。

#### 证据如何取得

| 要回答的现象 | 取证命令/数据 | 本次看到的证据 |
|---|---|---|
| 客户端收到什么 | `curl --max-time 1 -D headers -o body -w ...`，看退出码、HTTP code、TTFB、body | 退出 28、HTTP 000、0 bytes |
| Provider 调了几次、用哪个 Key | `curl "$FAKE_PROVIDER_URL/__test/stats" \| jq '{calls,records}'` | primary 1，fallback 0；primary 记录使用 key-2 |
| Gateway 是否取消/继续尝试 | `docker logs ... app \| grep 'Request fail\|POST.*api/v1/chat'` | primary 和 fallback 都出现 `context canceled` |
| 是否真正到达 fallback | Fake Provider `calls` 中是否出现 `fallback` | 没有 fallback 调用 |
| 是否写残缺缓存 | `redis-cli EXISTS cache:content:<hash>` | 结果为 0 |
| MySQL 日志和余额 | `SELECT ... FROM request_logs WHERE user_id=1 AND id>83`、查询 `users.balance` | 两条 500 失败日志，余额不变 |
| 资源是否回落 | `/debug/pprof/goroutine?debug=1`、`/debug/vars`、容器 `/proc/1/fd`、`/proc/net/tcp` | 断开后回到 17 goroutine，fd/TCP 未持续增长 |

#### 限制

原始用例使用的 Fake Provider `header-delay` 只是睡眠后写响应，没有检查 `r.Context().Done()`，也忽略写入错误。因此该次记录不能证明 Provider handler 已感知取消。后续补做的 Provider 取消感知验证见本节 E2-01。

#### 结论

首字节前取消已经传到 Gateway 的上游 HTTP 请求；补充实验还确认，监听 `r.Context().Done()` 的 Fake Provider handler 能在响应头前观察到取消并退出。当前 Gateway 错误分类仍会触发一次无意义的 fallback 尝试；本次没有写入缓存，也没有扣费。

### E2-01 补充：Provider handler 取消感知

#### 实验目的

原始 E2-01 使用不监听 Context 的 `header-delay`，只能证明 Gateway 取消上游请求。为验证取消是否到达一个会配合取消的 Provider handler，增加并执行 Fake Provider 专用场景 `cancel-aware-header-delay`。

#### 实验设置

- 用例 ID：`e2-01-provider-context-cancel-20261103-unique-01`。
- primary 等待响应头 6000 ms，并在等待期间监听自己的 `r.Context().Done()`；fallback 配置为 normal。

#### 预期

- 预期客户端在首字节前关闭后，Provider handler 在响应头发送前观察到 `context canceled` 并退出；Gateway 不应把取消当作 Provider 故障再次尝试 fallback。

#### 实际结果

- 客户端使用 `curl --max-time 1`，实际退出码 28、HTTP 000、TTFB 0、总耗时 1.005415 s、收到 0 bytes。
- Fake Provider primary 调用 1 次，fallback 0 次。Provider handler 开始于 `19:36:14.002761`，在 `19:36:14.985257` 观察到 `context canceled`，约 982.5 ms 后退出；没有发送响应头，stats 为 `cancel_observed=true`、`status_code=0`、`chunks=0`。
- Gateway primary `client.Do` 和随后尝试的 fallback 都记录 `context canceled`；Gin 最终记录 HTTP 500，约 1.01 s。
- MySQL 新增 id=3305（fallback，500，0 ms）和 id=3306（primary，500，990 ms），均无 usage；用户 1 余额仍为 99958。
- 实验结束后重启 Fake Provider，配置恢复为启动默认的 primary/fallback normal；Gateway 业务代码未修改。

#### 结论

Gateway 取消上游请求后，Fake Provider 的 Go `net/http` server 检测到连接中断，并取消该请求自己的 Context；显式监听 `r.Context().Done()` 的 handler 在 6 秒等待结束前退出。这证明取消信号到达了本地、会配合取消的 Provider handler。它不证明真实厂商内部一定停止推理或如何计费。此次验证也再次确认：Gateway 仍把首字节前取消归为 `ErrRetryNextModel` 并尝试 fallback。

### E2-02：首个 SSE frame 后断开

#### 实验目的

验证客户端收到首个完整 SSE frame 后断开时，Gateway 是否能在 body 读取阶段识别取消，停止读取上游，并避免 fallback、残缺缓存、错误扣费和资源滞留。

#### 实验设置

- 客户端收到第一个完整 SSE frame 后立即执行 `shutdown(SHUT_RDWR)+close()`。
- Fake Provider 使用正常 SSE 输出，控制首帧时序；本次复跑使用唯一消息 `e2-first-chunk-20261103-135857-r2`。
- 断开发生在 usage 和 `[DONE]` 之前。

#### 预期

客户端应只收到首个完整 frame；Gateway 的 `scanner` 应在后续读取时得到 `context canceled`，停止当前请求且不触发 fallback。请求日志应记录取消或客户端断开，而不是成功 200；在没有 usage 的情况下不应扣费或写入内容缓存。请求相关 goroutine、fd 和 TCP 应在停载后回到请求前水平。

#### 实际结果

- Run：`e2-first-chunk-20261103-135857`；客户端收到第一个完整 frame 后立即 `shutdown(SHUT_RDWR)+close()`，TTFB 为 2538.7 ms。
- Gateway 在 body 扫描阶段收到 `context canceled`，约 2.54 s 结束并记 HTTP 200；primary 调用 1 次、fallback 0 次。Fake Provider handler 继续运行约 5.008 s，因为 normal 场景未监听 Context。
- MySQL id=3286，latency=2523 ms、status 200、usage 0、余额 `99958 -> 99958`；内容缓存不存在。
- 资源复跑使用唯一消息 `e2-first-chunk-20261103-135857-r2`。其 Fake Provider stats 被后续控制请求覆盖，因此下面只用该复跑的 Gateway/资源数据。

#### 性能与资源

| 复跑时点 | goroutine | fd | TCP ESTABLISHED | CLOSE_WAIT | HeapAlloc | app cgroup 内存 | DB Open/InUse/Idle/Wait |
|---|---:|---:|---:|---:|---:|---:|---|
| 采样开始，空载 | 14 | 13 | 2 | 0 | 1.45 MB | 58.4 MiB | 1/0/1/0 |
| 请求进行中（约 1.0~2.5 s） | 18 | 15 | 3 | 0 | 1.59~1.73 MB | 58.4~58.7 MiB | 1/0/1/0 |
| 客户端关闭后约 0.7 s | 14 | 13 | 2 | 0 | 1.81 MB | 58.4 MiB | 1/0/1/0 |
| 停载约 13.7 s | 14 | 13 | 2 | 0 | 2.44 MB | 58.7 MiB | 1/0/1/0 |

请求相关 goroutine、fd 和 TCP 回到空载值；没有观察到 `CLOSE_WAIT` 或 DB 等待增长。`/debug/vars` 没有 active proxy/request 计数器。

#### 结论

资源层面没有观察到请求相关对象持续滞留，但取消在 body 读取阶段仍被错误地当作成功，Gateway 记录了 HTTP 200。此用例未进入 usage，所以本次没有产生扣费和残缺缓存。

### E2-03：多个 SSE frame 后断开

#### 实验目的

验证 Gateway 已经转发多个 SSE frame 后，客户端中途断开是否会停止后续读取和写入，并检查部分内容是否被误当成完整成功结果。

#### 实验设置

- 客户端收到 2 个完整 SSE frame 后真实关闭 TCP。
- 断开发生在 usage 和 `[DONE]` 之前；Fake Provider 继续输出一段时间，用来观察 Gateway 是否仍在等待上游。
- 同时记录事件时间线，以及请求前、请求中、断开后和停载后的资源快照。

#### 预期

客户端应只收到已经到达的两个 frame；Gateway 应在 body 读取阶段得到 `context canceled`，不再触发 fallback，不写残缺内容缓存，也不扣费。日志应明确表示流被取消或读取失败，而不是 `ErrSuccess`/HTTP 200。请求相关 goroutine、fd、TCP 和 DB 等待应在停载后回到基线；heap 允许受 GC 时机影响，但不能据单次快照判断泄漏。

#### 实际结果

- Run：`e2-multiple-chunks-cancel-20261103-145304`；客户端收到 2 个完整 frame 后真实关闭 TCP，未收到 usage 或 `[DONE]`。从请求开始到第 2 frame 约 2.006 s。
- Gateway 在客户端关闭约 3.7 ms 后读到 `context canceled`，约 2.03 s 结束并记 HTTP 200；primary 1 次、fallback 0 次。Fake Provider handler 运行约 12.006 s，仍未监听 Context。
- MySQL id=3304，latency=2016 ms、status 200、usage 0、错误为 `usage没收到!!?`；余额 `99958 -> 99958`。内容缓存 key `cache:content:55f470d12f008d60b1f5d2c6ad6466ff` 不存在。

#### 事件时间线

| 事件 | 时间 |
|---|---|
| Fake Provider handler 开始 | 15:09:54.179797 |
| Fake Provider 响应头 flush | 15:09:54.181012 |
| 客户端收到第 1 个完整 frame | 15:09:55.184480 |
| 客户端收到第 2 个完整 frame | 15:09:56.185710 |
| 客户端执行 shutdown/close | 15:09:56.185729 |
| Gateway 日志 `context canceled` | 15:09:56.189407 |
| Gateway Gin 请求结束 | 15:09:56.189557 |
| Fake Provider handler 结束 | 15:10:06.185700 |

#### 性能与资源

| 时点 | goroutine | fd | TCP ESTABLISHED/CLOSE_WAIT | HeapAlloc / HeapInuse | DB Open/InUse/Idle/Wait | app cgroup memory |
|---|---:|---:|---:|---:|---|---:|
| 请求前 | 26 | 37 | 26/0 | 2.41/4.28 MB | 13/0/13/0 | 63,913,984 B |
| 请求约 1 秒、收到第 1 frame 前后 | 30 | 39 | 27/0 | 2.57/4.49 MB | 13/0/13/0 | 63,991,808 B |
| 客户端关闭后约 1 秒 | 26 | 37 | 26/0 | 2.68/4.58 MB | 13/0/13/0 | 64,229,376 B |
| 停载 5 秒 | 26 | 37 | 26/0 | 3.36/5.02 MB | 13/0/13/0 | 63,717,376 B |
| 停载 30 秒 | 26 | 37 | 26/0 | 3.45/5.10 MB | 13/0/13/0 | 64,258,048 B |

`CLOSE_WAIT` 和 DB `WaitCount` 始终为 0；goroutine、fd 和 TCP 在客户端关闭后回到请求前平台。Heap 数值未回到原值，单次短测不足以据此判定泄漏。为控制 usage/`[DONE]` 时序，Fake Provider 实验版本临时增加过 10 秒间隔，实验后已恢复源码和镜像。

上表保留了事件时间线和请求前后完整资源快照。资源读法是比较同一次运行的请求前、断开后和停载后：goroutine、fd、TCP、`CLOSE_WAIT`、DB `WaitCount` 回到原平台说明请求级资源已释放；Heap 的短时上升要结合 GC 和长时间趋势判断。

#### 结论

多个 frame 已经发送后，Gateway 仍能结束自己的请求和连接，但把 `scanner.Err()` 后的取消记成 HTTP 200，并在日志里留下 `usage没收到!!?`。当前样本没有 usage，所以没有扣费和缓存；代码路径仍需要修复，避免完整性未确认时走成功处理。

### E2-04：16 个客户端并发断开

#### 实验目的

验证多个客户端同时在首个 SSE event 后断开时，取消处理是否会被并发放大成 goroutine、fd、TCP 或数据库连接泄漏，并观察停载后的资源回落。

#### 实验设置

- 并发数 `c=16`，16 个独立客户端均在收到首个完整 SSE event 后关闭连接。
- 每个请求使用唯一消息，Fake Provider 为正常 SSE；记录每个客户端是否收到首帧、到首帧耗时和 Gateway 的取消日志。
- 在负载前、观测峰值以及停载后 0/5/10/30 秒采集资源和连接池数据。

#### 预期

16 个请求都应在首帧后进入取消路径，不应为每个取消再触发 fallback。瞬时 goroutine、fd、TCP 和内存可以随并发上升，但停载后应回到稳定平台；`CLOSE_WAIT`、DB `WaitCount` 和活跃请求不应持续增长。连接池保留 idle 连接属于正常保温，需要与仍在处理的请求区分。

#### 实际结果

- Run：`e2-concurrent-disconnect-20261103-140841`；`c=16`，16 个独立客户端均在首个完整 SSE event 后关闭连接，16/16 收到首帧后断开；到首帧耗时 5018.4~5047.9 ms。
- Fake Provider primary 16 次、fallback 0 次；handler 耗时 10000.7~10005.1 ms，normal 场景未监听 Context。Gateway 记录 16 次 body `context canceled`，无 fallback 请求。
- MySQL 新增 id=3288~3303，均为 HTTP 200、无 usage，日志 latency 为 5004~5019 ms，余额不变；16 个内容缓存均不存在。

观测峰值为 `goroutine=91`、fd=70、TCP `ESTABLISHED=43`、`CLOSE_WAIT=0`、app 内存约 60.33 MiB。采样时间点受 `docker exec` 和诊断请求影响，峰值不作为精确瞬时上界。

#### 性能与资源

| 时点 | goroutine | HeapAlloc | HeapInuse | NumGC | DB Open/Idle/InUse/Wait | fd | TCP EST/CLOSE_WAIT | app 内存 |
|---|---:|---:|---:|---:|---|---:|---|---:|
| 负载前 | 14 | 1.44 MB | 3.26 MB | 13 | 1/1/0/0 | 13 | 2/0 | 59.93 MiB |
| 观测峰值（during-1） | 91 | 3.33 MB | 5.23 MB | 14 | 14/14/0/0 | 70 | 43/0 | 60.33 MiB |
| 停载后 0 秒 | 27 | 3.68 MB | 5.51 MB | 14 | 14/14/0/0 | 38 | 27/0 | 60.37 MiB |
| 停载后 5 秒 | 27 | 3.73 MB | 5.53 MB | 14 | 14/14/0/0 | 38 | 27/0 | 60.21 MiB |
| 停载后 10 秒 | 27 | 3.78 MB | 5.59 MB | 14 | 14/14/0/0 | 38 | 27/0 | 60.21 MiB |
| 停载后 30 秒 | 27 | 3.82 MB | 5.60 MB | 14 | 14/14/0/0 | 38 | 27/0 | 60.21 MiB |

停载后 13 个额外 goroutine 对应 MySQL idle connection watcher，DB pool 保留 14 条 idle 连接；27 条 TCP `ESTABLISHED` 与 14 条 MySQL、13 条 Redis 空闲连接对应，不是仍有 16 个活跃请求。`CLOSE_WAIT=0`、DB `WaitCount=0`。本轮是 c=16 短突发，不代表长时间持续负载。

上表保留了负载前、峰值和停载 0/5/10/30 秒的完整数据。并发实验重点看峰值是否随 c 增长，以及停载后是否回落；数据库 idle 连接和 watcher 需要按连接池行为解释。

#### 结论

并发取消没有观察到请求级资源持续滞留，也没有 fallback 放大；停载后的额外 goroutine、TCP 和 DB 连接主要属于数据库连接池的 idle 保温。Gateway 仍把 16 个 body 取消记录为 HTTP 200，和单请求用例暴露的是同一个成功判定问题。

### E2-01~04 总体结论与待修复项

#### 总体结论

- E2-01 的首字节前取消会在 `client.Do` 阶段返回错误，Gateway 把它当 Provider 故障尝试 fallback；Fake Provider 的补充验证确认取消已传到其 Go HTTP handler，合作式 handler 能在响应头前退出。
- E2-02/03/04 的 body 读取阶段都观察到 `context canceled`，没有 fallback；并发 c=16 未观察到请求相关资源在停载后持续滞留。
- E2-02/03/04 的请求日志仍记为 HTTP 200；本次在 usage 到达前取消，因此没有扣费或内容缓存。若取消前已读到 usage，当前路径仍可能扣费。

#### 需要修复

1. **首字节前取消被归为 Provider 故障。** `backend/internal/provider/chat.go` 的 `client.Do` 错误分支统一返回 `ErrRetryNextModel`，使 `backend/internal/httpapi/chat.go` 尝试 fallback。应单独识别客户端 Context 取消，结束请求并避免对已取消的 Context 重试。
2. **body 扫描错误被当作成功。** `StreamOnce` 在 `scanner.Err()` 后仍返回 `ErrSuccess`；Handler 因此记录 200，并可能在已有 usage 时扣费。应把取消/读取失败与正常收到 `[DONE]` 的成功路径分开，只对完整响应执行成功日志、缓存和扣费。
3. **取消后仍尝试写错误 SSE。** 客户端已断开时 `writeStreamError` 的写入无法送达，写错误也被忽略。取消错误应直接结束当前 handler；对于仍连接的 Provider 流读取错误，再按明确的流错误策略处理。

这些是实验记录的修复方向；本轮未修改 Gateway 业务代码。

## E9：goroutine、内存和文件描述符变化

### 实验目标

慢 Provider、慢客户端、不同阶段的断流和并发连接是否造成 goroutine 泄漏、连接泄漏、fd 耗尽或内存持续增长？负载停止后资源是否回到稳定平台？

### 实验设计

在 E2 的正常 SSE 控制组和 Phase1 的本机 E13 参考基线之上，分别施加以下负载：

1. Provider 长时间慢速输出。
2. 客户端建立连接但不及时读取 response。
3. 在首字节前、首个 chunk 后和多个 chunk 后批量断开。
4. 持续运行的并发请求，而不是只观察一次短突发。

### 观测指标

记录 goroutine、heap、alloc、GC、fd、TCP `ESTABLISHED/CLOSE_WAIT`、活跃请求、Provider 请求数、`db_pool` 和 Docker 容器内存；停载后记录 0/5/10/30 秒回落曲线。每轮都以本轮请求前快照为直接对照，Phase1 的 macOS 与 Windows 数值只用于理解机器和 Docker 资源背景。

### 完成判断

在固定负载和本机资源配置下，能区分正常保温资源和无界增长。持续增长、fd/TCP 不释放、heap 不形成稳定平台或资源达到容器边界时，保留 profile 和现场数据，定位泄漏或无界等待；没有异常时记录资源归属和回落时间。

### 共同实验条件

- 宿主机为 macOS Darwin ARM64，Docker Desktop 资源参考见 [Phase1-E01-E13-upstream-baseline.md](Phase1-E01-E13-upstream-baseline.md)。
- 每个场景单独清理 Fake Provider 统计和限流 key，使用唯一消息避免内容缓存命中；场景之间不并行修改共享 Docker 栈。
- 每轮记录本轮请求前基线、负载期间峰值或平台，以及停载后 `0/5/10/30s` 快照。跨场景的绝对值只作背景参考，直接判断使用同一轮的请求前数据。
- 原始证据目录：慢 Provider 为 `/tmp/e9_provider_run2_20261104_rerun`；慢客户端为 `/tmp/e9_client_run2_close_20261104T141410Z`；批量断开为 `/tmp/e9_disconnect_stages_20261104`；持续并发为 `/tmp/e9_continuous_normal_20261104`。

### 慢 Provider：上游长时间慢速输出

**实验设置**：primary 使用 `chunk-delay=7500ms`，fallback 为正常响应；`c=4、n=8`，分两波各 4 个请求，全部使用唯一 miss body，负载约 30 秒。Fake Provider 每个请求在响应头后等待两个 chunk 间隔，客户端 8/8 返回 HTTP 200。

**客户端和上游结果**：TTFB P50/P95/P99 为 `7508.66/7523.69/7523.69ms`，Total P50/P95/P99 为 `15019.03/15050.88/15050.88ms`；primary 调用 `8` 次、records `8`，fallback `0`。

**资源数据**：

| 时点 | goroutine | fd | TCP ESTABLISHED | HeapAlloc | DB Open/Idle/InUse/Wait | app memory |
|---|---:|---:|---:|---:|---|---:|
| 请求前 | 18 | 19 | 8 | 1.68 MB | 3/3/0/0 | 59.88 MiB |
| 负载中，4 个请求在途 | 33 | 27 | 16 | 2.12~2.31 MB | 4/4/0/0 | 约 60.0~60.2 MiB |
| 第二波在途 | 33 | 27 | 16 | 2.66~2.73 MB | 4/4/0/0 | 约 60.2 MiB |
| 停载后 0 秒 | 21 | 21 | 10 | 2.84 MB | 4/4/0/0 | 59.99 MiB |
| 停载后 5 秒 | 21 | 21 | 10 | 2.94 MB | 4/4/0/0 | 59.98 MiB |
| 停载后 10 秒 | 21 | 21 | 10 | 3.00 MB | 4/4/0/0 | 59.98 MiB |
| 停载后 30 秒 | 21 | 21 | 10 | 3.05 MB | 4/4/0/0 | 60.00 MiB |

全程 `CLOSE_WAIT=0`、`WaitCount=0`。goroutine profile 在负载中捕获到 4 组 `StreamOnce -> bufio.Scanner.Scan -> net/http.persistConn.Read`，说明请求是在等待上游 SSE body，而不是 CPU 忙等。负载结束后资源下降到稳定平台；`db_pool` 从 3 条连接扩展到 4 条并保持 idle，是连接池行为。Heap 在 30 秒内仍上升，但该轮 `Alloc` 没有触发下一次 GC，且诊断采样本身会分配内存，不能单凭这组快照判定泄漏。

**结论**：当前 `ResponseHeaderTimeout` 只约束响应头，不能限制响应头之后的慢 body；慢 Provider 会让每个在途请求长期占用上游 socket、Gateway goroutine 和部分内存。此轮没有发现资源无界增长，但说明 E9 后续若要控制长时间占用，需要单独讨论 body idle timeout 或请求最大持续时间。

### 慢客户端：建立连接但不及时读取 response

**实验设置**：primary 使用 `chunk-delay=20s`，`c=8`；4 个客户端读到首帧后停止读取，4 个客户端从头不读取，全部在 35 秒主动关闭，Provider 的第二帧计划在 40 秒后到达。首帧约 157 bytes，客户端关闭时 Gateway 仍在等待上游第二帧。

**资源数据**：

| 时点 | goroutine | fd | TCP ESTABLISHED/CLOSE_WAIT | HeapAlloc / HeapInuse | DB Open/Idle/InUse/Wait | app cgroup memory |
|---|---:|---:|---:|---:|---|---:|
| 请求前 | 25 | 37 | 26/0 | 2.70 / 4.66 MB | 8/8/0/0 | 62.48 MiB |
| 负载 5 秒 | 53 | 51 | 40/0 | 4.72 / 6.64 MB | 8/8/0/0 | 62.30 MiB |
| 负载 15 秒 | 53 | 51 | 40/0 | 4.83 / 6.73 MB | 8/8/0/0 | 62.30 MiB |
| 负载 25 秒 | 53 | 51 | 40/0 | 4.95 / 6.83 MB | 8/8/0/0 | 62.31 MiB |
| 客户端关闭时 | 21 | 35 | 24/0 | 4.73 / 6.78 MB | 8/8/0/0 | 62.66 MiB |
| 停载后 5 秒 | 21 | 35 | 24/0 | 4.31 / 6.39 MB | 8/8/0/0 | 62.41 MiB |
| 停载后 10 秒 | 21 | 35 | 24/0 | 4.37 / 6.46 MB | 8/8/0/0 | 62.41 MiB |
| 停载后 30 秒 | 21 | 35 | 24/0 | 4.45 / 6.48 MB | 8/8/0/0 | 62.42 MiB |

Provider primary 调用 `8` 次；客户端关闭时 records 尚未完成，停载后 Fake Provider 仍完成 8 条记录，因为该测试场景没有监听 `r.Context().Done()`。Gateway 日志为 8 次 `context canceled`，但 HTTP 仍记为 200。

**profile 证据**：负载 25 秒时有 8 组 `StreamOnce -> Scanner.Scan -> bodyEOFSignal.Read -> poll.FD.Read`，没有 `poll.FD.Write` 或 `net.Conn.Write` 阻塞；8 个 Transport `writeLoop` 处于空闲 select。也就是说，本次小响应没有填满 Gateway 到客户端的 TCP 发送缓冲，慢客户端没有形成 HTTP 写阻塞，实际占用主要来自等待上游 body 的读路径。

**结论和限制**：客户端关闭后 goroutine、fd 和 TCP 明显下降，未见 `CLOSE_WAIT` 或 DB 等待增长。这个场景证明了“慢客户端连接会延长请求生命周期”，但没有覆盖大响应或高并发下的真正写背压；要研究 `ResponseWriter.Write` 阻塞，需要更大的持续 SSE 响应或更高并发。

**后续补测建议（未执行）：Gateway 向慢客户端写背压**

- **目的**：确认客户端持续不读时，Gateway 的 SSE 写入是否会卡住；量出这种写阻塞会占用多少请求 goroutine、fd 和 TCP 连接，并确认客户端恢复读取或断开后资源是否释放。这不仅是定位阻塞函数，也用于判断慢客户端是否会长期占住并发容量，以及是否需要请求时限或并发限制。
- **现有缺口**：本轮只有约 157 bytes 的首帧，Provider 第二帧在 40 秒后才到；profile 捕获到的是等待上游 body 的读路径，没有捕获下游写阻塞。
- **最小场景**：先用 `c=1`，让 Fake Provider 快速连续发送大量 SSE 帧；客户端保持连接但不读取，并确保数据量足以填满 TCP 缓冲（必要时限制客户端接收缓冲）。每个 SSE 行需小于 `bufio.Scanner` 的单行限制，避免混入 Scanner 超长行问题。若目标还包括容量评估，再逐步增加并发；这属于同一类写背压场景的扩展，不是必须拆成第二种实验。
- **判断证据**：负载中的请求栈进入 Gateway 的 response `Write`/`Flush`，并停在 `net.Conn.Write` 或 `poll.FD.Write`；可同时观察客户端连接的 TCP `Send-Q`。客户端开始读取或断开后，写入解除阻塞，请求 goroutine 和连接资源回落。
- 这项补测与阶段 4 E6/E7 的日志 Channel/数据库消费者背压不同，当前后续计划没有覆盖这个 Gateway→客户端网络写路径。

### 批量断开：不同阶段的 `c=16` 客户端

实验在重启 app 后建立 clean baseline：`goroutine=14`、`HeapAlloc=1.55MB`、`HeapInuse=3.48MB`、`fd=13`、TCP `ESTABLISHED/CLOSE_WAIT=2/0`、DB `1/1/0/0`、app memory `52.62MiB`。三轮均使用唯一消息，Fake Provider 统计单独 reset。

| 断开阶段 | 注入条件与客户端结果 | Provider / 日志 | 负载峰值 | 停载后平台（0~30 秒） |
|---|---|---|---|---|
| 首帧前 | `header-delay=4000ms`；16/16 HTTP 200，0 个 SSE frame，响应约 4098~4165ms | primary 16，fallback 0；16 条 200 日志，usage=3 | goroutine 93，fd 75，ESTABLISHED 64，DB 16/16/0/0，app 58.02 MiB | goroutine 33，fd 45，ESTABLISHED 34，CLOSE_WAIT 0，DB 16/16/0/0，app 约 58.4 MiB |
| 首帧后 | `chunk-delay=3000ms`；16/16 收到 1 个 frame，约 3019~3021ms 后关闭 | primary 16，fallback 0；16 条 200 日志，usage=0，错误为 `usage没收到!!?` | goroutine 93，fd 75，ESTABLISHED 64，app 60.34 MiB | goroutine 29，fd 43，ESTABLISHED 32，DB 16/16/0/0，app 60.62~60.82 MiB；30 秒时 heap 经 GC 降低 |
| 多 frame 后 | `chunk-delay=3000ms`；16/16 收到 2 个 frame，约 6019~6035ms 后关闭 | primary 16，fallback 0；16 条完整上游记录，usage=3 | goroutine 93，fd 75，ESTABLISHED 64，DB 16/16/0/0，app 59.48 MiB | goroutine 33，fd 45，ESTABLISHED 34，CLOSE_WAIT 0，DB 16/16/0/0，app 59.44~59.69 MiB |

三轮 `DB WaitCount=0`、`CLOSE_WAIT=0`。批量 pprof 捕获到 16 组 `StreamOnce -> bufio.Scanner.Scan -> persistConn.Read`，证明 16 个在途请求都在等上游 chunk。停载后请求级 goroutine 和连接下降；剩余 16 条 MySQL idle、Fake Provider keep-alive 以及 Redis/诊断连接属于连接池或长连接平台，不是仍有 16 个活跃请求。当前 Gateway 仍把 body 取消记为 HTTP 200，和 E2 的单请求结果一致。

**结论**：`c=16` 下三种断开时机都没有产生可见的 fd、TCP 或 goroutine 无界增长；资源峰值大致随在途请求数增加，停载后回到该进程的连接池平台。这个实验没有证明真实 Provider 会取消，因为 Fake Provider 的 normal/chunk-delay handler 不监听 Context。

### 持续并发：两轮 `load-stop-load`

**实验设置**：primary/fallback 均为正常 SSE；每轮持续 60 秒，15 轮、每轮 16 个请求、4 秒节拍，共 240 请求；两轮之间清空限流 key 并 reset Fake Provider。两轮均 `240/240 HTTP 200`，primary 调用 240 次，fallback 0。

**客户端性能**：第一轮 TTFB P50/P95/P99 为 `21.077/34.210/39.109ms`，Total 为 `30.556/43.359/43.775ms`；第二轮 TTFB 为 `18.981/40.101/45.814ms`，Total 为 `25.530/57.323/57.887ms`。按节拍计算的持续吞吐为 `4 req/s`，轮内短 burst 吞吐均值约 `553.1/611.6 req/s`，二者口径不同，不能混用。

**资源数据**：两轮运行中和停载后均稳定在 `goroutine=33、fd=45、TCP ESTABLISHED=34、CLOSE_WAIT=0、DB Open/Idle=16/16、WaitCount=0`。第一轮 app memory 约 `61.75~62.04MiB`，第二轮约 `62.02~62.75MiB`。第一轮 HeapAlloc 从运行中的 `3.45MB` 上升到约 `5.72MB`，停载 30 秒为 `5.37MB`；第二轮 baseline 为 `5.43MB`，停载 30 秒为 `5.24MB`。`NumGC` 持续增加，说明 heap 在 GC 周期中回收和再分配，而不是只做单调累积。

**profile 证据**：采样主要看到 `logWorker`、HTTP Transport 的 `persistConn` read/write loop 和 `database/sql` 的 connection cleaner/opener；采样点在轮次结束后，没有捕获瞬时请求栈。

**结论**：两轮持续负载没有看到 goroutine、fd、TCP 或 DB wait 的无界增长，资源形成稳定平台。app memory 和 DB idle 平台比最初 clean baseline 高，主要来自前面实验建立的连接池、keep-alive 和 Go heap 保留，不能直接归因于本轮泄漏；判断泄漏需要把每轮自己的 stable baseline 与下一轮对比，而不是拿最初启动值直接做结论。

### 从资源曲线和 profile 反推程序行为

E9 的学习重点不是记住某个数字，而是把“现象”翻译成“程序内部正在等待什么”：

| 观测现象 | 可以提出的内部假设 | 用什么证据验证 |
|---|---|---|
| 慢 Provider 时 goroutine、fd、TCP 随在途请求增加，profile 停在 `persistConn.Read` | 每个请求都持有一条上游 HTTP 连接，并有 goroutine 阻塞等待 body；这是阻塞 I/O，不是 CPU 忙等 | goroutine 栈、Provider 调用数、TCP/FD 同步上升 |
| 慢客户端时没有 `FD.Write`，但 `StreamOnce` 仍在 `Read` | 小响应没有填满发送缓冲，真正占用资源的是上游读等待；尚未形成客户端写背压 | profile 中 Write 计数为 0，Read 栈有 8 组，客户端关闭后资源下降 |
| 批量断开后 goroutine/fd/EST 降低，但 DB `Idle` 保留 | 请求已经结束，连接池主动保留可复用连接；`Idle` 不等于活跃请求 | `CLOSE_WAIT=0`、`WaitCount=0`，TCP 连接可按 MySQL/Redis/Fake 归属解释 |
| 持续两轮中 goroutine/fd/TCP 保持平台，Heap 随 GC 上下波动 | 系统进入稳定工作平台，没有观察到这些资源无界增长；Go runtime 保留 heap 不等于泄漏 | 多轮 stable baseline、`NumGC`、heap profile 和停载回落曲线 |

底层知识对应关系如下：TCP socket 是文件描述符，`ESTABLISHED` 表示连接双方仍保持传输状态，`CLOSE_WAIT` 表示对端已经发 FIN 而本地还没关闭；goroutine 卡在 `Read` 时通常是在等待内核唤醒，不代表占满一个 CPU；`HeapAlloc` 是当前仍被 Go 对象使用的堆，`TotalAlloc` 是累计分配量，`HeapInuse` 是运行时已使用的堆页，必须结合 GC 和多轮趋势判断；`DB Open/Idle/InUse/WaitCount` 则分别描述连接池规模、空闲连接、正在使用的连接和因池满而等待的次数。

### E9 结论和后续动作

- 四类负载都观察到了资源随在途请求增加，并在请求结束或停载后下降；本轮没有发现 goroutine、fd、`CLOSE_WAIT`、TCP 或 DB wait 的无界增长。
- 慢 Provider 证明响应头之后的慢 body 会长期占用上游读路径；当前没有 body idle timeout，这属于后续策略讨论点，不等同于已确认的泄漏。
- 慢客户端实验没有填满发送缓冲，不能排除 `ResponseWriter.Write` 背压；按上面的单一补测方案另行验证，尚未执行，不属于本轮已验证结论。
- 批量断开再次复现 E2 的 HTTP 200 成功判定问题；E2 中记录的取消错误分类和 `scanner.Err()` 修复仍然是当前明确的代码修复项。
- 持续并发形成稳定资源平台，证明判断资源泄漏必须看多轮 stable baseline、profile 和回落曲线，不能看单个 Heap 数字。

### Heap/GC 机制对照补测（未执行）

**目的**：直观看到 GC 只回收不可达对象，不会清空整个 Heap；区分“已不可达、但还没等到 GC 的暂存对象”和“仍被引用、GC 后仍存活的对象”。这项补测既用于理解回收机制，也用于诊断 Heap 增长是否来自持续保留的对象；一次强制 GC 不能单独证明有或没有泄漏。

**现有问题**：慢 Provider 场景的 `Alloc` 从约 1.68 MB 升至停载 30 秒后的 3.05 MB，`NumGC` 一直是 43，`NextGC` 约 4.19 MB。该轮没有触发自然 GC，因此快照无法区分临时垃圾和仍然存活的对象。

**建议步骤**：

1. 预热 Gateway、缓存和连接池，等预热请求结束、资源平台稳定后记录基线；预热流量不计入对比。
2. 固定并执行一轮负载；停载并确认请求结束后，记录强制 GC 前的 `/debug/vars`，保存 `Alloc/HeapAlloc`、`HeapInuse`、`HeapIdle`、`TotalAlloc`、`NumGC`、`NumForcedGC` 和 `NextGC`。
3. 请求诊断端口的 `/debug/pprof/heap?gc=1`，保存 heap profile；该参数会在 heap profile 采样前运行 GC。随后再次采集 `/debug/vars`，比较 GC 前后的数值。记录 profile 的 `inuse_space`，必要时也看 `alloc_space`。
4. **至少执行 3 轮可比的“负载—停载—强制 GC”**，每轮使用相同请求量、并发和数据规模，比较 GC 后的 `HeapAlloc` 与 `inuse_space` 基线；不要在仍有请求进行时强制 GC，以免把仍在使用的请求对象误当成垃圾。若三轮的回收后基线仍持续上升，再增加轮数并分析 heap profile。

一轮“负载前后强制 GC”足以观察这轮有多少 Heap 在 GC 后回落；多轮对比才有助于区分一次性预热/连接池扩展和持续的对象保留，从而判断是否存在泄漏信号。

**如何解读**：GC 后 `HeapAlloc` 或 `inuse_space` 明显下降，说明此前有一部分已不可达对象尚未被回收；GC 后仍占用的对象可能是连接池、缓存等预期保留，也可能是意外引用，应通过 heap profile 找其分配位置并检查引用关系。若排除预期保留后，GC 后的基线仍随等量负载逐轮上升，才是需要继续定位的信号。`TotalAlloc` 是累计分配量，GC 后不会下降；`HeapInuse`、容器内存也不保证随一次 GC 同步下降，因为 Go 运行时可能保留内存供后续复用。诊断采样本身会分配少量内存，需记录并考虑这项开销。

**范围**：这是 E9 的诊断性补测建议，尚未执行，也不改变本轮结论。强制 GC 会扰动运行时，只在受控实验中使用。

E9 正式场景已完成；Fake Provider 已恢复为 primary/fallback `normal,status_code=200`，Docker app、Fake Provider、MySQL、Redis 均健康，实验期间未修改 Gateway 业务代码。E13 的 macOS/Docker Desktop 和 Windows 参考基线见 [Phase1-E01-E13-upstream-baseline.md](Phase1-E01-E13-upstream-baseline.md)。
