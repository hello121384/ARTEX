# ARTEX 个人分支 v0.4.5 — 跨服务器测试部署

版本来源：[`hello121384/ARTEX@v0.4.5`](https://github.com/hello121384/ARTEX/releases/tag/v0.4.5)

包含 RuoJi6 v0.3.30 基底、v0.4.4 的 Planner/Worker 改造、以及 e63be87 大图修复。
这是**实验版本**：定向回归和 Linux AMD64 构建通过，但完整 Agent/DB 测试尚未全部通过。务必先隔离测试。

## A. Docker（Linux AMD64）

在一台**全新服务器**上：

```bash
git clone https://github.com/hello121384/ARTEX.git
cd ARTEX
git checkout v0.4.5
cp .env.example .env
# 修改 .env，设置强随机 POSTGRES_PASSWORD
# 确保 Docker / Compose 可用
docker compose -f docker-compose.yml -f docker-compose.github.yml pull
docker compose -f docker-compose.yml -f docker-compose.github.yml up -d
docker compose -f docker-compose.yml -f docker-compose.github.yml ps
```

固定镜像：`ghcr.io/hello121384/artex:v0.4.5`（**目前仅 Linux AMD64**）。首次发布的 GHCR 包若为 Private，需要在 GitHub Packages 设置为 Public；或以具有 `read:packages` 权限的 GitHub PAT 执行 `docker login ghcr.io`。

本镜像使用 `8787` 提供 Web UI；`8788` 仅绑定本机。不要直接裸露到公网；建议 HTTPS 反代、管理员鉴权、网络白名单，并在合法授权的范围内测试。首次进入需要初始化管理员并配置模型。

**不要在旧 ARTEX 的目录复用已有数据库、容器和数据卷**。新建目录、独立 Compose 项目及新数据库以免意外触发旧任务。

日志：

```bash
docker compose -f docker-compose.yml -f docker-compose.github.yml logs -f --tail 100 artex
```

## B. GitHub Release 可执行文件

[GitHub Releases v0.4.5](https://github.com/hello121384/ARTEX/releases/tag/v0.4.5) 在 Actions 发布成功后会提供 Linux AMD64/ARM64、macOS AMD64/ARM64、Windows AMD64 五个平台的 ZIP 和 `SHA256SUMS`。解压后还需自行配置 PostgreSQL；ZIP 不是 Docker 镜像。

## 对照测试

优先把 v0.4.5 部署到独立实例，然后分别比较 Planner 紧凑视图、Worker Intent 精简模式的开关效果。重点记录任务覆盖、历史 Fact 召回、重复 Intent、执行期间的输入/缓存 Token，以及经人工核实的有效发现。

目前本机仍在运行旧镜像上的任务，本次发布不对其做任何变动。
