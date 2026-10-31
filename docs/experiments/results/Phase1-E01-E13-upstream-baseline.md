# 阶段 1：可控上游和测量基线

> 实验编号：E1 + E13
> 状态：E1-01~E1-09 已执行，E13 执行中（顺序延迟基线已完成，未测并发和资源）
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

附带观测：`GET /debug/pprof/` 返回 HTTP 404；当前 Gin 服务没有注册 pprof 路由。本项只记录，不在本轮修复。

暂未测：并发吞吐、并发错误率、CPU、heap/allocs/GC、goroutine、block profile、Redis RTT、MySQL 连接池和 race。以上 P50/P95/P99 仅来自 20 个顺序样本。
```

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
