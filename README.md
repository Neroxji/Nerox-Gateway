# Nerox-Gateway (高性能大模型 API 网关)

这是一个面向 OpenAI 兼容接口的统一大模型 API 网关，当前版本聚焦高可用、鉴权计费、缓存优化和多厂商故障转移。项目使用 Go、Gin、MySQL 和 Redis，支持 SSE 流式响应、API Key 池化、分布式限流和 Docker Compose 部署。

---

## 💻 技术栈
* **语言**: Go (Goroutine, Channel, Context) 
* **框架**: Gin, GORM 
* **存储**: MySQL (索引/锁优化), Redis (Lua 脚本/缓存策略) 
* **协议**: HTTP/SSE, TCP/IP
* **部署**: Docker, Docker Compose
* **测试**: Postman
---
## 📚最终的核心路由架构图
![架构图](assets/architecture.png)
---

## 🚀 核心性能指标 (Benchmarks)
经 `Hey` 工具实测（并发 50），本项目在核心链路上表现如下 ：

* **QPS**: 本地环境单机测试（MBAm2 8核8g）并且 命中缓存 的情况下 **8700+** 。
* **延迟**: P99 稳定在 **10ms** 以内 。

<details>
  <summary>🔍 点击查看 QPS & 延迟测试截图</summary>
  <br>
  <img src="assets/benchmark_qps.png" alt="Benchmark QPS" width="100%">
</details>                          
<p></p> 

* **拦截响应**: 分布式限流在 **1.5万+ QPS** 的恶意刷量场景下（限流拦截），依然保持微秒级拦截响应 。

<details>
  <summary>🛡️ 点击查看限流拦截效果图</summary>
  <br>
  <img src="assets/rate_limit_demo.png" alt="Rate Limiting Response" width="100%">
</details>


## 🧩 核心能力

* **多厂商适配与故障转移**：统一请求协议，按模型配置 Provider fallback 链路。
* **API Key 池化**：轮询多个密钥，并在单个密钥或 Provider 失败时自动切换。
* **鉴权与计费**：通过 Token 关联用户账户，使用数据库事务和行级锁完成并发扣费。
* **Redis 缓存**：缓存 Token 鉴权数据，使用 TTL 抖动降低缓存雪崩风险。
* **分布式限流**：使用 Redis Lua 脚本保证限流计数的原子性。
* **可观测性与优雅关闭**：异步写入请求日志，并在进程退出时等待日志安全落库。

---

## 🐳 本地部署

依赖：装好 [Docker](https://www.docker.com/products/docker-desktop/) 就行，不需要本地装 Go / MySQL / Redis。

**第一步：获取代码**

```bash
git clone https://github.com/Neroxji/Nerox-Gateway.git
cd Nerox-Gateway
```

**第二步：填写你自己的 API Key**

复制 `configExample.json` 为 `config.json`，把 `keys` 换成自己的。可以配置多个 Provider 和 fallback 链路。

```json
{
  "providers": [
    {
      "name": "deepseek",
      "url": "https://api.deepseek.com/chat/completions",
      "model": "deepseek-v4-flash",
      "keys": ["Bearer sk-你的key"],
      "price_per_k": 500
    }
  ],
  ...
}
```

**第三步：按需修改密码（可选）**

`docker-compose.yml` 里默认密码是 `123456`，自己部署的话改一下，两处要一致：

```yaml
MYSQL_ROOT_PASSWORD: 你的密码
DSN: "root:你的密码@tcp(mysql:3306)/ai_gateway?..."
```

**第四步：构建 & 启动**

```bash
docker build -t aigateway:v1.0 .
docker-compose up -d
```

第一次会拉基础镜像，稍微等一下。跑完之后：

```bash
docker-compose ps        # 三个容器都是 Up 就说明正常
docker-compose logs app  # 看有没有报错
```

服务启动后监听 `http://localhost:8080`。

**停止服务**

```bash
docker-compose down          # 停止，数据保留
docker-compose down -v       # 停止并清掉数据库数据
```

---

> config.json 是通过 volume 挂载进容器的，修改 API Key 后直接 `docker-compose restart app` 生效，不需要重新 build 镜像。

---

## 🧪 API 测试（Postman）

服务跑起来后，按以下顺序调用接口。

### 第一步：创建用户

```
POST http://localhost:8080/admin/users/create
Content-Type: application/json
```

```json
{
  "username": "testuser",
  "balance": 100000
}
```

返回示例：
```json
{
  "message": "create user successfully!",
  "user_id": 1,
  "balance": 100000
}
```
<details>
  <summary>📸 点击查看创建用户测试截图</summary>
  <br>
  <img src="assets/postman_create_user.png" alt="Create User Postman" width="100%">
  <br><br>
  <img src="assets/dbeaver_create_user.png" alt="Create User DB" width="100%">
</details>

---

### 第二步：给用户创建 Token

```
POST http://localhost:8080/admin/tokens/create
Content-Type: application/json
```

```json
{
  "user_id": 1,
  "name": "我的测试密钥"
}
```

返回示例：
```json
{
  "message": "create token successfully!",
  "token_id": 1,
  "token_key": "sk-xxxxxx"
}
```

把 `token_key` 的值复制下来，下一步要用。
<details>
  <summary>🔑 点击查看创建 Token 测试截图</summary>
  <br>
  <img src="assets/postman_create_token.png" alt="Create Token Postman" width="100%">
  <br><br>
  <img src="assets/dbeaver_create_token.png" alt="Create Token DB" width="100%">
</details>

---

### 第三步：发起对话请求

```
POST http://localhost:8080/api/v1/chat
Authorization: Bearer sk-xxxxxx（上一步拿到的 token_key）
Content-Type: application/json
```

```json
{
  "model": "deepseek-v4-flash",
  "messages": [
    {
      "role": "user",
      "content": "你好"
    }
  ]
}
```

网关会自动选择可用的 Provider 转发请求，支持SSE流式响应。
<details>
  <summary>💬 点击查看对话请求（SSE流式）测试截图</summary>
  <br>
  <img src="assets/postman_chat_sse.png" alt="Chat SSE Postman" width="100%">
</details>


---

### 其他：禁用用户

```
DELETE http://localhost:8080/admin/users/:id
```

把 `:id` 替换成具体的用户 ID，例如 `/admin/users/1`。执行后该用户的 Token 将无法继续使用。
