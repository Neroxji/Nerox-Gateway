# 性能测试学习手册（E13 沉淀）

> 定位：性能测试与观测的通用方法论，以本网关的真实实验数据为实例。
> 与 `results/Phase1-E01-E13-upstream-baseline.md` 附录的关系：附录 = E13 现场速查（矩阵一~四、作业卡）；
> 本手册 = 完整学习版。判读规则与实验设计规则以附录矩阵二/三为准，本文不重复。
> 整理日期：2026-11-02，来源：E13 实验过程问答的去重整理。

## 0. 一图总览：五个观测楼层

所有观测工具按"站在系统的哪一层"归位，每层只回答一类问题：

```text
┌─────────────────────────────────────────────┐
│ ④ 宿主机层    任务管理器、Docker Desktop        │ ← 机器还活着吗
├─────────────────────────────────────────────┤
│ ③ 容器/系统层  docker stats、/proc、fd、TCP     │ ← 环境资源够不够
├─────────────────────────────────────────────┤
│ ② 依赖层      redis-cli、mysql 客户端、         │ ← 依赖拖后腿了吗
│              Fake Provider 的 /__test/stats    │
├─────────────────────────────────────────────┤
│ ① 进程层      pprof（九个探测器）+ expvar       │ ← 进程内部发生了什么
│              benchmark、race 也属于这一层       │
├─────────────────────────────────────────────┤
│ ⓪ 客户端层    loadgen、curl -w                 │ ← 用户感受到多快
└─────────────────────────────────────────────┘
```

排障永远从 ⓪ 开始（先确认"慢"这个症状），再往 ① 问"为什么"；没有症状不下楼。
每层楼里挑哪个探测器，由怀疑的对象决定。

## 1. 统一查表：症状 → 楼层 → 工具 → 命令

| 症状/怀疑 | 楼层 | 工具 | 本项目命令 |
|---|---|---|---|
| 请求慢/开始排队 | ⓪ | loadgen | `e13_loadgen.exe -mode hit -c 16 -n 200 ...` |
| 慢是因为算力吗 | ① | pprof CPU | `curl .../debug/pprof/profile?seconds=10` |
| 慢是在等什么 | ① | goroutine、block/mutex | `curl .../debug/pprof/goroutine?debug=1` |
| 内存泄漏/GC 拖累 | ① | heap、allocs | `curl .../debug/pprof/heap` |
| 并发写坏数据 | ① | race | `go test -race ./...` |
| 改动变快还是变慢 | ① | benchmark | `go test -bench -benchmem ./...` |
| Redis 拖后腿 | ② | redis-cli | `--latency`、`INFO` |
| MySQL 拖后腿 | ② | SQL + expvar | `SHOW PROCESSLIST`、`/debug/vars` 的 db_pool |
| Provider 慢吗 | ② | 它自己的 stats | `curl .../__test/stats` |
| 容器资源扛住没 | ③ | docker stats、/proc | `docker stats --no-stream`、`ls /proc/1/fd` |
| 机器/环境异常 | ④ | 宿主机工具 | 任务管理器、Docker Desktop |

## 2. 进程层详解（楼层①）

### 2.1 pprof 九个探测器：各自回答什么、什么时候看

| 端点 | 回答的问题 | 什么时候看 | E13 使用情况 |
|---|---|---|---|
| `profile`（CPU） | CPU 时间花在哪个函数？ | 怀疑算力瓶颈：CPU 满了、吞吐上不去 | ✅ E13-09 |
| `heap` | 现在谁占着内存？ | 怀疑泄漏（停载不回落）、怀疑囤积 | ✅ E13-12 |
| `allocs` | 启动以来谁在持续分配？ | 怀疑 GC 压力：延迟毛刺、吞吐波动 | ✅（heap 的累计视角） |
| `goroutine` | 有多少 goroutine、卡在哪？ | 怀疑泄漏、怀疑"大家都堵着" | ✅ E13-10 |
| `block` | goroutine 在锁/channel 上等了多久？ | 怀疑锁竞争/排队（CPU 闲但慢） | ⚠️ 已开启（SetBlockProfileRate）但负载下未读 |
| `mutex` | 谁持锁太久导致别人等？ | 锁专项 | ❌ 未开启（需 SetMutexProfileFraction） |
| `threadcreate` | OS 线程创建历史 | 几乎不看（线程泄漏才用） | ❌ |
| `cmdline` | 进程启动命令 | 确认观测的是哪个二进制 | — |
| `trace` | 时间线级执行轨迹 | 需要微秒级归因（GC 暂停、调度延迟）才上 | ❌ |

不需要九个全认识：`profile / heap / goroutine` 三个覆盖约 80% 场景；
`block / mutex / trace` 等对应嫌疑出现再学。遗留项已记档，N3（锁竞争是主角）正好用。

### 2.2 排查决策树：什么时候看哪个

```text
客户端数字说"慢"
 ├─ CPU 忙（>50%）→ CPU profile：榜首函数就是问题
 └─ CPU 闲（E13 实测仅 1%）→ 等待型，往下查：
      ├─ goroutine：大家堵在哪个调用栈？
      ├─ block/mutex：堵在等哪把锁、等多久？
      └─ heap/allocs：是不是 GC 在拖？
停载后资源不回落 → goroutine + heap 逐条对账
```

E13 的实战对照：CPU 闲 + 吞吐到顶 → 判定等待型；goroutine 显示堵在等上游；
heap 显示 bufio 分配居首——三步走完，归因闭环。

### 2.3 profile 读法要点

- CPU profile：`flat` = 函数自身耗时，`flat%` 找瓶颈从大到小；`cum` = 含下级调用的总耗时
- 三种典型形状：业务函数大 = 算力型；`Syscall6`/`futex` 大 = I/O 型；`mallocgc`/GC 大 = 分配型
- CPU 忙度 = Total samples ÷（窗口 × 核数）。低于 5% 却吞吐到顶 = 等待型瓶颈的铁证
- heap 两视角：`inuse_space`（此刻谁占着，查泄漏/囤积）、`alloc_space`（累计谁在分配，查 GC 压力）
- 观察者效应：goroutine 报告包含"正在生成报告的 goroutine 自己"；pprof 自身缓冲也进 heap 账

### 2.4 benchmark：单函数的尺子

压测与 benchmark 是两把不同的尺子：

| | 压测 | benchmark |
|---|---|---|
| 对象 | 整条请求链路 | 单个函数 |
| 尺度 | 毫秒 | 微秒/纳秒 |
| 回答 | 系统多快 | 这段代码多贵 |
| 对照物 | 以前的压测结果（基线） | 以前的代码版本（回归标尺） |

输出参数逐列：

```text
BenchmarkGetTokenCache_Hit-20   8715   131636 ns/op   960 B/op   16 allocs/op
                    │            │          │            │           │
              GOMAXPROCS=20   迭代次数    每次耗时      每次分配字节   每次分配次数(越多GC压力越大)
```

现有 4 个基准都是"每请求热函数"：CheckRateLimit（限流）、GetTokenCache_Hit/Miss（鉴权缓存）、
WriteCachedStream（缓存命中写回）。E13 验收标准是"能稳定复现"——已达标。
完备性上仍缺：内容缓存 GET/SET、KeyPool 轮换、JSON 解码、扣费、日志入队。
补基准的时机由证据决定：profile 指到谁、或准备优化谁，再给谁立标杆，现在补齐只是凑数。

### 2.5 race：正确性测试，不是性能测试

- 查的是数据竞争：两个 goroutine 无保护地读写同一内存。Go 编译时插桩 + 运行时顺序追踪
- 观测方式：跑起来盯 stderr 的 `WARNING: DATA RACE` 报告（含两个冲突调用栈）；退出码 0 = 本次干净
- race 模式慢 5~10 倍，只查正确性，永远不用于测性能
- 抓的是"交错模式"不是"压力大小"——c4 就足够让常见竞态现形
- 局限：只证明"本次执行未发现"，不是数学证明，工程惯例是进 CI 每次跑
- Windows 本机跑 `-race` 需要 gcc；本项目用 golang:1.26-alpine 容器执行（apk 换腾讯镜像装 gcc）

## 3. 依赖层详解（楼层②）

工具：`redis-cli`（`--latency`、`INFO`、`SLOWLOG`）、MySQL 客户端（`SHOW PROCESSLIST`、
慢查询日志）、被测依赖自己的观测接口（Fake Provider `/__test/stats`）。

拆账方法：**单价 × 数量 = 总价**。

- 单价：一次 Redis 往返的 RTT（`redis-cli --latency` 实测，E13 = avg 0.46ms）
- 数量：代码里数出来的往返次数（鉴权 2 次 + 限流 1 次 + 缓存 1 次 ≈ 4~6 次）
- 总价：hit P50 2.6ms ≈ 1.6ms Redis + ~1ms 解析与写出

RTT 高 → 网络/部署问题；RTT 低但请求慢 → 往返次数太多或逻辑在磨蹭。
注意口径差异：连续 PING 循环与热连接单操作测出的数字可能差数倍，记录时要注明口径。

## 4. 容器/宿主机层详解（楼层③④）

- `docker stats --no-stream`：各容器 CPU/内存
- `docker exec ... ls /proc/1/fd | wc -l`：进程 fd 数（泄漏老牌信号）
- `cat /proc/net/sockstat`、busybox `netstat`：TCP 状态
- 宿主机：核数影响所有百分比判读（E13 = 20 核，CPU 忙度 1.1% 的分母）
- Windows 宿主机注意：进程级观察一律 `docker exec` 进容器做（计划 4.1 的规定）

## 5. 指标词汇表

### A. 本实验用到的（附 E13 实测值）

| 指标 | 含义 | E13 实测 |
|---|---|---|
| 吞吐 throughput | 每秒完成请求数 | hit c16 = 2728 req/s |
| 并发数 c | 同时在飞的请求数 | 1/4/16/64 |
| TTFB | 发请求→收到第一个字节 | P50 2.6ms |
| Total | 发请求→响应读完 | miss c1 = 11ms |
| P50/P95/P99 | 50%/95%/99% 的请求快于此值 | 见 E13-08 表 |
| P95÷P50 比值 | 排队程度体温计 | c64 hit = 6.8 |
| 错误率/状态码分布 | 正确性 | 全 200 / 全 429 |
| 样本量 n / 时长 wall | 统计可信度基础 | 200 条/轮 |
| goroutine 数 | 进程内并发执行单元数量 | 15 → 253 → 38 |
| CPU profile flat/cum | 函数自身/含下级 CPU 占比 | Syscall6 32% |
| CPU 忙度 | 采样总量÷(窗口×核数) | 1.1% |
| heap inuse_space | 此刻谁占着内存 | 共 17.26MB |
| heap alloc_space | 累计谁在分配 | bufio 67MB 居首 |
| ns/op / B/op / allocs/op | 单函数：耗时/分配字节/分配次数 | 22.7µs / 396B / 14 |
| db_pool Open/Idle/InUse | 连接池水位 | 20/20/0 |
| WaitCount/WaitDuration | 连接池排队次数/累计等待 | 全 0（从未排队） |
| Redis RTT | 一次往返延迟 | avg 0.46ms |
| 账面指标 | 日志条数/缓存 key 数/余额 | 10072 / 4019 / 99838+ |

### B. 实验外常用（生产环境进阶词汇）

**流量与容量**
- QPS/RPS、峰值流量、日活——容量规划入口
- 活跃请求数 in-flight——此刻正在处理多少，过载第一信号
- TCP 连接数（ESTABLISHED/TIME_WAIT）——TIME_WAIT 暴涨 = 短连接滥用

**延迟与稳定性**
- P90/P99/P999——更苛刻的分位数，大厂常盯 P999
- 长尾 tail latency——均值正常但尾部爆炸（E13 c64 是微型版）
- SLI/SLO/SLA——"99.9% 请求 < 200ms"的正式化表达
- 错误预算——SLO 允许犯错的额度，用完冻结发布

**资源与饱和度**
- CPU 利用率 / load average / CPU throttling（容器被限流）
- GC 暂停时间与频率——Go 服务延迟毛刺头号嫌疑人
- 队列长度/排队时间——饱和度直接证据（E7 主角）
- fd 数量——泄漏老牌信号

**中间件专项**
- 缓存命中率、穿透/击穿/雪崩——E12/N2 主场
- Redis 慢命令、big key——`SLOWLOG`
- MySQL QPS/TPS、慢查询、锁等待、主从延迟——E8 主场

**方法与工具名**
- USE 法（三视角框架原型）、RED 法（Rate/Errors/Duration）、黄金四信号（延迟/流量/错误/饱和度）
- Prometheus + Grafana——把手动 curl 变成 24 小时自动时序曲线
- OpenTelemetry——跨服务请求时间线（单体网关暂用不上）
- ELK 日志聚合、tcpdump 抓包——长期运行/包级排障才需要

以上"长期运行才需要"的四样不是漏掉的作业：实验平台阶段，手动按需观测完全正确。

## 6. 测试类型全景：清单是推导出来的，不是背出来的

推导链：程序形状 → 最怕的事故 → 对应武器。

| 程序形状 | 最怕的事故 | 武器 | Go 用法 |
|---|---|---|---|
| 有逻辑分支 | 算错 | 单元测试 | `go test`（表驱动惯用法） |
| 解析外部输入 | 被怪输入打崩 | fuzzing | `testing.F` |
| 并发共享状态 | 数据竞争（偶发难复现） | race 检测 | `go test -race`、`go build -race` |
| 有性能预算 | 悄悄变慢 | 微基准 + 基线 | `go test -bench` |
| 依赖外部服务 | 挂了/变慢 | 故障注入 | 本实验计划的整体方法（E1/E4/…） |

每做完一个实验问三个问题，测试清单就会滚雪球式长出来：

1. 这个实验暴露了哪类风险？
2. 我武器库里哪个对准它？
3. 哪个风险还没有武器？（记为学习项）

真实公司 Go 服务的标配金字塔：单元测试量大管饱、race 进 CI、关键路径挂 benchmark
防退化、集成测试验真实依赖、故障注入验降级。

## 7. 工作流：设计、执行、分析分离

"要测什么"的困惑源于把三件事混在一起。分离后永远不需要临场想要测什么：

- **设计时**（实验开始前）：假设与风险决定指标清单，清单写在实验文档里（"观察哪些现象/应该记录的指标"小节）
- **执行时**：照单子打命令、抄数字
- **分析时**：才用判读规则（比值/差值/拐点，见结果文档附录矩阵二）从数字提结论

## 8. 命令速查（本项目环境）

```bash
# 诊断入口（业务 18080 / 诊断 18082，均只绑回环）
curl -s 'http://127.0.0.1:18082/debug/pprof/goroutine?debug=1' | head -1
curl -s http://127.0.0.1:18082/debug/vars | grep -o '"db_pool": *{[^}]*}'
curl -o cpu.out 'http://127.0.0.1:18082/debug/pprof/profile?seconds=10'
curl -o heap.out http://127.0.0.1:18082/debug/pprof/heap

# profile 分析（本机无 Go 时借容器）
docker run --rm -i golang:1.26-alpine sh -c 'cat > /tmp/p.out && go tool pprof -top /tmp/p.out' < cpu.out
docker run --rm -i golang:1.26-alpine sh -c 'cat > /tmp/p.out && go tool pprof -top -inuse_space /tmp/p.out' < heap.out
docker run --rm -i golang:1.26-alpine sh -c 'cat > /tmp/p.out && go tool pprof -top -alloc_space /tmp/p.out' < heap.out

# 压测（四模式：hit/miss/fallback/ratelimit；25 用户轮转分摊限流）
./docs/experiments/scripts/e13_loadgen.exe -mode hit -c 16 -n 200 \
  -tokens-file experiments/support/e13-tokens.txt -label e13-hit -csv out.csv

# race / benchmark（Windows 本机无 gcc，容器内执行，详见 E13-11）
go test -race ./...
go test -run '^$' -bench . -benchmem ./...

# 依赖侧
docker run --rm --network aigateway-experiment_default redis:7-alpine \
  redis-cli --latency -h redis -i 1
docker exec aigateway-experiment-redis redis-cli INFO stats
docker stats --no-stream
```
