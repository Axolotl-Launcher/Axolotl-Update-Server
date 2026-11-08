# 从 Python/SQLite 迁移

新项目是独立服务，不原地改写旧库。导入读取旧 SQLite，写入新的目标库；支持已知 Alembic 0001～0009 结构，包括开发目录中实际存在的 0004。无需先让旧 Python 服务升级到最新迁移。

## 导入内容

- version、artifact、channel_pointer、webhook_event、audit_log。
- 保留版本/制品/审计 ID、状态、签名、发布信息、事件 ID 和原始载荷摘要。
- 缺失的 force_update、制品分类和展示字段按旧迁移缺省补齐。
- 通用 macOS 制品按旧规则补齐目标平台；Webhook 仅在 event_id 精确符合旧 GitHub 规则时关联版本，不猜测自定义 ID。
- 所有版本采用元数据可用性判断，旧 artifact_storage 不带入新模型；不要求安装包在本机。
- 无时区的旧时间解释为 UTC，目标保存为带时区时间。
- 跳过旧 usage_event，既不导入也不删除旧统计。
- 不复制或删除旧安装包、旧清单和隔离文件。目标清单由 `manifests rebuild` 创建。

## 预演

1. 为新服务创建独立 PostgreSQL 数据库，配置 `.env` 的生产连接、密钥和分发地址。`DIST_ROOT` 指向新目录，不能与旧服务共用。
2. 备份旧 SQLite 及旧 `DIST_ROOT`。旧库运行时应使用 SQLite backup API 或停止所有写入后备份，不能只复制正在使用 WAL 的主文件。
3. 对快照执行迁移、预检、导入、清单重建和校验：

```sh
axolotl-server db migrate
axolotl-server db import-legacy --source /backup/update-server.db --dry-run
axolotl-server db import-legacy --source /backup/update-server.db
axolotl-server manifests rebuild
axolotl-server db verify --source /backup/update-server.db
```

`--dry-run` 只读取源快照并检查目标为空，不写目标业务数据。正式导入在单事务中执行，异常时回滚；不会跳过无法转换的业务行。目标业务表非空时拒绝导入。数据库结构必须先迁移。

导入完成后若希望重复预演，创建新的空目标数据库。不要把预演目标当作最终增量同步目标，工具不做合并。

4. 在其他端口启动新服务，检查 `/api/health`、版本列表、Release/Beta `/latest`、两个 macOS 平台和下载目录。分发重定向必须能在外部下载服务器实际找到对应路径。
5. 在预演库检查管理员撤销/恢复、强制更新和删除重试。不要使用旧生产事件进行测试发布，以免把预演变更与最终数据混淆。

## 最终切换

1. 停止旧服务和 CI 发布/管理写操作，记录切换时间；保存最终一致性备份。
2. 使用新的空 PostgreSQL 目标重新执行导入、重建和校验，保持原 ID 和事件哈希。
3. 原子切换到新二进制/工作目录和新数据库配置。反向代理继续转发 `/api/`、`/latest` 和所有 `/dist/` 到 8082；保留 `/latest` 的缓存头。
4. 启动单实例新服务，确认数据库结构、请求采集、分区和读写权限。先做只读冒烟检查，再恢复发布。
5. CDN 旧 `/latest` 最多受其缓存 TTL 影响；静态 immutable 清单可能更久。无需为了重写更改安装包 URL。

## 回滚

新服务尚未接受发布/管理变更时，可停止新服务，恢复旧代理/进程和原 SQLite、文件目录。新产生的请求统计可以放弃。

新服务已接受业务写入时，停止写入并备份 PostgreSQL，列出切换后发布、撤销、强制更新和删除操作，先在旧数据中核对/恢复这些变更再回切。没有自动 PostgreSQL→旧 SQLite 逆向迁移，也不执行双写。

## 文件一致性和故障处理

清单和数据库之间无法使用数据库原子事务。正常错误路径执行原文件恢复，删除使用隔离区并支持重试；进程被强制终止时仍可能在两个持久化步骤之间中断。

- 发布或强制更新异常后运行 `db verify`，数据库是权威元数据；必要时停止服务执行 `manifests rebuild`。
- 删除隔离区位于 `DIST_ROOT` 同级的 `.<目录名>-deletions/<版本SHA256>`。提交成功但清理失败时，重试对应 DELETE。
- 如果数据库中版本仍存在，但其目录因进程崩溃留在隔离区，先停止服务，核对版本与隔离目录后恢复，再运行校验。不要直接清除未确认的隔离目录。
- 符号链接或不安全目录会被拒绝。`version_restore_failed` 表示需要人工恢复文件。

## 配置变化

`APP_ENV` 替代 `FLASK_ENV`；移除 Flask `SECRET_KEY`、`UPLOAD_TOKEN`、安装包物理保留相关配置和大文件下载上限。保留有效的 DATABASE_URL、DIST_ROOT、PUBLIC_BASE_URL、DIST_SERVER、Webhook、管理密钥、缓存、代理和签名下载超时配置。

旧 `/api/artifacts/*` 和 `/api/admin/stats` 返回 404。所有历史本地包服务及 GitHub 回退删除。外部分析程序按 `ANALYTICS.md` 使用 PostgreSQL，只读账号不受业务结构变化影响。
