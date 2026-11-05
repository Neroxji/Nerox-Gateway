# Goroutine：机制与当前实验能说明什么

## 先给结论

现有实验足以解释这个 Gateway 在请求负载中的 goroutine 数量变化、网络等待、取消和资源回落；不足以从实测数据解释 Go runtime 的调度细节。goroutine 数不能直接当作活跃请求数，也不能据此推导“一请求等于一个 goroutine”。

本文把 Go 机制说明和项目实测分开。官方文档说明语言与 runtime 的一般行为；实验记录只说明当前代码、环境和负载下观察到的现象。

## Goroutine 是什么

`go f()` 启动一个新的 goroutine，让 `f` 与调用方并发执行。它是 Go runtime 管理的执行单元，不是一个固定对应的操作系统线程。

Go runtime 用 G、M、P 描述调度资源：G 是 goroutine，M 是操作系统线程，P 提供运行 Go 代码所需的调度资源。runtime 调度 G 在持有 P 的 M 上运行，因此“并发数”“goroutine 数”“操作系统线程数”和“同时使用的 CPU 核数”是不同指标。[Go runtime 的调度说明](https://go.dev/src/runtime/HACKING)

并发表示任务在一段时间内交错推进；并行表示任务在同一时刻由多个执行资源运行。当前实验是并发负载测试，没有测量 G、M、P 的调度轨迹，不能拿 goroutine 峰值推算线程数或并行度。

## 需要掌握的机制

| 机制 | 当前说明范围 |
|---|---|
| 创建与退出 | `go` 语句启动 goroutine；退出不会自动通知其他 goroutine。需要确认工作完成时，要通过同步手段等待。 |
| G-M-P 调度与抢占 | 这是 runtime 调度机制；本项目的 goroutine 总数和请求耗时不能单独说明调度策略或抢占效果。 |
| 阻塞、等待与唤醒 | goroutine 可能等待网络 I/O、channel、锁或其他条件。可用 goroutine profile 看它停在哪条调用栈。 |
| channel、锁、原子操作等同步 | 它们用于协作和保护共享状态；仅仅启动多个 goroutine 并不会让共享数据自动安全。[Go 内存模型](https://go.dev/ref/mem) |
| Context 取消（库级协作机制） | Context 发出取消信号；执行中的代码需要检查并响应。它不会强制终止任意 goroutine，也不等待工作结束。[Context 文档](https://pkg.go.dev/context) |
| 动态栈与 GC | goroutine 有由 runtime 管理的栈，并与 GC 工作相关。本轮没有 GC 实验，本文不据 heap 快照推导 GC 或栈行为。 |
| `defer`、panic、recover | 属于 goroutine 执行期间的退出与错误处理行为；当前实验没有覆盖。 |

runtime 的网络轮询器会等待网络 fd 就绪，并使等待的 goroutine 重新变为可运行；这让网络等待不等同于 goroutine 持续占用 CPU。[runtime netpoll 实现](https://go.dev/src/runtime/netpoll.go) 当前实验观察到的是 Gateway 的网络读取调用栈和资源变化，而不是完整的 runtime 调度事件。

## 当前实验提供的证据

### 1. 并发档位不是逐个 goroutine 加一

E13-08 使用 `c=1/4/16/64` 四档并发，每轮 200 个请求；不是从 1、2、3 一直逐个增加。`c` 表示负载生成器配置的并发度，不表示进程 goroutine 总数。

同在 `c=64`，记录的 goroutine 峰值随路径不同：hit 为 111、miss 为 203、fallback 为 253。进程中还包含服务处理、HTTP Transport、数据库连接池、日志 worker、runtime 和诊断等 goroutine；因此峰值不等于活跃请求数，也不是稳定的“每请求 goroutine 成本”。这组数据支持的是：在本次配置下，实际回源路径的峰值会随并发和在途工作变化；限流路径没有按相同比例增长。[E13 并发与资源基线](results/Phase1-E01-E13-upstream-baseline.md)

### 2. profile 能指出 goroutine 在等什么

E9 的慢 Provider 负载中，goroutine profile 捕获到 `StreamOnce → Scanner.Scan → persistConn.Read`；另一轮也观察到 `poll.FD.Read`。这说明采样时请求正在等待上游 SSE 数据，而不是在业务代码中持续计算。负载结束后，请求相关 goroutine 和连接下降到稳定平台。[E9 慢 Provider 与持续负载](results/Phase2-E02-E09-sse-resource-baseline.md)

这类证据适合解释“为什么请求还没结束”以及“它停在哪里”，但单份 profile 不是调度全过程，也不能据此得出所有网络库、所有操作系统下都完全相同的结论。

### 3. 取消是协作式的，结束后要看资源是否回落

E2/E9 的断开实验显示，客户端断开后 Gateway 读到了 `context canceled`，请求相关 goroutine、fd 和 TCP 连接随后下降。E2 的补充实验还显示：Fake Provider 的 Go HTTP handler 只有在监听请求 Context 并主动退出时，才会提前结束。Context 的取消信号本身不是强制杀死 goroutine 的指令。

批量断开后仍存在的部分 goroutine 能对应到 MySQL idle connection watcher、HTTP keep-alive 等连接保温行为；两轮持续并发也形成稳定资源平台，未观察到无界增长。这些结果支持“本次负载下没有发现 goroutine 泄漏信号”，不等于证明所有代码路径都没有泄漏。[E2/E9 取消与资源回落](results/Phase2-E02-E09-sse-resource-baseline.md)

此外，实验发现 Gateway 将部分 body 读取取消记录为 HTTP 200。它是取消错误分类和请求结果记录问题；goroutine 结束或资源回落，不代表业务结果就被正确处理。

## 现有数据能支持和不能支持的结论

**能支持：**

- goroutine 总量会受并发档位、请求路径、网络连接和连接池状态共同影响。
- 慢上游会让请求 goroutine 等待网络读取；等待不等于 CPU 忙等。
- 客户端断开后，Gateway 的请求资源在当前实验中会回落；需要配合的下游 handler 才能响应取消。
- 判定泄漏要比较负载前、负载中、停载后和重复负载的平台，并识别连接池等常驻资源。

**不能由现有实验推出：**

- G-M-P 的具体调度、抢占、工作窃取、公平性，或操作系统线程的精确数量。
- channel、mutex、WaitGroup、select 的阻塞与唤醒成本，以及共享内存同步是否正确。E13 的 `go test -race` 通过，但没有在 race 二进制下执行并发压测。
- goroutine 栈增长、CPU 密集型负载的并行扩展、GC 对栈的影响。
- 慢客户端造成真实下游 `Write` 背压的行为；现有 E9 小响应没有填满 TCP 发送缓冲。

## 对工程实践的迁移

- **容量判断：** 以请求并发、吞吐、尾延迟、依赖等待和资源峰值一起判断；不要用 goroutine 总数替代并发请求数。
- **排障：** 用 goroutine profile 找等待栈，再和上游调用数、fd、TCP、连接池等待及停载回落对照。
- **生命周期：** 把 Context 从入口传到下游；每个长期等待点都要能响应取消，并把取消与 Provider 故障、成功响应分开处理。
- **泄漏判断：** 先建立同一进程的稳定基线；区分仍在运行的请求、连接池保温和真正持续增长的资源。

## 参考资料

- [Go runtime 内部：G、M、P 与调度器](https://go.dev/src/runtime/HACKING)
- [Go runtime 网络轮询器](https://go.dev/src/runtime/netpoll.go)
- [Go 内存模型](https://go.dev/ref/mem)
- [Go `context` 包文档](https://pkg.go.dev/context)
- [Go `net/http` 服务 goroutine 说明](https://go.dev/src/net/http/server.go)
