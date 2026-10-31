# N1 管理接口与密钥安全边界

> 环境准备已于 2026-10-25 由助手完成：栈运行正常、config.json 仅占位 key 且被 .gitignore 覆盖、数据库已清至上次实验前状态。从「实验前假设」起由实验者填写。

- 实验编号：N1
- 实验名称：管理接口与密钥安全边界
- 完成状态：已完成（修复及自动化复验）
- 执行时间：2026-10-28
- 代码版本：安全修复提交 `79ed0fc`；测试和结果记录纳入 N1 完成提交。
- 环境：Windows 10（10.0.26200）+ Docker Desktop 29.7.2 / Compose v5.4.0（WSL2，20 核 / 16GB）；mysql 8.0.46（healthy）、redis 7.4.11、app 镜像 aigateway:v1.0（golang:1.26-alpine 构建，Up）；网关地址 http://127.0.0.1:8080

### 想验证的问题：
普通请求方是否可以调用创建用户、创建 Token、禁用用户等管理接口？Token、Provider Key、数据库密码是否被日志、响应或仓库泄露？

### 代码侧预读结论：
- 路由注册时只有 `/api/v1` 组挂载了 `AuthMiddleware`；`/admin` 组没有认证或授权中间件（`backend/internal/httpapi/chat.go:36-47`）。因此，能访问 8080 的请求方不需要 `Authorization` 就能进入创建用户、创建 Token 和禁用用户三个 Handler。合法请求体下，创建用户和创建 Token 预期返回 200；禁用存在用户预期返回 200。
- `AuthMiddleware` 只保护聊天接口。它把整个 `Authorization` Header 原样作为 Token 查询值，先查 Redis，未命中再按 `token_key=? AND status=1` 查 MySQL（`backend/internal/httpapi/middleware.go:24-45`、`backend/internal/storage/mysql.go:44-47`）。代码没有解析 `Bearer` 前缀；而 Token 生成值是 `sk-...`（`backend/internal/httpapi/admin.go:84-89`），README 示例却发送 `Bearer sk-...`，两者可能直接导致 401。
- 认证成功后只检查关联用户的 `Status` 和 `Balance`，没有管理角色、Token 作用域、过期时间或 Token 撤销流程（`backend/internal/httpapi/middleware.go:47-71`、`backend/internal/domain/models.go:83-91`）。Token 缓存命中时不会重新检查 `token.Status`；缓存 TTL 约为 5~6 分钟（`backend/internal/storage/redis.go:71-99`）。
- 创建 Token 使用按当前时间播种的非密码学 `math/rand`，并把完整 Token 通过响应返回（`backend/internal/httpapi/admin.go:84-100`）。TokenKey 以明文存入 MySQL（`backend/internal/domain/models.go:83-91`、`backend/internal/storage/mysql.go:66-67`），Redis 缓存的完整 Token JSON 也包含明文 TokenKey（`backend/internal/storage/redis.go:89-97`）。泄露后可重放 `/api/v1/chat`，消耗对应用户余额、用户限流额度以及网关的 Provider 配额（`backend/internal/httpapi/chat.go:51-63`、`125-147`）。
- 业务日志没有显式打印 Provider Key 或 Token；但数据库使用 `gorm.Config{}` 默认 Logger（`backend/internal/storage/mysql.go:20-22`），鉴权查不到 Token 或 SQL 变慢时，GORM 可能记录带参数的 SQL，从而把本次查询的 `Authorization` 值写入日志。
- Provider Key 从 JSON 配置明文加载到进程内存，并原样设置到上游 `Authorization` Header（`backend/internal/config/config.go:16-23`、`backend/cmd/aigateway/main.go:27-29`、`backend/internal/provider/chat.go:24-29`）。当前业务代码未发现将其返回给客户端或主动写入日志的路径；但构建上下文没有 `.dockerignore`，本地 `config.json` 若存在，会被 `backend/Dockerfile:15` 的 `COPY . .` 复制进镜像层。Provider URL 也没有被校验为 HTTPS（`backend/internal/httpapi/chat.go:104-110`），误配为 HTTP 时上游 Key 会明文传输。
- 数据库默认凭据以明文出现在 `docker-compose.yml:8-10,39-41` 和 `backend/configExample.json:34`；MySQL 与 Redis 端口也发布到宿主机（`docker-compose.yml:11-12,25-26`）。当前可见 Git 历史的早期提交 `43b4571` 还曾硬编码过一条 Provider Key；该 Key 已撤销时，历史文本仍存在但不再是有效凭据。

导致问题的代码侧原因总结：
- 路由分组只区分 URL，不会自动产生权限；管理路由没有挂载认证，更没有独立的管理员角色检查。
- Token 被当作可直接查询、缓存和持久化的明文业务字段，没有采用哈希存储、过期、撤销和轮换机制。
- Token 生成使用时间种子的 `math/rand`，凭证生成强度不足；管理接口又无认证，导致凭证发行面完全暴露。
- 配置、容器构建和 Compose 部署没有形成完整的密钥边界：明文配置、默认数据库密码、无 `.dockerignore`、未强制 HTTPS。
- 数据库默认日志配置没有针对凭证字段做脱敏，鉴权失败或慢查询时存在把用户提交的 Token 写入日志的条件路径。

### 计划外变体（如有）：

### 是否发现代码缺陷：

### 是否需要修复 / 修复方案和替代方案：
- 需要修复。

  简单说：先给 `/admin` 加上一把独立的管理员凭证，阻止普通请求方管理用户和 Token；再把 Gateway Token 改成难猜、可过期、可撤销且不以明文存储；最后把数据库密码和 Provider Key 移出仓库与镜像构建上下文。

  主方案：最小应用侧修复 spec

  1. 管理接口
     - 新增 `AdminAuthMiddleware`，从必填环境变量 `ADMIN_API_KEY` 读取管理员密钥；环境变量缺失时服务启动失败，不提供默认值。
     - `/admin` 整组挂载该中间件，要求 `Authorization: Bearer <ADMIN_API_KEY>`。缺少、格式错误或不匹配统一返回 `401`，响应和日志不得回显密钥。
     - `/api/v1` 继续使用用户 Token 鉴权；用户 Token 不得被当作管理员凭证接受。

  2. Gateway Token
     - 用 `crypto/rand` 生成 `sk-` 加 32 字节随机值，禁止继续使用 `math/rand` 和时间作为随机种子。
     - 统一解析 `Authorization: Bearer <gateway-token>`，只把解析出的 Token 用于认证；缺少 Bearer、空值、错误 Token、过期 Token 和已撤销 Token 均返回 `401`。
     - 数据库只保存 Token 的 SHA-256 哈希和状态、创建时间、过期时间，不再保存明文 `TokenKey`。
     - 创建 Token 时明文只在管理员响应中出现一次；Redis 的 Key 和 Value 都不得包含明文 Token。缓存 TTL 不得超过 Token 剩余有效期。
     - 新增 `DELETE /admin/tokens/:id`：在数据库把 Token 设为失效并删除对应 Redis 缓存；撤销后下一次聊天请求必须立即返回 `401`。
     - Token 默认有效期设为 90 天，过期时间写入数据库；创建、认证和撤销都使用同一字段判断。

  3. 用户禁用与缓存
     - 保留 `DELETE /admin/users/:id` 的用户状态更新和用户缓存删除。
     - 验收要求：用户被禁用后，该用户已有 Token 的下一次聊天请求不得继续调用 Provider，并返回 `403`；与当前 `AuthMiddleware` 的用户状态判断保持一致。

  4. 配置、日志和构建
     - 新增 `backend/.dockerignore`，至少排除 `config.json`、`.env`、证书和私钥文件；镜像内不复制运行时 Provider 配置。
     - `docker-compose.yml` 不再提交 `123456` 等默认密码；`MYSQL_ROOT_PASSWORD`、`DSN`、`ADMIN_API_KEY` 从外部环境或 Secret 注入，缺失时启动失败。
     - 默认不发布 MySQL 和 Redis 到外部网络；本地调试如需端口，只绑定 `127.0.0.1`。
     - GORM 日志启用参数化 SQL，应用日志禁止记录 `Authorization`、明文 Token、Provider Key 和 DSN。
     - 启动时校验 Provider URL 必须是 HTTPS；Fake Provider 等测试场景必须通过显式测试配置单独允许 HTTP，默认拒绝。

  5. 数据迁移
     - 发布前先轮换现有 Gateway Token；为 Token 增加哈希列和过期时间列，完成迁移后清空或删除旧的明文 Token 列。
     - 不能只改 Go 结构体而保留数据库里的明文列；本地实验环境可以清空旧 Token 后重建表，生产环境需要可回滚的显式迁移。

  验收标准：
     - 无管理员凭证、错误管理员凭证和普通用户 Token 调用三个 `/admin` 接口均为 `401`；正确管理员凭证才能成功。
     - 正确格式的用户请求必须使用 `Bearer <token>`；原始 Token、错误 Token、过期 Token 和撤销 Token 均不能调用 `/api/v1/chat`。
     - 创建响应拿到的明文 Token 在 MySQL、Redis、应用日志和 Docker 镜像中均不可检索到；数据库只能看到哈希。
     - 撤销 Token、禁用用户、过期 Token 的复验结果符合上述状态码，并且不会产生 Provider 调用。

  替代方案：把 `/admin` 完全放到内网或反向代理后，仅允许 VPN、mTLS 或企业 SSO 访问，应用不再自己验证管理员密钥。这个方案依赖额外基础设施，不能单独解决 Token 明文存储和配置进入镜像的问题，因此本实验优先采用应用内管理员凭证方案。

### 自动化验收测试状态：

- 普通 Go 单测/API 测试：`go test -count=1 ./...` 已通过。
- 静态和竞态检查：`go vet ./...`、`go vet -tags=integration ./...`、`go test -race -count=1 ./...` 已通过。
- MySQL/Redis HTTP 生命周期集成测试：通过专用 MySQL 8.0 测试库和 Redis 7 测试 DB 执行，用户/Token 创建、Token 认证、撤销、过期、禁用用户及 Provider 调用边界均通过。
- MySQL 迁移集成测试：旧 `token_key` 表迁移、生成哈希和过期时间、删除明文列均通过。
- Redis Token 集成测试：实际缓存读写、明文排除、TTL 和撤销删除均通过。
- 集成测试编译与静态检查：`go test -tags=integration -run '^$' ./...`、`go vet -tags=integration ./...` 已通过。
- Compose 密钥、端口绑定和 `.dockerignore` 检查：`backend/tests/deployment_boundary_checks.sh` 已通过。
- Docker 镜像已构建；最终镜像文件和镜像历史检查通过，未发现运行时配置、环境文件、证书/私钥或测试密钥。
- 尚未执行运行中完整 Gateway 的日志检索；没有真实 Provider 调用。

### 实验局限：

- 当前未连接真实 Provider，只使用 Fake Provider 测试调用次数和调用边界。
- Docker 镜像检查已完成，但运行中的完整 Gateway 日志检索仍未执行。
