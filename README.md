# Axolotl Update Server — Go

Gin 更新服务。生产使用 PostgreSQL，本地调试使用 SQLite；保留 Release/Beta 发布、Tauri 更新清单、下载目录及版本管理。安装包由独立 `DIST_SERVER` 分发，本机只保存清单文件。

采集到达应用的 `/latest` 和 `/api/*` 请求，明细保留 14 天，提供受管理员认证保护的统计 API：概览、时间趋势、维度分布、性能百分位、状态与流量、客户端/来源/地理信息，以及请求明细。也保留 PostgreSQL 只读视图；不包含分析页面和长期汇总。

## 本地启动

需要 Go 1.27+ 和 C 编译器。SQLite 驱动使用 CGO：Windows 使用 MinGW/GCC，Linux 安装 `build-essential`，macOS 安装 Xcode Command Line Tools。

原 Python 项目已完整归档到 `legacy/`，Go 项目位于仓库根目录。原仓库历史和五个阶段的 Go 重写提交均保留。

```powershell
Copy-Item .env.example .env
go build -trimpath -ldflags="-s -w" -o bin/axolotl-server.exe ./cmd/axolotl-server
./bin/axolotl-server.exe serve
```

默认使用 `instance/update-server.db`，开发模式自动升级表结构。`.env` 不覆盖已有环境变量。健康检查：`GET http://127.0.0.1:8082/api/health`。

## 生产启动

### 下载 Linux amd64 二进制

GitHub Actions 的 **Build Linux amd64** 工作流在 push、pull request 时运行，也可以在 Actions 页面手动运行。成功后打开对应运行记录的 **Artifacts**，下载 `axolotl-server-linux-amd64`。

产物包含静态链接的 `axolotl-server`、保留执行权限的 `axolotl-server-linux-amd64.tar.gz` 和 `SHA256SUMS`。仅构建 Linux amd64，内置 SQLite 所需 C 代码，运行时不需要安装 Go 或 GCC；HTTPS 签名下载仍需要系统 CA 证书。

下载并解压 Actions 的 artifact ZIP 后，在其目录执行：

```sh
sha256sum -c SHA256SUMS
mkdir -p bin
tar -xzf axolotl-server-linux-amd64.tar.gz -C bin
./bin/axolotl-server --help
```

如果直接使用 ZIP 中的原始二进制，先执行 `chmod +x axolotl-server`。首次启动仍需配置 `.env`、运行 `db migrate`，从旧服务迁移时还需导入旧 SQLite 和重建清单。

### 配置与启动

单实例部署，Go 进程直接提供 HTTP，Nginx/Caddy 提供 HTTPS。使用 PostgreSQL 16+，完整集成测试使用 PostgreSQL 18。生产必须设置 PostgreSQL 连接、非占位的 `ADMIN_TOKEN` 和 `WEBHOOK_SECRET`。

```dotenv
APP_ENV=production
DATABASE_URL=postgres://axolotl:password@127.0.0.1:5432/axolotl?sslmode=disable
ADMIN_TOKEN=<随机令牌>
WEBHOOK_SECRET=<随机密钥>
DIST_ROOT=/var/lib/axolotl-update-server/dist
PUBLIC_BASE_URL=https://update.axlmc.org
DIST_SERVER=https://downloads.example.com
```

```sh
go build -trimpath -ldflags="-s -w" -o bin/axolotl-server ./cmd/axolotl-server
./bin/axolotl-server db migrate
./bin/axolotl-server serve
```

`serve` 在生产只检查结构版本，不自动迁移。systemd 示例的 `ExecStartPre` 显式执行迁移。`DATABASE_URL` 中的密码需要 URL 编码；远程数据库按部署环境配置 TLS。

容器步骤：设置 `POSTGRES_PASSWORD`、`DOCKER_DATABASE_URL`、`ADMIN_TOKEN`、`WEBHOOK_SECRET`，然后分别运行：

```sh
docker compose up -d db
docker compose run --rm server db migrate
docker compose up -d server
```

`DOCKER_DATABASE_URL` 使用 `postgres://axolotl:编码后的密码@db:5432/axolotl?sslmode=disable`。默认不信任容器外转发的客户端 IP；按反向代理实际连接地址配置 `TRUSTED_PROXY_CIDRS`。本机默认只信任 loopback。可信代理解析同时用于统计、审计和限流，不依赖 Cloudflare。

## 运维命令

```text
axolotl-server serve
axolotl-server db migrate
axolotl-server db status
axolotl-server db import-legacy --source <旧SQLite文件> --dry-run
axolotl-server db import-legacy --source <旧SQLite文件>
axolotl-server manifests rebuild
axolotl-server db verify --source <旧SQLite文件>
axolotl-server analytics maintain --dry-run
axolotl-server analytics maintain
```

导入、重建和手动维护命令在停止服务后执行。服务本身会在启动时和运行期间维护统计数据，无需额外调度。

## 行为变化

- 移除旧上传接口 `/api/artifacts/*`；`/api/admin/stats` 使用新统计 API v2 结构。
- 移除旧本地安装包服务、GitHub 回退下载、安装包保留及转换清理命令。
- 导入数据使用元数据判断制品可用性。`DIST_SERVER` 为空时只提供 `manifest.json`，其他 `/dist` 文件返回 404。
- 保留本地清单、强制更新的原子替换，以及删除文件的隔离和重试。
- 统计保留查询参数白名单，记录 HTTP/性能/字节指标，但不保存请求体、认证信息、完整 URL 或任意参数。

## 文档

- [HTTP API](docs/API.md)：协议、认证、发布载荷和缓存行为。
- [旧服务迁移](docs/MIGRATION.md)：预检、导入、校验、切换与回滚。
- [外部分析接入](docs/ANALYTICS.md)：稳定视图、白名单字段、权限和查询示例。
- [统计 API v2](docs/STATS_API.md)：dashboard、筛选、分组、性能、地理信息和升级说明。
- [统计 OpenAPI](docs/openapi-stats.json)：可导入 API 客户端的接口规范。
- [验证记录](docs/VALIDATION.md)：测试方式、实际验证和容量测量。

## 开发验证

```sh
go test ./...
go test -race ./...
go vet ./...
```

设置 `TEST_POSTGRES_URL` 后，集成测试创建随机命名的临时数据库并自动删除，需要测试账号有 `CREATEDB` 权限。仅指向测试实例。未设置时使用临时 SQLite 文件。

```sh
TEST_POSTGRES_URL='postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable' go test -race -count=1 ./...
```

