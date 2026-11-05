# 阶段 1：可控上游和测量基线

> 实验编号：E1 + E13
> 状态：E1-01~E1-09 已执行，E13 执行中（全部观测完成：顺序+并发基线、profile、race/bench/RTT/heap；余变体【暂缓】、迁移结论）
> 关联计划：`docs/experiments/nerox-gateway-experiment-plan.md`
> 执行顺序：先完成 E1，再用 E1 已验证的正常场景完成 E13

## 这次实验要干什么

阶段 1 要先把上游 Provider 变成一个**可以按条件控制的本地假服务**，再用它建立一组可以重复的网关测量基线。

这次实验不是为了马上优化代码，也不是为了证明 README 中的 QPS 数字。它要回答两个问题：

1. **E1：上游发生不同事情时，网关实际怎么反应？** 例如响应头慢、响应体慢、返回 429/401/500、输出一部分后断开时，网关会等待、换 Key、切换 Provider、返回错误，还是把请求当成成功处理。
2. **E13：在路径和数据都固定后，网关的基线成本是多少？** 缓存命中、缓存未命中、限流拦截和实际访问 Provider 的路径，吞吐、尾延迟、CPU、内存、goroutine、Redis 和数据库等待分别是多少。

E1 负责证明“故障注入确实可控”，E13 使用同一个 Fake Provider 的稳定正常响应来测量。两者共用环境，但不把故障请求和正常性能基线混在一组数据里。

## 本次实验的边界

### 要验证

- `ResponseHeaderTimeout` 覆盖响应的哪一段。
- 响应头已经到达后，慢速 body 是否仍可长期占用请求。
- 429/401 是否切换同一 Provider 的下一个 Key。
- 500 或请求级失败是否切换到备用 Provider。
- 已经输出部分 SSE 后断流，客户端最终看到什么。
- 超过 `bufio.Scanner` 默认单行上限的 SSE 行会造成什么结果。
- 同一请求路径下，缓存命中、缓存未命中、限流拦截和 Provider 请求的性能差异。
- pprof、race、benchmark 和简单压测能否提供解释现象所需的证据。

### 本次不验证

- 真实 Provider 的公网 DNS、TLS、丢包、拥塞和真实配额策略。
- 真实生产流量下的容量上限。
- 大规模分布式部署和多地域网络。
- 在没有 profile 证据前进行业务代码重构或性能优化。

## 实验前准备

- 使用本地或隔离环境，不连接真实 Provider，不使用有效的生产 API Key。
- 启动 MySQL、Redis、Gateway 和 Fake Provider。
- 配置 Gateway 的主 Provider 和备用 Provider 都指向 Fake Provider 的不同场景入口；使用 `ALLOW_INSECURE_PROVIDER_HTTP=1` 仅允许本地 HTTP 测试。
- 准备一个余额充足的测试用户、一个测试 Gateway Token，以及足够的测试 Key。所有 Key 都使用占位值。
- 记录 Go、Docker、MySQL、Redis、操作系统、CPU、内存和 Git commit。
- 每个请求使用唯一的消息内容，避免命中上一条内容缓存；每分钟同一用户请求数不要超过当前限流值 10 次。
- 固定请求体、并发数、样本量、运行时长和 Fake Provider 响应内容。
- 一次只改变一个主要变量。组合故障留到后续实验。

## 前置环境状态（2026-10-28）

前置栈已经启动；E1 故障矩阵已完成，E13 已开始进行单请求基线采集：

- Docker Engine `29.2.0`，Docker Compose `v5.0.2`。
- Gateway：`http://127.0.0.1:18080`。
- Fake Provider：`http://127.0.0.1:18081`。
- MySQL 和 Redis 只在 Compose 内部网络可访问，避免占用宿主机已有的 `3306` 和 `6379`。
- Fake Provider 的控制接口：`GET /__test/health`、`POST /__test/reset`、`GET /__test/stats`、`POST /__test/config`。
- 本地配置：`experiments/support/config.json`，仅包含占位 Provider Key。
- 本地凭据：`experiments/support/.env.local`，已被 Git 忽略，不写入实验结果。

启动或恢复环境：

```bash
set -a
source experiments/support/.env.local
set +a
docker compose --env-file experiments/support/.env.local -f experiments/support/docker-compose.yml up -d
docker compose --env-file experiments/support/.env.local -f experiments/support/docker-compose.yml ps
```

停止环境：

```bash
set -a
source experiments/support/.env.local
set +a
docker compose --env-file experiments/support/.env.local -f experiments/support/docker-compose.yml down
```

切换 Fake Provider 场景：

```bash
FAKE=http://127.0.0.1:18081

# 每个正式用例开始前清空调用记录
curl -fsS -X POST "$FAKE/__test/reset"

# 恢复 primary 为正常响应
curl -fsS -X POST "$FAKE/__test/config" \
  -H 'Content-Type: application/json' \
  --data '{"provider":"primary","scenario":"normal"}'

# primary 的第一次调用返回 429，后续调用恢复正常，用于观察换 Key
curl -fsS -X POST "$FAKE/__test/config" \
  -H 'Content-Type: application/json' \
  --data '{"provider":"primary","scenario":"normal","fail_first":1,"status_code":429}'

# primary 返回 500，fallback 保持正常，用于观察切换 Provider
curl -fsS -X POST "$FAKE/__test/config" \
  -H 'Content-Type: application/json' \
  --data '{"provider":"primary","scenario":"500"}'
curl -fsS -X POST "$FAKE/__test/config" \
  -H 'Content-Type: application/json' \
  --data '{"provider":"fallback","scenario":"normal"}'

# 响应头延迟、首个 chunk 延迟、chunk 间隔延迟
curl -fsS -X POST "$FAKE/__test/config" -H 'Content-Type: application/json' \
  --data '{"provider":"primary","scenario":"header-delay","header_delay_ms":6000}'
curl -fsS -X POST "$FAKE/__test/config" -H 'Content-Type: application/json' \
  --data '{"provider":"primary","scenario":"first-chunk-delay","first_chunk_delay_ms":6000}'
curl -fsS -X POST "$FAKE/__test/config" -H 'Content-Type: application/json' \
  --data '{"provider":"primary","scenario":"chunk-delay","chunk_delay_ms":6000}'
```

每次切换场景后用 `GET /__test/stats` 确认配置，再发 Gateway 请求。场景切换命令本身不计入 E1 用例；正式结果只记录 Gateway 请求产生的调用记录。

准备阶段的冒烟请求已经确认 Gateway 能调用 Fake Provider，并在 MySQL/Redis 链路中完成一次请求；这次请求不计入 E1-01 正式结果。冒烟输出出现了 `data: data:`，说明当前 Gateway 转发 SSE 行时可能重复添加 `data:` 前缀，留待 E1-01 记录和解释。

## 观察证据链

每个用例都按下面的链路收集证据：

```text
实验客户端
  -> Gateway 客户端看到的 HTTP/SSE 和时间
  -> Fake Provider 收到的请求、场景、Key、调用次数和发送时间
  -> Gateway 日志中的错误、重试和 Provider 信息
  -> MySQL request_logs、用户余额
  -> Redis 内容缓存和限流计数
```

单看 HTTP 状态码不算完成。每个场景至少要能回答：客户端收到了什么、Fake Provider 被调用了几次、Gateway 记录了什么、数据库和 Redis 是否发生了变化。

建议的现场观察方式：

```bash
# 终端 1：持续看 Gateway 日志
docker compose --env-file experiments/support/.env.local -f experiments/support/docker-compose.yml logs -f app

# 终端 2：保持 SSE 原样输出，并记录请求时间
curl -N --max-time 60 \
  -w '\nstatus=%{http_code} ttfb=%{time_starttransfer}s total=%{time_total}s\n' \
  -H "Authorization: Bearer $GATEWAY_TEST_TOKEN" \
  -H 'Content-Type: application/json' \
  --data-raw '{"model":"<test-model>","messages":[{"role":"user","content":"<unique-case-id>"}]}' \
  "$GATEWAY_URL/api/v1/chat"

# 终端 3：查询 Fake Provider 的统计接口（由 E1 Fake Provider 实现）
curl "$FAKE_PROVIDER_URL/__test/stats"
```

请求结束后等待异步日志写入，再查询：

```sql
SELECT id, target_provider, target_model, status_code, latency,
       total_tokens, error_message
FROM request_logs
WHERE user_id = <test-user-id>
ORDER BY id DESC
LIMIT 20;

SELECT id, balance FROM users WHERE id = <test-user-id>;
```

如果需要确认某条日志是否来自缓存，先执行 `DESCRIBE request_logs;`，再按实际列名查询 `CacheHIT` 对应字段；缓存路径也可以直接通过 Fake Provider 调用次数为 0 和 Redis 内容缓存命中来确认。

Redis 侧至少记录内容缓存是否出现、限流计数是否增加，以及请求结束后相关 Key 是否仍存在：

```bash
docker compose --env-file experiments/support/.env.local -f experiments/support/docker-compose.yml exec redis redis-cli --scan --pattern 'cache:content:*'
docker compose --env-file experiments/support/.env.local -f experiments/support/docker-compose.yml exec redis redis-cli --scan --pattern 'ratelimit:*'
```

## TTFB 口径

本文的 TTFB 都使用客户端 `curl` 的 `time_starttransfer`：从客户端开始请求，到客户端收到 Gateway 返回的第一个响应字节。公式相同，但第一个字节代表的事件随路径不同：

- SSE 正常响应：Gateway 发出的第一个 `data:` 帧，不是 Fake Provider 的响应头到达时间。
- fallback：包含前一个 Provider 的等待或失败处理，到 Gateway 发出 fallback 的第一个 `data:` 帧为止。
- 限流：Gateway 发出的第一个 `429` JSON 字节；请求体虽然已经发到 Gateway，但不会继续解析、查内容缓存或访问 Provider。
- 缓存命中：Gateway 生成的第一个缓存 SSE 帧。
- `total` 包含后续 SSE 帧、`[DONE]` 和连接结束；TTFB 不能代替总耗时。

## E1：Fake Provider 响应矩阵

### 代码侧预读结论

- Gateway 的 Transport 设置了 `ResponseHeaderTimeout: 5s`，`http.Client` 没有设置总超时。
- 响应头到达后，当前 body 读取没有独立的 idle timeout；如果 Fake Provider 已发送响应头但很久不发 body，请求可能继续占用连接。
- Provider 返回 429 或 401 时，`StreamOnce` 返回 `ErrRetryNextKey`，预期继续尝试同一 Provider 的下一个 Key。
- 其他非 200 响应（例如 500）返回 `ErrRetryNextModel`，预期跳到 fallback Provider。
- 已经向客户端写出部分 SSE 后，如果读取 body 失败，当前代码写入一个错误 SSE 和 `[DONE]`，不会重新切换 Provider。
- `bufio.Scanner` 默认单行上限约为 64KB。超长 SSE 行会进入 `scanner.Err()`；当前调用链把它作为 `ErrSuccess` 返回，需要观察这是否导致错误响应、日志、缓存和扣费状态不一致。
- 网关只有在 `content` 非空时才写入内容缓存。断流或 scanner 错误是否留下缓存，必须通过 Redis 实际查询确认。

这些是跑实验前根据源码写下的预测，不是实验结果。

### Fake Provider 最小能力

Fake Provider 放在 `experiments/support/fakeprovider`，至少需要支持：

- 正常 SSE 响应。
- 响应头延迟。
- 已发送响应头后，第一个 chunk 延迟。
- 每个 chunk 之间延迟。
- 返回 429、401、500。
- 输出若干 chunk 后断开连接。
- 返回超过 64KB 的单行 SSE。
- 记录 request ID、Authorization、场景、调用次数、响应时间和每个 chunk 的发送时间。
- 提供清空统计和查询统计的测试接口，避免不同用例的调用次数混在一起。

### E1 用例表

| 用例 | 注入条件 | 客户端要看什么 | Fake Provider 要看什么 | Gateway/数据要看什么 |
|---|---|---|---|---|
| E1-01 正常响应 | 立即返回完整 SSE | 是否收到完整内容和 `[DONE]`；TTFB 和总耗时 | 调用次数、Key、chunk 时间 | 是否成功记日志、是否写内容缓存、是否扣费 |
| E1-02 响应头超时 | 响应头延迟超过 5 秒 | 约何时失败；是否收到错误或 fallback 内容 | 主 Provider 是否收到请求；是否有后续调用 | 重试类型、调用次数、日志状态 |
| E1-03 首个 chunk 很慢 | 先发送响应头，再等待超过 5 秒发送 body | 是否一直等待；实际 chunk 间隔 | 连接是否仍保持；是否发送了 chunk | 是否出现 timeout；goroutine/连接是否持续 |
| E1-04 chunk 间隔很慢 | 每个 chunk 间隔超过 5 秒 | 是否仍能陆续收到内容 | 每个 chunk 的发送时间 | 请求持续时间、资源是否回落 |
| E1-05 429 换 Key | 第一个 Key 返回 429，第二个 Key 正常 | 最终是否收到正常 SSE | 同一 Provider 是否调用两次，使用的 Key 是否不同 | 每次尝试的日志状态 |
| E1-06 401 换 Key | 第一个 Key 返回 401，第二个 Key 正常 | 最终是否收到正常 SSE | 是否切换到下一个 Key | 是否把 401 当作 Key 失败而不是 Provider 失败 |
| E1-07 500 换 Provider | 主 Provider 返回 500，备用 Provider 正常 | 是否收到备用 Provider 的内容 | 两个 Provider 各调用几次 | 主失败和备用成功是否各有日志 |
| E1-08 部分输出后断流 | 输出若干 chunk 后主动断开 | 是否只收到部分内容；是否收到 gateway error 和 `[DONE]` | 是否只完成一次调用 | 是否 fallback；是否缓存残缺内容；是否扣费 |
| E1-09 超长 SSE 行 | 单行超过 64KB | 是否收到部分内容或错误事件 | 请求是否只调用一次 | scanner 错误、缓存、usage、余额和日志状态 |

每个用例执行前先清空 Fake Provider 统计，并把 `<unique-case-id>` 写入记录。每个用例至少保留客户端原始输出、Fake Provider 统计和一条数据库查询结果。

### E1 定量预测

在执行前填写，执行后不得改写原预测：

```text
E1-01 正常响应：预期 Provider 调用 _1_ 次，TTFB _？_ ms，总耗时 _？_ ms。
E1-02 响应头超时：预期首个失败发生在 _5_ s，fallback/重试调用 _1_ 次。
E1-03 首个 chunk 很慢：预期 Gateway 是否在 _5？_ s 内超时：是✅ / 否。
E1-04 慢速 chunk：预期相邻 chunk 最大间隔 _？_ s，请求是否保持：是✅ / 否。
E1-05 429 换 Key：预期同一 Provider 调用 _1_ 次，最终状态 _成功_。
E1-06 401 换 Key：预期同一 Provider 调用 _1_ 次，最终状态 _成功_。
E1-07 500 换 Provider：预期主/备用 Provider 调用次数为 _1_ / _1_。
E1-08 部分断流：预期收到完整内容：是 / 否✅；预期写缓存：是✅ / 否；预期扣费：是 / 否✅。
E1-09 超长 SSE：预期 scanner 错误后 HTTP/SSE 表现为 _直接断开_；预期写缓存：是 / 否✅。
```

### E1 结果记录

```text
执行时间：
代码版本或 Git commit：
Fake Provider 版本：
环境：

实验前假设：
代码侧预读结论：见上文
定量预测：见上文，执行前已填写
测试配置：

E1-01 正常响应：
  客户端结果：
  HTTP 200，TTFB 52.997ms，总耗时 61.105ms。
  TTFB 口径：客户端请求开始 -> Gateway 发出的第一个 SSE `data:` 字节。
  收到 2 个 SSE 内容块，但内容显示为 data: data:，没有看到 [DONE]。

  Provider 调用次数：
  primary 1 次，fallback 0 次，使用 fake-primary-key-1。

  MySQL：
  request_logs 状态码 200，latency 14ms，total_tokens=3，错误为空。
  余额从 100000 变为 99999。

  Redis：
  写入 cache:content:f6d0df1a15449e143faeb194560a27b0，
  value 为 fake response，TTL 3430 秒。

  初步结论：
  正常请求能调用 primary Provider、解析 usage、写入缓存并扣费。
  但 Gateway 转发 SSE 时重复添加 data: 前缀，客户端没有收到 [DONE]。

E1-02 响应头超时：
  客户端结果：
  HTTP 200，TTFB 5.047s，总耗时 5.063s；等待约 5 秒后收到 fallback 内容，仍显示为 data: data:，没有看到 [DONE]。
  TTFB 口径：客户端请求开始 -> primary 超时并切换后，Gateway 发出的第一个 fallback SSE 字节；包含约 5 秒等待。

  Provider 调用次数：
  primary 1 次（header-delay，fake-primary-key-2），fallback 1 次（fake-fallback-key-1）。

  MySQL：
  primary 状态码 500，latency 5012ms，total_tokens=0；fallback 状态码 200，latency 5ms，total_tokens=3。
  余额从 99999 变为 99998。

  Redis：
  fallback 内容写入 cache:content:575f625ceb8df62e0cb4ea022f540319，value 为 fake response。

  初步结论：
  Gateway 的响应头超时约为 5 秒；primary 超时后切换 fallback 并成功返回。primary Fake Provider 仍运行到 6 秒才结束，说明上游请求没有随 Gateway 超时立即结束。

E1-03 首个 chunk 很慢：
  客户端结果：
  HTTP 200，TTFB 6.020s，总耗时 6.027s；收到 delayed 内容，没有收到 [DONE]。
  TTFB 口径：客户端请求开始 -> Gateway 发出的第一个 delayed SSE 字节；包含等待首个 body chunk 的时间。

  Provider 调用次数：
  primary 1 次（first-chunk-delay，fake-primary-key-1），fallback 0 次。

  MySQL：
  request_logs 状态码 200，latency 6008ms，total_tokens=0，错误为 usage 没收到。
  余额保持 99998。

  Redis：
  写入内容缓存，value 为 delayed。

  初步结论：
  响应头已经到达，但 Gateway 没有 body 空闲超时，会继续等待首个 chunk。
  记录说明：本场景实现只发送 delayed 内容和 [DONE]，没有发送 usage，因此同时改变了“首个 chunk 延迟”和“usage 缺失”两个变量。本次不重跑；等待行为的结论有效，但 total_tokens=0、不扣费和缓存 delayed 只能说明该场景缺少 usage，不能归因于首个 chunk 延迟本身。

E1-04 chunk 间隔很慢：
  客户端结果：
  HTTP 200，TTFB 6.015s，总耗时 12.037s；两个内容块都收到，没有收到 [DONE]。
  TTFB 口径：客户端请求开始 -> Gateway 发出的第一个 SSE 字节；后续 chunk 间隔只计入总耗时。

  Provider 调用次数：
  primary 1 次（chunk-delay，fake-primary-key-2），fallback 0 次。

  MySQL：
  request_logs 状态码 200，latency 12013ms，total_tokens=3，错误为空。
  余额从 99998 变为 99997。

  Redis：
  写入内容缓存，value 为 fake response。

  初步结论：
  chunk 之间没有 idle timeout，请求持续等待 12 秒后成功完成并扣费。

E1-05 429 换 Key：
  客户端结果：
  HTTP 200，TTFB 10.801ms，总耗时 18.474ms；最终收到正常内容，没有收到 [DONE]。
  TTFB 口径：客户端请求开始 -> 第二个 Key 成功后，Gateway 发出的第一个 SSE 字节；中间的 429 不单独返回给客户端。

  Provider 调用次数：
  primary 2 次：fake-primary-key-1 返回 429，fake-primary-key-2 返回 200；fallback 0 次。

  MySQL：
  两条记录分别为 429/0 tokens 和 200/3 tokens；余额从 99997 变为 99996。

  Redis：
  写入内容缓存，value 为 fake response。

  初步结论：
  429 会切换同一 Provider 的下一个 Key，第二次调用成功，最终只扣一次费。

E1-06 401 换 Key：
  客户端结果：
  HTTP 200，TTFB 6.508ms，总耗时 13.832ms；最终收到正常内容，没有收到 [DONE]。
  TTFB 口径：客户端请求开始 -> 第二个 Key 成功后，Gateway 发出的第一个 SSE 字节；中间的 401 不单独返回给客户端。

  Provider 调用次数：
  primary 2 次：fake-primary-key-1 返回 401，fake-primary-key-2 返回 200；fallback 0 次。

  MySQL：
  两条记录分别为 429/0 tokens 和 200/3 tokens；余额从 99996 变为 99995。

  Redis：
  写入内容缓存，value 为 fake response。

  初步结论：
  401 也会切换下一个 Key，但 Gateway 的失败日志把它记成了 429。

E1-07 500 换 Provider：
  客户端结果：
  HTTP 200，TTFB 20.417ms，总耗时 36.238ms；收到 fallback 的正常内容，没有收到 [DONE]。
  TTFB 口径：客户端请求开始 -> fallback Provider 成功后，Gateway 发出的第一个 SSE 字节；包含 primary 失败和切换时间。

  Provider 调用次数：
  primary 1 次（500，fake-primary-key-1），fallback 1 次（200，fake-fallback-key-2）。

  MySQL：
  primary 状态码 500，total_tokens=0；fallback 状态码 200，total_tokens=3。
  余额从 99995 变为 99994。

  Redis：
  写入内容缓存，value 为 fake response。

  初步结论：
  500 会放弃当前 Provider 并切换 fallback，fallback 成功后只扣一次费。

E1-08 部分输出后断流：
  客户端结果：
  HTTP 200，TTFB 7.061ms，总耗时 9.310ms；先收到 partial，随后收到 gateway_error 和 [DONE]。
  TTFB 口径：客户端请求开始 -> Gateway 发出的第一个 partial SSE 字节；后续错误事件和 `[DONE]` 只计入总耗时。

  Provider 调用次数：
  primary 1 次（partial，fake-primary-key-2），fallback 0 次；Fake Provider 记录 intentional disconnect。

  MySQL：
  request_logs 状态码 200，latency 5ms，total_tokens=0，错误为 usage 没收到。
  余额保持 99994。

  Redis：
  没有写入本次残缺内容缓存。

  初步结论：
  已输出部分内容后断流不会 fallback；Gateway 会补发错误事件和 [DONE]，但仍按 HTTP 200 记日志，不扣费。

E1-09 超长 SSE 行：
  客户端结果：
  HTTP 200，TTFB 7.406ms，总耗时 7.427ms；收到 gateway_error 和 [DONE]。
  TTFB 口径：客户端请求开始 -> Gateway 发出的第一个错误 SSE 字节。

  Provider 调用次数：
  primary 1 次（long-line，fake-primary-key-1），fallback 0 次。

  MySQL：
  request_logs 状态码 200，latency 3ms，total_tokens=0，错误为 usage 没收到。
  余额保持 99994。

  Redis：
  没有写入内容缓存。

  初步结论：
  超过 Scanner 默认约 64KB 单行限制后，Gateway 返回错误事件和 [DONE]，但 HTTP 和日志仍为成功，不 fallback，也不扣费。

客户端原始输出或文件：
Fake Provider 调用记录：
Gateway 日志：
MySQL request_logs：
用户余额变化：
Redis 状态变化：
goroutine、内存、fd、TCP 连接：

预测与实际差异：
假设是否成立：
源码证据：
是否发现代码缺陷：
是否需要修复：
修复方案和替代方案：
实验局限：
```

### E1 发现的问题（先记录，不展开修复）

1. **SSE 结束标记和格式转发异常**：Gateway 消费了上游的 `[DONE]`，没有转发给客户端；普通内容还出现了重复的 `data:` 前缀。
2. **响应头超时不能保证上游处理停止**：Gateway 约 5 秒后切换 fallback，但 Fake Provider 的 handler 仍继续执行，可能造成上游并发、资源和配额额外消耗。
3. **响应体没有空闲超时**：chunk 间隔很长时请求持续等待；如果上游一直不再发送数据，连接可能长期占用资源。
4. **401 的日志状态错误**：401 和 429 都会换 Key，但 401 的 `request_logs.status_code` 被记成了 429。
5. **流中途错误被记成成功**：E1-08、E1-09 的客户端收到了 `gateway_error`，但 HTTP 和 `request_logs` 仍显示 200。
6. **E1-03 场景变量不够单一**：Fake Provider 没有发送 usage，因此“不扣费”和 `total_tokens=0` 不能归因于首个 chunk 延迟。

## E13：pprof、race、benchmark 和压测基线

### 这部分要干什么

E13 不测故障恢复，而是把请求路径固定下来，回答“网关的时间和资源花在哪里”。至少分别建立以下四条基线：

1. **缓存命中**：请求内容已经在 Redis，Gateway 不访问 Provider。
2. **缓存未命中**：请求内容不在 Redis，Gateway 访问 Fake Provider 并写入缓存。
3. **限流拦截**：超过每分钟限流值，Gateway 在访问 Provider 前拒绝请求。
4. **Provider 路径**：每次使用新的请求内容，确保实际访问 Fake Provider。

每条基线都必须固定请求体、并发数、运行时间、Fake Provider 响应和数据准备方式；否则只能比较不同实验条件，不能比较代码路径。

每条基线至少记录：客户端 HTTP/SSE、TTFB、总耗时；Fake Provider 调用次数和 Key；Redis 命中或写入；MySQL 日志、延迟和 `cache_hit`；用户余额变化。批量或并发阶段再记录 P50/P95/P99、吞吐量和资源变化。

### E13 观测内容

- 请求数、吞吐、错误率、P50/P95/P99、首字节延迟和总耗时。
- CPU、heap、allocs、GC、goroutine 和阻塞 profile。
- Redis 命中率、命令延迟和错误率。
- MySQL 连接池使用、等待和慢查询。
- Fake Provider 调用次数，确认 benchmark 没有误走缓存或限流路径。
- race detector 是否报告共享状态问题。

当前 `main.go` 虽然导入了 `net/http/pprof`，但网关 HTTP Server 使用的是 Gin router；在没有显式注册 pprof 路由前，`GET /debug/pprof/` 预期返回 404。E13 的第一项观测是记录这个事实，然后再通过独立端口或受保护路径挂出诊断入口，重新采集 profile。诊断入口不得暴露到公开网络。

### E13 结果记录

```text
执行时间：2026-10-29
代码版本或 Git commit：
环境：Docker Compose；Gateway 通过 Rosetta 运行 amd64 二进制；空载 app 内存约 71 MiB
固定请求体和响应：model=e1-model；Fake Provider=normal；响应内容为 fake response
统一 TTFB：见上文；以下均为客户端到 Gateway 的首字节时间。

E13-01 缓存未命中：
  请求：`e13-cache-20261029-02`。
  TTFB 口径：客户端请求开始 -> Gateway 转发的第一个 Provider SSE 字节。
  客户端：HTTP 200，TTFB 90.043ms，总耗时 100.683ms；收到内容，但出现 `data: data:`，没有 `[DONE]`。
  Fake Provider：primary 1 次，fallback 0 次；status=200，chunks=2。
  Redis：写入 `cache:content:d5b96f79d17e6219c04ace1eaea7a677`，值为 `fake response`，TTL=3694 秒。
  MySQL：request_logs id=18，status_code=200，latency=4ms，total_tokens=3，cache_hit=0。
  余额：扣除 1 次；随后查询余额为 99991。
  结论：请求未命中缓存，访问 Provider、写入内容缓存并扣费。

E13-02 缓存命中：
  请求：原样重复 `e13-cache-20261029-02`。
  TTFB 口径：客户端请求开始 -> Gateway 生成的第一个缓存 SSE 字节。
  客户端：HTTP 200，TTFB 14.634ms，总耗时 14.723ms；收到 role、content、finish_reason 和完整 `[DONE]`，响应 ID 为 `chatcmpl-cache`。
  Fake Provider：primary 仍为 1 次，fallback 0 次；本次没有新增回源。
  Redis：同一 key 仍存在，TTL=3558 秒。
  MySQL：request_logs id=19，status_code=200，latency=0ms，total_tokens=0，cache_hit=1，target_provider 为空。
  余额：99991 -> 99991。
  结论：请求走 Gateway 内容缓存，不访问 Provider、不扣费，并正确发送 `[DONE]`。

E13-03 限流拦截：
  请求：同一分钟发送 `e13-rate-20261029-01` 至 `-11` 共 11 条不同消息。
  TTFB 口径：前 10 次为第一个 Provider SSE 字节；第 11 次为 Gateway 返回的第一个 429 JSON 字节。
  客户端：第 1~10 次 HTTP 200，TTFB 1.475~21.833ms；第 11 次 HTTP 429，TTFB 12.774ms、总耗时 12.820ms，无 SSE 内容。
  Fake Provider：primary 10 次，fallback 0 次；第 11 次没有到达 Provider，两把 Key 交替使用。
  Redis：`ratelimit:1:29887989` 的值为 11，查询时 TTL=26 秒。
  MySQL：新增 id=20~29 共 10 条 status_code=200、cache_hit=0 的日志；没有第 11 次日志。
  余额：99991 -> 99981，共扣除 10 次。
  结论：限流在访问 Provider 前生效；第 11 次未回源、未写成功日志、未扣费，但会增加 Redis 计数并消耗当前窗口额度。

E13-04 Provider 回源（复用 E13-03 前 10 次）：
  请求：`e13-rate-20261029-01` 至 `-10`，每条消息唯一且未命中缓存。
  TTFB 口径：客户端请求开始 -> Gateway 转发的第一个 Provider SSE 字节。
  客户端：10/10 HTTP 200；TTFB 1.475~21.833ms，总耗时 3.588~29.863ms。本批脚本把响应体写入 `/dev/null`，只记录状态和时间。
  Fake Provider：primary 10 次，fallback 0 次；每次 status=200、chunks=2。
  MySQL：10 条 `cache_hit=0`、status_code=200 的日志。
  结论：低并发、normal Fake Provider 下，10 次回源均成功；该小样本不作为容量或 P95/P99 结论。

E13-05 缓存未命中顺序延迟（20 样本）：
  测试方式：每次使用唯一消息，测试前删除当前用户的限流 key；顺序发送 20 次，响应体写入 `/dev/null`。
  TTFB：平均 2.930ms，P50 2.093ms，P95/P99 2.782ms，范围 1.704~18.393ms。
  总耗时：平均 5.100ms，P50 4.253ms，P95/P99 5.231ms，范围 3.470~22.330ms。
  客户端：20/20 HTTP 200。
  Fake Provider：primary 20 次，fallback 0 次；每次 status=200、chunks=2。
  MySQL/余额：新增 request_logs id=51~70，全部 `cache_hit=0`、`total_tokens=3`；余额 `99980 -> 99960`。
  结论：唯一请求确实走 Provider 回源路径。首个样本 TTFB=18.393ms、总耗时=22.330ms，明显高于其余样本；当前只能记录为离群样本，不能据此判断具体瓶颈。

E13-06 限流拒绝顺序延迟（20 样本）：
  测试方式：直接把当前窗口的 Redis 计数预置为 10，再发送 20 条请求，避免用额外请求消耗余额来预热限流。
  TTFB：平均 1.982ms，P50 1.558ms，P95/P99 3.191ms，范围 1.218~5.109ms。
  总耗时：平均 2.029ms，P50 1.604ms，P95/P99 3.249ms，范围 1.258~5.153ms。
  客户端：20/20 HTTP 429。
  Fake Provider：0 次调用。
  Redis：计数 `10 -> 30`，查询时 TTL=24 秒；每个被拒绝请求仍会增加计数。
  MySQL/余额：没有新增 request_logs；余额保持 `99960`。
  结论：限流拒绝路径只经过 Gateway 的限流检查和 429 响应，不解析缓存、不访问 Provider、不扣费。预置 Redis 计数是实验准备动作，不代表正常业务请求流程。

附带观测：`GET /debug/pprof/` 返回 HTTP 404；当前 Gin 服务没有注册 pprof 路由。本项只记录，不在本轮修复。（2026-10-31 已作为 E13-07 最小修复并复验，见下）

暂未测（2026-11-01 更新）：go test -race、go test -bench 复验、Redis RTT、MySQL 内部状态、heap profile 分析、block profile 有负载读取、计划外变体。P50/P95/P99 数据源：20 个顺序样本 + 17 轮×200 条突发样本。

E13-07 诊断入口挂出（E13 允许的最小修复）与复验（2026-10-31）：
  背景：`main.go` 导入 `net/http/pprof` 但 `DefaultServeMux` 从未被服务，`GET /debug/pprof/` 返回 404（见上方附带观测）。
  环境说明：本日实验环境全新重建（原镜像、数据卷、本地凭据均不在），测试用户与 Token 经 admin 接口重建（user_id=1，余额 100000）。
  修复内容：`main.go` 在独立端口 `:6060` 服务 `DefaultServeMux`（pprof + expvar），并发布 `db_pool`（`sql.DB.Stats()`）；
  同时开启阻塞采样 `runtime.SetBlockProfileRate(1)`（Go 默认关闭，不开启则 block profile 恒为空）；
  实验 compose 增加 `127.0.0.1:18082:6060` 映射，仅回环暴露。业务 gin router 与主路径代码不变。
  修复前证据：`/debug/pprof/` 404、`/debug/vars` 404、宿主机 18082 无监听。
  修复后：`/debug/pprof/` 200；`/debug/vars` 含 memstats、cmdline、db_pool（MaxOpen=100，Open=1，Idle=1，WaitCount=0）；
  goroutine profile 空载 total 14。
  主路径复验（同 E13-02 缓存命中场景）：
    第 1 次唯一消息（未命中）：HTTP 200，TTFB 13.565ms；request_logs cache_hit=0、target_provider=fake-primary、total_tokens=3；余额 -1。
    第 2 次原样重发（命中）：HTTP 200，TTFB 4.898ms，返回 `chatcmpl-cache` 与完整 `[DONE]`；request_logs cache_hit=1；Fake Provider 无新增调用；不扣费。
    对比 E13-02（TTFB 14.634ms）：命中路径行为一致、延迟同量级，诊断端口对主路径无可观察影响。
    goroutine 复验后 total 16，空载未见增长。
  实验准备动作记录：清空 Fake Provider 统计；删除 `ratelimit:*` key（准备动作，非正常业务流程）。
  定量预测：未做——首次建立基线前无法外推，本轮明确跳过；并发阶段是否补做由后续记录说明。
  发现：容器内监听须绑 `:6060` 而非 `127.0.0.1:6060`，否则 Compose 端口映射无法转发到容器回环地址；对外隔离由 compose 的 `127.0.0.1` 发布地址保证。

E13-08 并发基线矩阵（2026-11-01）：
  工具：docs/experiments/scripts/e13_loadgen.go（-mode hit|miss|fallback|ratelimit，-c 并发，-n 请求数，
  -tokens-file 多用户轮转，-csv 明细），编译为 e13_loadgen.exe 运行。
  限流应对：单用户限流为硬编码 10 次/60 秒固定窗口（storage/redis.go:172），业务代码不可改；
  建立 25 个测试用户（e13-u01~u25，各余额 100000）轮转 Token，轮间清空 ratelimit:*（准备动作）。
  固定条件：model=e1-model；每轮 n=200 突发；hit 轮复用同一缓存内容，miss/fallback 每请求唯一内容；
  ratelimit 轮预置 user1 当前窗口计数到 10。路径 x 并发（1/4/16/64）共 16 轮 + rl_c4 重跑 1 轮。
  结果表（吞吐 req/s | TTFB P50/P95 ms | Total P50/P95 ms | goroutine 峰值 | WaitCount 峰值）：
    hit c1     |  332 |  2.64/ 5.30 |  2.67/ 5.37 |  34 | 0
    hit c4     | 1379 |  2.70/ 4.01 |  2.73/ 4.01 |  42 | 0
    hit c16    | 2728 |  4.86/ 8.05 |  5.08/ 8.78 |  52 | 0
    hit c64    | 1765 | 14.26/96.41 | 15.74/96.41 | 111 | 0
    miss c1    |   88 |  4.08/ 6.06 | 10.98/15.72 |  35 | 0
    miss c4    |  281 |  4.36/ 7.01 | 13.60/20.45 |  46 | 0
    miss c16   |  737 |  8.05/14.80 | 20.95/28.45 |  52 | 0
    miss c64   |  989 | 13.72/67.46 | 48.65/108.11| 203 | 0
    fb   c1    | 78.7 |  5.50/ 8.37 | 12.05/17.28 |  41 | 0
    fb   c4    |  265 |  6.00/ 8.78 | 14.42/20.89 |  45 | 0
    fb   c16   |  637 |  9.88/22.25 | 24.36/33.17 |  72 | 0
    fb   c64   |  880 | 21.75/70.20 | 63.87/126.95| 253 | 0
    rl   c1    |  430 |  2.11/ 3.89 |  2.12/ 3.89 |  39 | 0
    rl   c4    | 1389 |  2.63/ 4.10 |  2.63/ 4.59 |  42 | 0（重跑轮；首跑混入 9 个 200，见下）
    rl   c16   | 2937 |  3.87/17.37 |  3.99/18.00 |  38 | 0
    rl   c64   | 3383 | 13.80/54.13 | 13.88/54.13 |  38 | 0
  关键读数：
    1. hit 吞吐拐点在 c16（2728 req/s）：c64 并发×4 但吞吐反跌至 1765，且 P95 从 8ms 涨至 96ms
       （P95/P50 比值从 1.6 变 6.8）——并发超过处理能力后排队，两信号同时出现。
    2. miss/fallback 天花板为上游 I/O：TTFB(4ms) << Total(11ms)，差值是等上游流输出；
       fallback 比 miss 慢一档（c1 79 vs 88），对应每请求多一次失败往返。
    3. ratelimit 是最快路径（c64 3383 req/s）：不碰 Provider、不写库、不扣费。
    4. goroutine 峰值 ≈ 15 + 1.5×并发；miss c64 达 203、fallback c64 达 253（每在途请求挂上游连接读写 goroutine）。
    5. db_pool WaitCount 全程 0：池上限 100，负载最多用到 20 条连接，从未排队。
       E8 观察连接池排队需先调小池子。
    6. rl_c4 首跑出现 9 个 200：60 秒固定窗口在轮间翻页（计数随窗口滚动重置），重跑后干净。
  Fake Provider 记账：miss 轮恰好 200 次调用/轮；fallback 轮 primary+fallback 各恰好 200 次/轮。

E13-09 CPU profile（压测进行中采样，2026-11-01，主机 20 核）：
  hit：      10s 窗口 Total samples 2.24s（≈1.1% CPU）；TOP：Syscall6 32% + futex 24%，
             net/http 写路径累计 10.7%；业务函数（JSON/gin/日志）均 <1.5%。
  miss：     15s 窗口 3.49s（≈1.2%）；形状相同，encoding/json 解码累计 3.7%。
  fallback： 15s 窗口 4.50s（≈1.5%）；Syscall6 38% + futex 19%。
  结论：该速率下网关为 I/O 型负载——CPU 近乎空闲（~1%）而吞吐到顶，CPU 花销集中在
  网络系统调用与 goroutine 调度等待，不在计算。反事实：若为计算瓶颈，榜首应为
  encoding/json 或 GC 函数占 40% 以上。未出现该形状。
  （heap_hit/heap_miss/heap_fb 三份已于 E13-12 分析）

E13-10 停载回落与账面对账（2026-11-01）：
  停载 30 秒后 goroutine total=38（空载 15），逐条有归属：
    20 = MySQL 空闲连接 watcher（连接池 1→20 条，SetMaxIdleConns(20) 的合法保温）
    5  = 日志 worker（恒定）
    ~6 = 上游 keep-alive 连接 readLoop/writeLoop（Transport 保温）
    其余 = 信号等待、main、connectionOpener/Cleaner 等基线常驻
  判断：无泄漏特征。泄漏的判据是"持续增长不封顶"，不是"必须回到初始值"。
  db_pool：Open=20/Idle=20/InUse=0，MaxIdleClosed=131，WaitCount=0。
  账面：request_logs 共 10072 条；cache:content key 4019 个；25 用户余额 99838~99975（分摊扣费）。
  定量预测：未做（沿用 E13-07 的说明）。
  实验局限：
    - 负载形态为短促突发（受 250 条/60 秒窗口预算约束），非持续稳态；排队结论只覆盖突发场景
    - 并发梯度止于 c64，更高并发未测
    - Fake Provider 本身在链路内且为单实例，miss/fallback 吞吐含其处理时间
    - gin debug 模式与每请求 stdout 日志计入测量（基线如实记录现状，不改）
    - 未测：race、go test -bench、Redis RTT、MySQL 内部状态、heap 分析、block profile 有负载读取

E13-11 race、benchmark 复验与 Redis RTT（2026-11-01）：
  执行环境：golang:1.26-alpine 容器挂实验网络（Windows 宿主机跑 -race 需要 gcc，故容器内执行；
  模块源 goproxy.cn，apk 经腾讯镜像装 gcc；MySQL 集成测试使用专用库 ai_gateway_test，不动实验数据）。
  go test -race ./...：全部包 ok，退出码 0，未发现 DATA RACE。
  （覆盖为测试套件全部用例；"-race 二进制 + 并发压测"加强版未做）
  go test -bench -benchmem（GOMAXPROCS=20，默认 1s/项）：
    BenchmarkCheckRateLimit       22.7µs/op   396 B/op  14 allocs/op
    BenchmarkGetTokenCache_Hit   131.6µs/op   960 B/op  16 allocs/op
    BenchmarkGetTokenCache_Miss  130.5µs/op   656 B/op  29 allocs/op
    BenchmarkWriteCachedStream    6.6µs/op  3853 B/op  29 allocs/op
  解读：缓存类操作耗时被 Redis 往返支配（Hit 与 Miss 几乎相等，都是一次往返）；
  WriteCachedStream 纯内存 6.6µs，但 3853 B/op 偏高——每次写缓存流都重新分配缓冲。
  Redis RTT（compose 网络内 redis-cli --latency，96 样本）：min 0 / max 2 / avg 0.46ms；
  基准实测单次缓存操作 0.13ms（含一次 RTT + 编解码）——两种口径（连续 PING 循环 vs 热连接单操作）
  均证明亚毫秒级。hit P50 2.6ms ≈ 4~6 次 Redis 往返（E13-08 的归因获得实测支撑）。

E13-12 heap 分析（2026-11-01，heap_fb.out 快照，go tool pprof -top）：
  inuse_space（负载后现占内存）：全进程 17.26MB；榜首为 bufio.NewWriterSize(3.2MB)/NewReaderSize(2.6MB)
  ——连接读写缓冲（keep-alive 保温），无业务对象囤积，与 E13-10 goroutine 回落结论互证。
  彩蛋：pprof.StartCPUProfile 占 1.18MB，CPU 采样自身缓冲也计入内存账。
  alloc_space（进程启动以来累计 ~348MB）：bufio 三项合计 ~67MB 居首（每请求新建缓冲 +
  Scanner 逐行扫上游 SSE），次为 json.Marshal、MIMEHeader——与 E13-11 benchmark 的
  WriteCachedStream 3853 B/op 交叉指向同一嫌疑人："每请求缓冲不复用"。
  两把独立尺子（微基准 / heap）指向一致，构成将来缓冲复用优化的完整证据链（本轮不改）。
```

## E13 macOS / Docker Desktop 参考基线（2026-11-02）

> 这组数据来自 MacBook 本机 Docker Desktop，与上方 Windows 主机的 E13 结果分开保存。它用于阶段 2 在同一台 Mac 上比较资源变化，不作为跨机器的绝对容量阈值。

### 环境与固定条件

- 宿主机：macOS Darwin 25.6.0，ARM64，8 CPU，8 GiB 内存。
- Docker Engine：`29.2.0`；Docker Compose：`v5.0.2`。
- Docker Desktop 全局资源：4 CPU，`2068910080` bytes（约 1.93 GiB）。
- Compose 没有设置 `cpus`、`mem_limit` 或 `deploy.resources`；app 容器的 `cpu.max` 和 `memory.max` 均为 unlimited。
- app 容器 `ulimit -n=1048576`。
- 负载生成器：宿主机 `go1.24.3 darwin/arm64` 编译；Gateway/Fake Provider 镜像使用 `golang:1.26-alpine` 构建。
- Gateway：`127.0.0.1:18080`；诊断端口：`127.0.0.1:18082`；Fake Provider：`127.0.0.1:18081`。
- app 和 Fake Provider 均按当前工作区源码重建；MySQL/Redis 数据卷保留。
- model：`e1-model`；Fake Provider 正常 SSE 返回 HTTP 200、两个内容 chunk、usage 和 `[DONE]`。
- `miss` 每条请求使用唯一消息；每轮 `n=200`；25 个本地测试用户和 Token 轮转，每轮开始前清空 `ratelimit:*`。
- 每轮确认 Fake Provider 调用数，避免把缓存命中误当成回源结果。

### E13-07 观测入口

| 入口/指标 | 本机结果 |
|---|---|
| `GET /debug/pprof/` | HTTP 200，2594 bytes |
| `GET /debug/vars` | HTTP 200，4097 bytes |
| 空载 goroutine | 16 |
| 空载 `db_pool` | `MaxOpen=100, Open=1, InUse=0, Idle=1, WaitCount=0` |

空载稳定采样（5 次）还得到：`HeapAlloc=1.54~1.74 MB`、`HeapInuse=3.38~3.48 MB`、fd=14、TCP `ESTABLISHED=3`、`CLOSE_WAIT=0`、app cgroup 内存约 `57.3~57.6 MiB`。

### E13-08 Provider 回源路径

#### `miss` 正常 SSE

所有轮次均为 200/200 HTTP 200，Fake Provider primary 调用 200 次。

| 并发 | 吞吐 req/s | TTFB P50/P95/P99 ms | Total P50/P95/P99 ms |
|---:|---:|---:|---:|
| c=1 | 477.9 | 0.58 / 1.68 / 3.01 | 1.78 / 3.47 / 5.21 |
| c=4 | 1216.7 | 0.83 / 1.91 / 10.20 | 2.69 / 4.91 / 21.33 |
| c=16 | 2515.5 | 2.13 / 6.46 / 9.84 | 5.73 / 10.15 / 13.56 |
| c=64 | 1749.5 | 9.82 / 21.07 / 21.61 | 27.52 / 69.38 / 81.12 |

#### `fallback` 正常对照

- 配置：primary 返回 500，fallback 返回正常 SSE。
- `c=1, n=200`，HTTP 200 为 200/200。
- 吞吐：`440.3 req/s`。
- TTFB：P50/P95/P99=`0.92/1.49/3.47 ms`。
- Total：P50/P95/P99=`1.99/3.09/7.23 ms`。
- Fake Provider：primary 200 次，fallback 200 次。

### E13-10 资源峰值和回落

#### c64 正常 SSE 突发

在 c64、n=200 的正常回源轮中，监控捕获到的峰值样本为：goroutine 242、fd 181、TCP `ESTABLISHED=169`、`CLOSE_WAIT=0`、app cgroup 内存 `78,491,648` bytes（约 74.9 MiB）。当前代码没有活跃请求计数器，本轮用负载生成器配置的并发数作为 `active_configured=64`，并以 goroutine、fd、TCP 和 Provider 调用数作为资源观测代理。

#### 停载回落

| 时点 | goroutine | HeapAlloc | HeapInuse | DB Open/Idle/InUse/Wait | fd | TCP EST/CLOSE_WAIT | app 内存 |
|---|---:|---:|---:|---|---:|---|---:|
| t0 | 37 | 6.99 MB | 9.37 MB | 20 / 20 / 0 / 0 | 73 | 63 / 0 | 71.6 MiB |
| t5s | 37 | 5.31 MB | 8.08 MB | 20 / 20 / 0 / 0 | 73 | 62 / 0 | 71.4 MiB |
| t10s | 37 | 6.44 MB | 8.94 MB | 20 / 20 / 0 / 0 | 73 | 62 / 0 | 70.5 MiB |
| t30s | 37 | 6.80 MB | 9.22 MB | 20 / 20 / 0 / 0 | 73 | 62 / 0 | 70.5 MiB |

DB 连接池从空载的 1 条连接扩展到 20 条 idle 连接后保持稳定；`WaitCount` 始终为 0。goroutine、fd 和 TCP 在停载后没有继续增长，当前没有观察到单调泄漏信号。

#### 延长观测窗口的正常 SSE 控制

为避免短突发错过资源峰值，另外执行了 primary `chunk-delay=100ms`、`c=16、n=200` 的正常 SSE 控制负载。它保持同一 Provider 回源路径，只延长每个请求的生命周期：

- HTTP 200：200/200；primary 调用 200 次。
- 吞吐：`72.8 req/s`。
- TTFB P50/P95/P99：`102.96/138.16/138.28 ms`。
- Total P50/P95/P99：`207.91/245.22/247.79 ms`。
- 观测峰值：约 97 goroutine、103 fd、92 条 TCP、app 内存约 74.6 MiB。
- 采样期间 `db_pool` 为 `Open=20、InUse=0、Idle=20、WaitCount=0`。

这轮是资源观测控制，不是阶段 2 的取消实验。

### E13-12 heap 基线

已保存正常 SSE 前后的 heap profile 快照：

- 空载/预热后：`heap_idle_initial.out`，2816 bytes。
- c64 负载结束后 t0：`heap_recovery_t0.out`，20011 bytes。
- 停载 30 秒后：`heap_recovery_t30.out`，21076 bytes。

对应的 `memstats` 快照显示：c64 结束后 `TotalAlloc=279,997,968` bytes、`NumGC=88`；30 秒后 `TotalAlloc=308,489,616` bytes、`NumGC=96`。本轮只建立快照，不做完整 `go tool pprof -top` 归因。

### 本机结论和后续用法

- E13-07 诊断入口在本机可用。
- E13-08 的 Provider 回源控制组已经建立；`c=16` 是本机吞吐最高的已测档位，c64 出现吞吐下降和尾延迟上升。
- E13-10 的资源回落控制组显示停载 30 秒后资源进入稳定平台；DB idle 连接保温属于当前配置行为，不作为泄漏判据。
- E13-12 已有本机 heap 前后快照，后续 E9 若出现持续增长，再分析 profile 内容。
- Mac 和 Windows 的数据用于各自机器上的前后对比；跨机器数字只作参考，不直接作为相同容量阈值。

## 完成标准

- Fake Provider 可以稳定重现 E1 表中的每个场景，并能清空和查询调用统计。
- 每个 E1 场景都只改变一个主要变量，并保留客户端、上游和网关侧证据。
- 能解释每个场景的调用次数、fallback/换 Key 行为、SSE 输出、日志、缓存和余额变化。
- E13 的四条基线都确认实际走了目标路径，而不是依赖 Redis 历史状态。
- 性能结果包含测试配置和环境，能在同一环境中重新执行。
- 如果做了 E13 允许的最小观测修复，必须用同一场景复验，并记录修复前后差异。
- 结果写回本文件，并在实验总计划的进度表中更新状态和链接。

## 是否需要修复

第一轮默认只观察，不因为单个现象立即重构业务逻辑。以下情况可以进入最小修复：

- E1 证实请求在不应无限等待的阶段长期占用资源。
- 取消、断流或 scanner 错误被错误地当成成功，导致错误缓存或错误扣费。
- fallback 或换 Key 次数与代码契约不一致。
- E13 证实 pprof/观测入口不可用，且修复只涉及受保护的诊断入口。

修复后必须重跑同一个用例，不能只根据代码阅读宣布问题已解决。

## 迁移结论待填写

```text
迁移到 Python/httpx：
迁移到 FastAPI StreamingResponse：
迁移到 Celery 或后台任务：
迁移到真实反向代理和第三方 Provider：
本实验不能证明的内容：
```

## 附录：性能测试方法论速查（E13 学习笔记）

> 从 E13 全程提炼，供后续任何性能测试复用。测试清单不是背出来的，是从"程序最怕发生什么"推导出来的。

### 矩阵一：三层观测框架（测什么）

| 视角 | 回答什么 | 指标 | 实验/本机工具 | 生产对应物 | E13 实例 |
|---|---|---|---|---|---|
| 客户端 | 对外多快？何时排队？ | 吞吐、错误率、状态码分布、TTFB/Total 的 P50/P95/P99 | loadgen、hey、wrk | APM、网关监控 | hit 拐点 c16（2728 req/s） |
| 进程 | 时间花在哪？资源涨没涨？ | goroutine 峰值/回落、heap/allocs/GC、连接池 WaitCount、CPU profile | pprof、expvar | 持续 profiler、Prometheus | Syscall6+futex≈55%，业务函数 <1.5% |
| 系统 | 依赖和环境扛住没？ | 容器 CPU/内存、Redis RTT/命中率、MySQL 连接/慢查询 | docker stats、redis-cli、SHOW STATUS | 节点/DB 监控 | 本轮全空闲未成瓶颈，故无数据 |

工具会换，三个问题永远不变（即 USE 法：每种资源看利用率/饱和度/错误）。
不同实验往框架里加自己的列：E2 加 TCP/fd，E7 加队列长度/丢弃数，E8 加连接池等待明细。

### 矩阵二：结论判读规则（怎么读）

| 读法 | 操作 | 判读 | E13 实例 |
|---|---|---|---|
| 竖着比 | 同路径不同并发，看吞吐曲线 | 吞吐跌 + P95 暴涨同时出现 = 拐点/排队 | hit c64 反跌 |
| 横着比 | 同并发不同路径，控制变量 | 差值 = 环节成本 | c64: rl 3383 > hit 1765 > miss 989 > fb 880 |
| P95 ÷ P50 | 算比值 | >2~3 倍 = 排队/尾延迟问题 | c64 hit 达 6.8 |
| TTFB vs Total | 算差值 | 差大 = 时间在等上游 | miss c1: 4ms vs 11ms |
| 峰值 vs 回落 | 停载 30s 再采 | 持续增长不封顶 = 泄漏；逐条有归属的保温 = 正常 | 38 vs 15 |
| CPU profile 榜首 | flat% 从大到小 | 业务函数大=算力型；syscall/futex 大=I/O 型；mallocgc/GC 大=分配型 | I/O 型 |
| CPU 总忙度 | Total samples ÷（窗口×核数） | <5% 却吞吐到顶 = 等待型瓶颈 | 1.1% |

### 矩阵三：实验设计规则（怎么测才可比）

| 要素 | 规则 |
|---|---|
| 控制变量 | 一次只改一个变量（路径 × 并发），其余全部固定 |
| 必须固定的项 | 请求体/响应内容、数据量、时长、用户集、软硬件版本、git commit |
| 自变量梯度 | 并发阶梯（如 c=1/4/16/64），必须覆盖"不挤→开始挤→挤爆" |
| 样本量 | P50 需几百，P99 需 ~1000+ 才稳；小样本只能作参考并注明 |
| 基线绑定环境 | 数字只在同环境可比；换环境/改配置后须重测基线 |
| 修复后复验 | 用同一场景重跑，记录修复前后差异，否则修复不算完成 |
| 负载形态要声明 | 突发（burst）和持续（sustained）结论不可互相引用 |

### 矩阵四：测试类型全景（从"怕什么"推导，超出性能范畴的补充）

| 程序形状 | 最怕的事故 | 武器 | Go 里的用法 |
|---|---|---|---|
| 有逻辑分支 | 算错 | 单元测试 | `go test`（表驱动惯用法） |
| 解析外部输入 | 被怪输入打崩 | fuzzing | `testing.F` |
| 并发共享状态 | 数据竞争（偶发、难复现） | race 检测 | `go test -race`、`go build -race`（慢 5~10 倍，只查正确性） |
| 有性能预算 | 悄悄变慢 | 微基准 + 基线 | `go test -bench`（纳秒/次、allocs/op） |
| 依赖外部服务 | 挂了/变慢 | 故障注入 | 本实验计划的整体方法（E1/E4/…） |

### E13 沉淀的标准作业卡（以后性能测试照此执行）

1. 定路径：识别要测的代码路径（如缓存命中/回源/拒绝/切换）
2. 定负载：突发还是持续、并发梯度、每档样本量
3. 定控制变量：固定项清单 + 每轮的准备动作（如清限流 key）
4. 写定量预测（跑前）
5. 三层同时采：客户端计时 + 进程 profile + 系统侧状态
6. 按矩阵二判读，写出"证据 → 结论"链
7. 归档：结果表 + 环境说明 + 局限声明，基线表供后续实验对照
