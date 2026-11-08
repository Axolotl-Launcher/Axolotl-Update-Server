# 外部 PostgreSQL 分析接入

本项目只负责采集、14 天保留及数据库读取契约。分析页面、数据搬运、聚合和长期存储由外部程序负责。

## 稳定接口

外部程序查询 **`analytics.requests_v1`**。不要依赖内部表、分区名、UA 字典 ID 或内部序列的连续性；视图中的 `id` 是记录标识，使用 `(occurred_at,id)` 排序和分页。内部迁移可以改变物理存储，`v1` 字段和含义保持兼容；不兼容调整应新增视图版本。

| 列 | PostgreSQL 视图类型 | 含义 |
| --- | --- | --- |
| `occurred_at` | timestamptz | 请求入口时间，应用写入 UTC |
| `id` | bigint | 记录标识；会有间隙，不是提交水位 |
| `client_ip` | text，可空 | 可信代理解析后的规范 IP，内部为 inet |
| `user_agent` | text | UA 原文，空 UA 为 `''` |
| `route` | text | Gin 路由模板，未匹配使用 `unmatched` |
| `channel` | text，可空 | 当前接口实际使用的 release/beta |
| `client_version` | text，可空 | `/latest` 的客户端当前版本 |
| `platform` | text，可空 | `/latest` 的已知平台 |
| `target_version` | text，可空 | 路由中合法的目标版本 |
| `include_updater` | boolean，可空 | 下载目录选项 |
| `include_revoked` | boolean，可空 | 下载目录选项 |

不包含方法、状态码、耗时、响应体字节数、请求 ID、国家/地区、ASN、Referer、请求体、完整 URL、完整查询字符串或任意参数 JSON。

因此可以分析请求趋势、路由、IP、UA 和版本分布；不能计算错误率、请求延迟、带宽、CDN 缓存命中率或下载完成数。IP 数量不等同于用户数量，UA 可由客户端伪造。

## 白名单和缺省规则

只记录路径严格为 `/latest` 或以 `/api/` 开头的请求，包含这些路径下的 HEAD、OPTIONS、未匹配请求和错误请求。采集的是到达 Go 应用的请求；CDN 缓存命中或在边缘被阻断的请求不会出现。`/dist` 不采集。

| 路由 | 采集参数 |
| --- | --- |
| `/latest` | `channel`、`current_version`、`platform`，对应 `X-Axolotl-*` 非空请求头优先 |
| `/api/downloads/latest` | `channel`、`include_updater`、`include_revoked` |
| `/api/downloads/:version` | `target_version`、`include_updater`、`include_revoked` |
| 其他带 `:version` 的有效路由 | `target_version` |
| 其他 `/api/*` 与未匹配路径 | 只保存基础字段 |

`channel` 在适用接口默认 release；下载选项缺省为 false。非法 channel、版本、平台保存 NULL；不适用或未提供的版本/平台也为 NULL。布尔只为 true/false 时保存相应值，其他文本为 NULL。处理器仍保持现有业务解析规则，不因统计参数改变响应。

`version` 查询参数不是现有 API 支持的别名，忽略；目标版本来自路由。`foo`、`token`、`start`、`end` 等未定义参数全部忽略，不读取 Webhook 或管理请求体中的字段作为统计维度。存在相同查询参数多次时采用接口使用的第一个值。

UA 不解析浏览器/系统，也不在请求链路做 IP 地理查询。内部仅对非法 UTF-8 或 NUL 做数据库可存储的替换，正常 UA 原样保留。

## 可见性和保留

- 每 1 秒或累计 500 条提交，实际可见时间还受调度、数据库负载和重试影响。
- 内存队列默认 10,000；满时丢弃新记录。数据库故障时批次退避重试，进程崩溃可能丢失未提交明细。
- 正常退出最多用配置的 10 秒冲刷。日志输出写入、失败和丢弃计数。
- 视图严格返回当前数据库时间之前最近 14 天内的数据下界；所有聚合查询仍应显式提供 `[start,end)`。
- UTC 日分区每小时维护，完整过期分区删除，物理边界最多额外留不足一天。维护失败会记录日志并重试；视图时间过滤持续生效。
- 删除版本不删除明细；删除过期明细后清理无引用 UA。
- 原始 IP 仅在上述保留期可见。备份和外部复制的保留策略由部署方管理。

SQLite 仅开发调试，对应视图为 `analytics_requests_v1`，不构成外部程序的生产集成接口。

## 只读账号

以下 SQL 由数据库管理员执行，替换数据库名和密码：

```sql
CREATE ROLE analytics_reader LOGIN PASSWORD '<随机密码>';
GRANT CONNECT ON DATABASE axolotl TO analytics_reader;
GRANT USAGE ON SCHEMA analytics TO analytics_reader;
GRANT SELECT ON analytics.requests_v1 TO analytics_reader;
ALTER ROLE analytics_reader SET default_transaction_read_only = on;
ALTER ROLE analytics_reader SET statement_timeout = '30s';
ALTER ROLE analytics_reader SET idle_in_transaction_session_timeout = '30s';
ALTER ROLE analytics_reader SET timezone = 'UTC';
```

只授予视图，不授予业务表、UA 字典或分区。使用视图所有者权限读取底层表，分析账号不需要其 SELECT 权限。应用迁移/分区维护账号需要在数据库中创建表和 schema 的权限，示例部署使用专属数据库所有者。应用与分析账号使用不同凭据。

外部程序通过 PostgreSQL 驱动连接数据库；`psql` 只是可选命令行客户端。连接建议设置 `application_name`、超时和 UTC，并按实际网络配置 TLS。先按时间过滤，再聚合；查询较大窗口时避免无条件扫描所有明细。

## SQL 示例

每小时请求量，`$1`、`$2` 均为带时区时间，区间 `[start,end)`：

```sql
SELECT date_trunc('hour', occurred_at, 'UTC') AS hour, count(*) AS requests
FROM analytics.requests_v1
WHERE occurred_at >= $1 AND occurred_at < $2
GROUP BY hour ORDER BY hour;
```

渠道与客户端版本分布：

```sql
SELECT channel, client_version, count(*) AS requests
FROM analytics.requests_v1
WHERE occurred_at >= $1 AND occurred_at < $2 AND route = '/latest'
GROUP BY channel, client_version ORDER BY requests DESC;
```

路由与 Top UA：

```sql
SELECT route, count(*) AS requests
FROM analytics.requests_v1
WHERE occurred_at >= $1 AND occurred_at < $2
GROUP BY route ORDER BY requests DESC;

SELECT user_agent, count(*) AS requests
FROM analytics.requests_v1
WHERE occurred_at >= $1 AND occurred_at < $2
GROUP BY user_agent ORDER BY requests DESC LIMIT 100;
```

单 IP 查询，以及时间/ID 游标分页：

```sql
SELECT occurred_at, id, route, client_version, user_agent
FROM analytics.requests_v1
WHERE occurred_at >= $1 AND occurred_at < $2 AND client_ip = $3
ORDER BY occurred_at DESC, id DESC LIMIT 100;

SELECT * FROM analytics.requests_v1
WHERE occurred_at >= $1 AND occurred_at < $2
  AND (occurred_at,id) > ($3,$4)
ORDER BY occurred_at,id LIMIT 1000;
```

游标分页适用于固定时间窗浏览，不能将最大 ID 或请求入口时间直接作为可靠增量复制水位：并发请求、异步写入和重试可能晚到。外部增量同步应使用重叠时间窗并按 `(occurred_at,id)` 去重，定期补扫 14 天窗口；需要严格可靠复制时另行使用数据库变更流方案。

