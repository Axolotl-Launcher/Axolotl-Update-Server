# 统计 API v2

统计完全由 Go 服务和 PostgreSQL 提供，不依赖 Cloudflare。本项目负责采集、聚合和查询 API，分析页面与长期存储由调用方负责。机器可读规范见 `openapi-stats.json`。

## 升级

停止旧 Go 进程，运行 `./bin/axolotl-server db migrate`，再启动新二进制。新增迁移 `00003_statistics` 为请求增加 HTTP 指标与 UA 分类，新增 `analytics.requests_v2`；v1 视图保持兼容。

旧请求的状态、字节和耗时无法恢复，保存 NULL；旧 UA 在再次遇到时补齐分类。14 天清理、字典去重、分区和批量写入保持有效。`ANALYTICS_ENABLED=false` 只关闭采集，仍可查询保留数据。

## 接口

全部 GET，支持 HEAD，返回 `Cache-Control: no-store`，要求 `Authorization: Bearer <ADMIN_TOKEN>`，不开放跨域凭据；前端使用同源代理或由后端调用。令牌不能放在查询参数里。

| 路径 | `data` 内容 |
| --- | --- |
| `/api/admin/stats` | overview、timeseries、breakdowns，一次取得面板数据 |
| `/api/admin/stats/overview` | 总量、独立 IP、字节、状态、缓存、机器人、性能与百分位 |
| `/api/admin/stats/timeseries` | 升序时间统计点，自动补齐空桶 |
| `/api/admin/stats/breakdown` | 单维度 Top N 和未展示请求数 |
| `/api/admin/stats/requests` | 请求明细和游标分页 |
| `/api/admin/stats/meta` | 支持维度、时间粒度、能力和口径 |
| `/api/admin/stats/status` | 保留数据范围、采集开关、队列和丢弃计数 |

`/api/admin/stats` 使用新的 dashboard 结构，不兼容旧 Python 的历史统计接口。

除 meta、status 外，共用响应封装：

```json
{
  "schema_version":2,
  "scope":"origin",
  "generated_at":"2026-11-08T06:00:00Z",
  "window":{
    "start":"2026-11-07T06:00:00Z","end":"2026-11-08T06:00:00Z",
    "interval":"15m","interval_seconds":900,"filters":{},"retention_clamped":false
  },
  "data":{}
}
```

## 查询参数

- `start`、`end`：带时区 RFC3339，区间 `[start,end)`，默认最近 24 小时。起点早于 14 天时截到当前保留边界，返回 retention_clamped=true；完全过期或倒置区间返回 400。终点未来超过一分钟返回 400，小范围时钟偏差截止当前时间。
- `interval`：auto、1m、5m、15m、1h、6h、1d，默认 auto，约 240 点以内；显式粒度最多 2000 桶。UTC 对齐，首尾桶可能仅覆盖部分时间。
- `limit`：1～1000；breakdown 默认 20，requests 默认 100；dashboard 对各分组使用相同 limit。
- `dimension`：breakdown 必填，单维度。
- `dimensions`：dashboard 可选，逗号分隔，最多 8 项；默认 route、status_code、country、browser、os、device_type、channel、client_version。
- 同名维度参数为精确筛选，多项之间 AND。不接受任意 SQL；列固定白名单，值参数绑定。非法 IP、数值、布尔和分组维度返回 400。

支持筛选与分组的维度：

```text
route, method, host, scheme, http_version, tls_version,
status_code, status_class,
country, continent, region, city, asn, asn_org,
client_ip, ip_version,
user_agent, browser, browser_version, os, os_version, device_type, is_bot,
referer_host, content_type, app_cache, error_code,
channel, client_version, platform, target_version, include_updater, include_revoked
```

status_class 为 1xx～5xx；country/continent 为两位大写代码；ip_version 为 4/6；布尔为 true/false；ASN 为不带 AS 前缀的整数。缺失维度在分组中为 `value:null`，无法识别的 browser/os 为 Unknown。Referer 只保存域名，不保存路径、查询和认证信息。

## 指标

| 字段 | 定义 |
| --- | --- |
| requests | 已入库请求总数 |
| unique_ips | 不同有效 IP 数，不是人数；各桶/分组不能相加得到总数 |
| unique_user_agents | 不同 UA 数，空 UA 也是字典值 |
| requests_per_second | requests / 实际覆盖秒数 |
| status_1xx … status_5xx | 状态分类请求数，未知旧数据不归类 |
| status_samples | 有状态码的样本数 |
| error_rate | (4xx+5xx)/status_samples，0～1；无样本 null |
| rate_limited_requests | 429 请求数 |
| access_denied_requests | 401/403 请求数，不是 WAF 事件 |
| request_bytes | 请求体被应用实际读取的字节合计 |
| response_bytes | 应用写出的响应体字节合计；HEAD/204/304 通常 0 |
| request_byte_samples / response_byte_samples | 对应已知字节记录数，区分未知与真实 0 |
| duration_samples | 已知应用处理耗时样本数 |
| avg_duration_ms / max_duration_ms | 已知耗时平均/最大值，毫秒；无样本 null |
| latency.p50_ms/p90_ms/p95_ms/p99_ms | overview 中的精确连续延迟百分位 |
| bot_requests / bot_samples | UA 启发式机器人数量/可分类样本数，不验证爬虫身份 |
| country_samples | 成功识别国家的样本数 |
| app_cache_hits/misses/bypass | `/latest` 应用结果缓存状态 |
| app_cache_hit_rate | hit/(hit+miss)，不含 bypass；无样本 null |

overview 包含全部指标；timeseries/breakdown 不含 latency 百分位对象，其余相同。空桶数量/字节为 0，耗时和比率为 null。旧明细未知值不补造，必须结合各 samples 字段解释结果。

字节不含 HTTP 头、TLS、TCP、代理压缩，不等于网卡流量；未被应用读取的请求体不计入。耗时是 Gin 处理时长，不是客户端端到端延迟或 TTFB。HTTP/TLS 描述 Go 直接收到的连接；TLS 在 Nginx 终止时 tls_version 为 null。scheme 可来自可信代理覆盖写入的 X-Forwarded-Proto。

## 分组与明细

breakdown.data 为 dimension、items、total_requests、other_requests、limit。items 内每项为 value 和共同指标，按请求数降序；other_requests 是超出 Top N 的请求数，不是丢失数。

requests.data 为 items、next_cursor、has_more、limit。按 `(occurred_at,id)` 倒序，items 包含 v1 字段，以及 method、status_code、duration_ms、request_bytes、response_bytes、host、scheme、http_version、tls_version、content_type、referer_host、app_cache、error_code、ip_version、country、continent、region、city、latitude、longitude、asn、asn_org、browser、browser_version、os、os_version、device_type、is_bot。

下一页传入 cursor 并保持相同过滤条件，省略 start/end，沿用服务器在第一页接受的窗口。14 天保留边界随时间移动时仅裁掉过期记录，不误判游标；显式修改窗口、整个窗口过期或过滤条件改变仍返回 400。游标不是可靠复制水位，异步晚到数据需要重叠窗口补扫和去重。

## 调用示例

```sh
curl -sS -H "Authorization: Bearer $ADMIN_TOKEN" \
  'http://127.0.0.1:8082/api/admin/stats'

curl -sS -G -H "Authorization: Bearer $ADMIN_TOKEN" \
  --data-urlencode 'route=/latest' --data-urlencode 'channel=release' \
  --data-urlencode 'interval=1h' \
  'http://127.0.0.1:8082/api/admin/stats/timeseries'

curl -sS -G -H "Authorization: Bearer $ADMIN_TOKEN" \
  --data-urlencode 'dimension=country' --data-urlencode 'limit=50' \
  'http://127.0.0.1:8082/api/admin/stats/breakdown'

curl -sS -G -H "Authorization: Bearer $ADMIN_TOKEN" \
  --data-urlencode 'status_class=5xx' --data-urlencode 'limit=100' \
  'http://127.0.0.1:8082/api/admin/stats/requests'
```

## 国家、城市和 ASN

使用合法取得的 GeoLite2/GeoIP2 City 和 ASN MMDB 文件，不自动下载，不调用在线 IP 服务。

```dotenv
ANALYTICS_GEOIP_CITY_DB=/var/lib/axolotl/geoip/GeoLite2-City.mmdb
ANALYTICS_GEOIP_ASN_DB=/var/lib/axolotl/geoip/GeoLite2-ASN.mmdb
```

未配置时相关字段为 null，meta.capabilities 报告未配置；路径无效启动失败。国家库也能提供国家信息，城市和坐标取决于库内容。更新文件后重启；私有/回环 IP 不补地理信息，历史请求不重写。容器需只读挂载库目录并传入环境变量。

配置实际代理的 TRUSTED_PROXY_CIDRS。默认只信任 loopback，不再默认信任 Cloudflare 网段；不信任客户端自报的国家、ASN 等头。没有本地地理库时不伪造地理数据。

## 运行和边界

默认只采集 `/latest`、`/api/*`，排除 `/api/admin/stats` 及子路径，避免面板轮询污染；设置 ANALYTICS_INCLUDE_STATS_REQUESTS=true 可纳入。参数白名单保持有效，不保存完整 URL、任意参数、请求体、Cookie 或 Authorization。

默认队列 10000，每秒或 500 条提交；status.data.collector 的 written、failed_batches、dropped、queue_length 为当前进程计数，重启清零。异步可见性滞后，崩溃和持续故障可能丢失未提交事件。明细只保留 14 天，不做长期汇总。

控制台输出 `[GIN]` 请求访问日志，包含时间、状态码、耗时、解析后的客户端 IP、方法和路径；不输出查询字符串。统计计数只通过 status API 查询，不再周期或退出时输出 analytics counters 日志，异常警告仍保留。

单进程最多两个并发统计查询，默认 15 秒超时；STATS_QUERY_TIMEOUT_SECONDS 可设 1～120 秒。查询还受 `/api/*` IP 限流约束。错误码：400 invalid_stats_query、401 invalid_admin_token、429 stats_busy/rate_limited、503 stats_timeout。所有错误沿用标准 error 对象。

数据在数据库里聚合/分页，不将 14 天明细全读入 Go 内存。PostgreSQL 对重复客户端/维度组合预聚合，再用 GROUPING SETS 一次计算多维分布；去重计数和加权平均保持精确。dashboard 顺序执行概览、时间序列和分组，期间新入库批次可能造成细微数值差异，近实时面板不作为结算快照。

只反映到达应用的流量：Nginx 自己返回的错误、`/dist`、其他服务和远端下载不在采集范围。没有 CDN/WAF/DNS 服务，不提供虚构的边缘命中、攻击评分、挑战、DNS 或下载完成数；meta 对这些能力返回 false。
