# IP 属地与历史数据重算

## 查询规则

复用 `hjfunnyMC-NeoSite/backend/minecraft/geoip.py` 的数据源优先顺序：

1. 纯真 `qqwry.ipdb`（IPIP IPDB 格式，CN 语言）命中国家“中国”，采用其省市，不调用在线服务。
2. 其他地址或纯真未命中，使用 GeoLite2/GeoIP2 City MMDB；有省或城市则采用，名称优先 `zh-CN`，其次英文，行政区选择最具体一级。
3. 缺少省市时，若显式配置 ip-api 兼容 URL，则使用在线兜底。不可用时使用已有本地国家信息。私有/回环地址不发起查询。

统计协议保持 country/continent 两位代码，如 `CN` / `AS`，不是中文国家名称。纯真提供中国省市时不混用 MMDB 的近似坐标，latitude/longitude 留空；ASN 仍独立使用 ASN MMDB。不将纯真 ISP 字段伪装成 ASN。

与低频登录记录场景不同，服务采集只做本地查询/读取已缓存的在线结果：在线查询进入容量 256 的独立队列，不阻塞 HTTP，也不在事件写入事务中等待网络。首次缓存未命中的事件可能只有本地国家级数据；缓存成功后用于后续事件，首次事件可用重算命令补齐。成功省市结果缓存 24 小时，失败短缓存 5 分钟，内存缓存最多 10000 个 IP；队列满时跳过该次在线兜底。

## 安装数据库

可使用与另一项目相同的 `qqwry.ipdb` 文件，无需转换。不要使用旧 `qqwry.dat` 格式，也不要使用 `qqwry-raw.ipdb` 替代处理后的省市库。

```sh
mkdir -p /var/lib/axolotl/geoip
curl -fL --retry 3 \
  https://github.com/nmgliangwei/qqwry.ipdb/releases/latest/download/qqwry.ipdb \
  -o /var/lib/axolotl/geoip/qqwry.ipdb.new
mv /var/lib/axolotl/geoip/qqwry.ipdb.new /var/lib/axolotl/geoip/qqwry.ipdb
```

`.env`：

```dotenv
ANALYTICS_QQWRY_DB=/var/lib/axolotl/geoip/qqwry.ipdb
ANALYTICS_GEOIP_CITY_DB=/var/lib/axolotl/geoip/GeoLite2-City.mmdb
ANALYTICS_GEOIP_ASN_DB=/var/lib/axolotl/geoip/GeoLite2-ASN.mmdb
# 默认空值：不访问外部 IP 服务。需要时显式启用：
ANALYTICS_GEOIP_API_URL=http://ip-api.com/json/{ip}?lang=zh-CN&fields=status,countryCode,continentCode,regionName,city
```

可以只配置纯真；没有安装的 MMDB 路径应留空。显式配置但不存在/损坏的库会报错，避免误以为地理查询已启用。库文件不提交 Git；容器要额外挂载它们所在目录。修改配置或替换库文件后，重启 Go 服务，使后续采集使用新数据源；历史重算进程独立加载当前文件，不必为每次重算停止服务。

ip-api URL 中必须包含一个 `{ip}`，只发送 IP，不发送 UA、URL 或认证信息。免费 ip-api 仅提供 HTTP、限制非商业用途；使用前确认适用，或自行配置带有效凭据的 HTTPS 兼容接口。API 返回必须包含 `status`、`countryCode`，省市为 `regionName`、`city`；使用示例中的 fields，不能沿用只有中文 `country` 的字段列表。

## 40 次/分钟共享限流

进程内和同一应用数据库内共同实施滚动 60 秒最多 40 次真实请求，缓存命中和本地查询不占额度；失败请求也占额度。server、重算命令和连接同一数据库的其他进程共享同一行额度，不会各发 40 次。数据库无法预留额度时不调用外部 API；事务只锁这行额度，发 HTTP 前即提交。收到 429 或 `X-Rl: 0` 后按 `X-Ttl` 暂停并共享暂停时间。未返回有效时间则等待 60 秒，最大接受 1 小时暂停。

不同数据库/其他应用不共享本表。如果 hjfunnyMC 与本服务使用同一公网出口，ip-api 的出口总额度仍需共同分配或使用统一代理；本限制只能约束连接同一 Update Server 数据库的进程。

新增迁移 `00004_geoip_rate.sql` 创建一行共享限流状态，不改写事件表，不重建索引。部署新版前执行：

```sh
./bin/axolotl-server db migrate
```

## 不停服重算

配置好数据库路径并迁移后，在同一工作目录、新开一个终端运行：

```sh
# 预演：实际解析/可能消耗 API 额度，但不更新事件。
./bin/axolotl-server analytics relocate --dry-run
# 正式运行：server 可以继续 serve。
./bin/axolotl-server analytics relocate
```

默认处理命令启动时过去 14 天已有 IP 的事件，截止时间固定为命令启动时，不追赶运行中新来的请求。按 `(occurred_at,id)` 索引顺序分页，每批 500 行，批次间休眠 100ms。解析在事务外完成，PostgreSQL 每批一条 UPDATE/独立短事务，最多等待行锁 1 秒，语句超时 10 秒。不会锁整张表、暂停采集、调用统计保留维护或创建/删除分区；正常并发写入继续进行，已被保留维护删除的行自然跳过。

只更新属地和可解析的 ASN 字段，IP、请求时间、UA、状态码、流量、版本与渠道不变。完全无法解析的记录保留旧值；仅得到同一国家而缺少省市时保留已有省市，避免在线服务失败清空历史数据。配置的 ASN 数据源未命中时不清空原 ASN。大批量更新仍会产生 WAL 和死元组，应按服务器负载调小批次/增加间隔，正常依赖 autovacuum 回收，无须停服执行 VACUUM FULL。

```sh
./bin/axolotl-server analytics relocate --batch-size 200 --pause-ms 300
./bin/axolotl-server analytics relocate \
  --start '2026-11-01T00:00:00+08:00' --end '2026-11-10T12:00:00+08:00'
```

重算超出在线兜底额度时先使用本地结果或保留原值，不等待一分钟卡住中国 IP 的本地重算。需要在线兜底的未补齐记录可稍后再次重算。只使用本地纯真/MMDB 时完全不受在线额度影响。

每批输出 JSON：start/end 是固定范围，scanned/resolved/updated/skipped 是本次运行计数，after/after_id 是最后成功批次的游标，complete 表示结束。中断或锁等待超时后使用最后输出续跑：

```sh
./bin/axolotl-server analytics relocate \
  --start '<上次start>' --end '<上次end>' \
  --after '<上次after>' --after-id <上次after_id>
```

不要用 dry-run 的末尾游标跳过正式处理。重复执行重算本身安全；相同 IP 的本地解析使用有界缓存，不加载全部历史记录。分页经过后才提交的延迟采集事件可能在本轮之外，必要时再执行一次。SQLite 调试支持相同命令，PostgreSQL 是线上并发与吞吐验证目标。

## 验证

测试覆盖真实 IPDB 读取格式、中国优先不触网、中文省市与国家代码、异步网络不阻塞采集、24h 缓存、40 次共享额度及并发预留、429 共享暂停、重算预演/中断续跑/另一个数据库连接继续采集。可选真实库冒烟：

```sh
TEST_QQWRY_DB=/var/lib/axolotl/geoip/qqwry.ipdb go test ./internal/analytics -run TestActualQQWry -v
```

参考实现：`E:/mc.hjfunny.site/hjfunnyMC-NeoSite/backend/minecraft/geoip.py`；IPDB 解析使用 IPIP 官方 `github.com/ipipdotnet/ipdb-go`，在线额度和响应字段见 ip-api 官方 JSON 文档。
