# Experiment Support Runtime

这套目录是阶段 1 以及后续实验共用的本地运行时底座，和正常 Gateway 的 `backend/`、根目录 Compose 文件分开。

## 启动

在仓库根目录执行：

```bash
docker compose \
  --env-file experiments/support/.env.local \
  -f experiments/support/docker-compose.yml \
  up -d --build
```

服务地址：

- Gateway：`http://127.0.0.1:18080`
- Fake Provider：`http://127.0.0.1:18081`
- MySQL 和 Redis：仅实验 Compose 内部网络访问

查看状态：

```bash
docker compose \
  --env-file experiments/support/.env.local \
  -f experiments/support/docker-compose.yml \
  ps
```

停止服务：

```bash
docker compose \
  --env-file experiments/support/.env.local \
  -f experiments/support/docker-compose.yml \
  down
```

## Fake Provider 控制接口

- `GET /__test/health`
- `POST /__test/reset`
- `GET /__test/stats`
- `POST /__test/config`

所有 Key 和数据库密码都只用于本地实验。`.env.local` 已被 Git 忽略，`config.json` 只包含占位 Provider Key。
