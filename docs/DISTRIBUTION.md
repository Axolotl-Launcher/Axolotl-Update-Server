# 本地安装包与 Cloudflare 分发

## 行为

`DIST_SERVER` 留空时，发布 Webhook 从已校验的 GitHub Release 下载安装包和签名到 `DIST_ROOT/.staging/`。下载流式写入磁盘、核对大小和 SHA-256，通过后才移动到版本目录、写清单并提交版本。普通下载请求不会触发源站下载任务。发布失败会移除本次新增的文件，已有文件必须与元数据一致，不能覆盖成不同内容。

Release、Beta 按 SemVer 从大到小，各保留 3 个 **published** 版本的本地安装包及签名，不按 Webhook 到达时间排序。发布成功、撤销/恢复/删除操作完成后以及服务启动时执行清理；失败记录警告，可用 `dist prune` 重试。清理仅删除数据库登记的旧版本文件，保留版本元数据、审计、统计和 `manifest.json`，不删除未知文件或目录。延续旧保留语义：撤销版本不参与 published 排序，也不由自动清理删除；需要回收其空间时显式删除该版本。

下载顺序：

1. 先校验 Cloudflare 来源（默认开启），然后校验可选源站密钥。
2. 路径必须对应数据库内 published/revoked 版本及公开制品；公开制品引用的签名也允许下载。
3. 有本地文件则直接传输，支持 GET、HEAD、ETag、Range 和 If-Range。
4. 本地文件不存在，且是已发布、具有对应 `v<version>` tag 和 Release ID 的已知制品，返回 302 到 `github.com/Axolotl-Launcher/Axolotl/releases/download/<tag>/<filename>`。保留集内意外缺失的文件也可以回退。不会把原请求查询参数传给 GitHub。
5. 未知文件、私有文件、缺失的清单、缺失的 revoked 制品不回退；损坏或不安全路径也不会伪装为正常下载。已有 revoked 文件继续允许访问，撤销只控制更新选择，沿用既有协议。

GitHub 回退会让客户端下载 GitHub 公共资源；“仅 Cloudflare”约束的是本服务的 `/dist/*` 入口，不能限制 GitHub 自身的公开访问。`/latest`、`/api/*` 保持原有访问策略；统计采集范围仍为 `/latest` 和 `/api/*`，没有加入 `/dist/*`。

非空 `DIST_SERVER` 仍保留旧的外部 302 模式，优先于本地分发；此时不下载或清理本地安装包，入口同样检查 Cloudflare 来源。

## 服务器配置

```dotenv
DIST_ROOT=/www/wwwroot/update.axlmc.org/dist
PUBLIC_BASE_URL=https://update.axlmc.org
DIST_PUBLIC_BASE_URL=https://dist.axlmc.org
DIST_SERVER=
DIST_CLOUDFLARE_ONLY=true
UPDATE_SERVER_HOST=127.0.0.1
UPDATE_SERVER_PORT=8082
TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
GITHUB_ARTIFACT_DOWNLOAD_TIMEOUT_SECONDS=1800
```

本地调试可以设置 `DIST_CLOUDFLARE_ONLY=false`。生产域名 DNS 应开启 Cloudflare 代理（橙云），SSL 使用 Full (strict)。分发模式开启时，应用自动信任内置 Cloudflare 官方 IPv4/IPv6 网段以解析访客 IP。该列表于 2026-11-10 对照官方列表核验；网段变化时需更新 `internal/config/config.go`。

应用根据直接连接的 IP，或可信反向代理链中第一个非内部代理的 IP，检查 Cloudflare 网段，不把 `CF-Ray`、`CF-Connecting-IP` 等可伪造头当成通行凭证。应用只监听 loopback；如果用容器网络，信任实际内部代理地址，不要配置任意公网或 `0.0.0.0/0` 为可信代理。

`deploy/nginx.example.conf` 已基于当前 OpenResty 线上配置修改，可替换站点文件。它用 `$realip_remote_addr` 覆盖传给 Go 的 XFF，保留真实 TCP 对端：即使全局 `real_ip_header CF-Connecting-IP` 把 `$remote_addr` 改成访客 IP，Cloudflare 来源检查也不会误拒。需要 ngx_http_realip_module；若编译未启用，先禁用 real_ip 重写，再改用 `$remote_addr`。不要使用任意客户端提交的 XFF，也不要把 `/dist/` 改为静态 alias/root。

```sh
cp /www/server/openresty/nginx/conf/sites-enabled/update.axlmc.org.conf /www/server/openresty/nginx/conf/sites-enabled/update.axlmc.org.conf.bak
cp deploy/nginx.example.conf /www/server/openresty/nginx/conf/sites-enabled/update.axlmc.org.conf
/www/server/openresty/nginx/sbin/nginx -t
# 上一步成功后执行
/www/server/openresty/nginx/sbin/nginx -s reload
```

Cloudflare IP 白名单证明请求来自其网络，不能绑定你自己的 Zone。需要绑定时，生成至少 32 字符的随机 `DIST_ORIGIN_SECRET`，在你自己的 Cloudflare Request Header Transform Rule 中对 `/dist/*` 覆盖设置 `X-Axolotl-Origin-Secret` 为相同值。应用对每个 dist 请求额外检查它。不要把密钥放在前端、URL 或日志中；未配置规则前保持该配置为空。也可以在 OpenResty 使用 Zone 专用证书的 Authenticated Origin Pulls，或部署 Cloudflare Tunnel。

## 额外分发域名 dist.axlmc.org

同一台服务器、同一个 Go 进程和 DIST_ROOT 可以同时接受两个域名的 `/dist/*` 请求，无需复制安装包。`DIST_PUBLIC_BASE_URL=https://dist.axlmc.org` 指定对外下载链接，保留 `/dist/<version>/<filename>` 路径；不配置则兼容原 PUBLIC_BASE_URL。`DIST_SERVER` 必须留空，不要把它设置为指向同一 Go 服务的新域名，否则会产生重定向循环；配置校验也会拒绝这种组合。

上线顺序：

1. 在 Cloudflare 为 `dist.axlmc.org` 添加指向源站的 DNS 记录并启用代理（橙云）。
2. 复用已经添加 `dist.axlmc.org` 的原证书：`/etc/letsencrypt/live/update.axlmc.org/fullchain.pem` 和 `privkey.pem`。
3. 使用同一份 `deploy/nginx.example.conf` 替换原 update 站点文件；文件内同时包含 update 和 dist 的虚拟主机，不另建 dist 站点文件。dist 虚拟主机设置 `root /www/wwwroot/dist.axlmc.org`，只开放 `/dist/*`，API 和 `/latest` 仍由 update 域名提供。
4. 新域名加入 Cloudflare 的 `/dist/*` 缓存规则；若启用了 `DIST_ORIGIN_SECRET`，其请求头覆盖规则也必须匹配新域名。
5. 测试新站点与新域名下载正常后，停服修改 `.env` 的 `DIST_PUBLIC_BASE_URL`，执行 `manifests rebuild`、`db verify`，再启动服务。已有包不用重新下载，动态 `/latest` 和下载目录会自动生成新域名链接。

```sh
cp /www/server/openresty/nginx/conf/sites-enabled/update.axlmc.org.conf /www/server/openresty/nginx/conf/sites-enabled/update.axlmc.org.conf.bak
cp deploy/nginx.example.conf /www/server/openresty/nginx/conf/sites-enabled/update.axlmc.org.conf
/www/server/openresty/nginx/sbin/nginx -t
# 检查通过后
/www/server/openresty/nginx/sbin/nginx -s reload
curl -I https://dist.axlmc.org/dist/<version>/<filename>
```

新域名缓存策略、Cloudflare 来源限制、GitHub 回退均与旧入口一致。旧 update 域名的安装包路径继续有效，以兼容已经发布/缓存的旧清单与正在下载的客户端，不额外增加一次跳转。切换时清除旧清单的 Cloudflare 缓存并等待 `/latest` 原有短缓存刷新；不必清除旧二进制缓存。新站点不添加 `/api/*` 或 `/latest` 路由，也不影响源站现有统计采集范围。

若先前已经创建单独的 `dist.axlmc.org.conf`，将它移出 `sites-enabled` 后再检查/重载，避免重复声明同一域名。这里的 Nginx `root` 仅声明站点目录，`/dist/*` 仍由 Go 反代处理；安装包实际位置由 `.env` 的 `DIST_ROOT` 决定，现有文件无需搬迁。若也希望改变安装包的物理位置，应另外停服迁移目录并修改 `DIST_ROOT`，不能仅靠 Nginx root 改变存储路径。

## 缓存策略

| 响应 | Cache-Control | Cloudflare-CDN-Cache-Control |
| --- | --- | --- |
| 本地安装包/签名成功响应，包括 206/304 | `public, max-age=3600, s-maxage=31536000, immutable` | `public, max-age=31536000, immutable` |
| `manifest.json` | `no-cache, max-age=0, must-revalidate` | `no-store` |
| GitHub/外部分发 302、403/404/416 等错误 | `no-store` | `no-store` |

浏览器允许缓存安装包 1 小时，Cloudflare 允许缓存 1 年（31536000 秒），并声明 immutable。前提是发布后同一版本、同一文件名的内容永久不变；即使删除再发布，也不能复用原 URL 分发不同字节，应发布新版本或新文件名。保留策略只回收源站文件，不主动驱逐 CDN 已缓存的字节；旧版本缓存命中仍可下载，缓存失效或被提前淘汰后回源时才回退到 GitHub。TTL 是允许的新鲜期，不保证边缘节点实际保留一年。安装包支持基于 SHA-256 的强 ETag，清单使用文件修改时间/大小的弱 ETag。清单会被 force update 修改，因此不再使用旧的一年 immutable 缓存。动态 `/latest` 继续保持已有的 30 秒共享缓存和 Header 变体规则。

Cloudflare Cache Rule 匹配当前下载域名的 `/dist/` 路径：选择 Eligible for cache，尊重源站缓存头（包括 Cloudflare-CDN-Cache-Control），不要强制覆盖 TTL 或为 302/错误设置正数缓存时间。`manifest.json` 可额外单独设置 Bypass cache，放在能覆盖上述规则的位置。`/api/*` 不加入此规则；`/latest` 的缓存键规则保持现状。

缓存头不保证 Cloudflare 一定缓存：套餐的单文件大小上限、请求条件和配置仍会影响 HIT。Free/Pro/Business 默认单文件上限为 512 MB，Enterprise 默认 5 GB；更大制品可能每次回源，需在实际套餐验证。压缩包/安装包在 `/dist/` 禁用 OpenResty gzip，保留 Content-Length 及 Range 语义。`proxy_buffering off` 避免大包在反向代理临时目录额外落盘。

切换时清除旧 `/dist/*/manifest.json` 的 Cloudflare 缓存；已经被浏览器缓存为 immutable 的旧清单无法由源站强制清除，客户端更新判断继续使用 `/latest`。删除或紧急下架版本时，也需要单独清理对应 CDN 缓存；本项目不自动调用 Cloudflare purge API。

## 历史数据切换

无需新增数据库迁移；现有版本元数据足够重建下载地址。先备份数据库和 dist，然后停止服务及发布操作，再执行：

```sh
./bin/axolotl-server db migrate
./bin/axolotl-server manifests rebuild
./bin/axolotl-server dist sync
./bin/axolotl-server db verify
./bin/axolotl-server serve
```

`dist sync` 只为 Release/Beta 最近各 3 个 published 版本下载缺失制品，校验已有文件，结束后清理多余旧包；不为所有历史版本下载包。中途失败可重试，成功下载的版本保留。已有文件摘要不符时明确报错，需停服核查并移走损坏文件再重试，不会悄悄覆盖。没有匹配 GitHub Release 元数据的历史手动版本需人工处理。

`dist prune` 仅清理旧包，不下载。以上命令需服务账号对 DIST_ROOT 有读写权限；维护期间停止服务避免跨进程竞争。已处理 Webhook 重放仍是幂等返回，不会替代 `dist sync` 回填历史安装包。

发布期间暂存占用约为本次缺失安装包总大小，完整接收后才发布；各 3 份是稳定状态的包保留策略，不是瞬时磁盘配额，也不包含统计库、清单、撤销版本、备份及代理日志。整个发布的下载时间可能超过 Cloudflare 回源响应等待上限：原发布请求应在超时后使用相同 event_id 重试并核对版本，避免生成新事件重复发布。

## 部署验收

```sh
# 经过橙云域名：预期 200/206/304（无本地文件时 302）。
curl -I https://update.axlmc.org/dist/<version>/<filename>
curl -I -H 'Range: bytes=0-1023' https://update.axlmc.org/dist/<version>/<filename>
# 绕过 Cloudflare：预期 403。使用 --resolve 保持正确 TLS 域名和 SNI。
curl -I --resolve update.axlmc.org:443:<源站IP> https://update.axlmc.org/dist/<version>/<filename>
# 伪造 Cloudflare 头仍应 403。
curl -I --resolve update.axlmc.org:443:<源站IP> -H 'CF-Connecting-IP: 1.1.1.1' -H 'CF-Ray: forged' https://update.axlmc.org/dist/<version>/<filename>
```

从真实 Cloudflare 域名连续请求检查 `CF-Cache-Status`、`Age`、Range 及回退结果；本地单元测试只能验证应用行为，不能证明线上 Cloudflare 规则已生效。缓存命中请求不会到源站，现有统计仍只代表到达应用的 `/api/*` 和 `/latest` 请求。

参考：

- https://www.cloudflare.com/ips-v4
- https://www.cloudflare.com/ips-v6
- https://developers.cloudflare.com/cache/concepts/cdn-cache-control/
- https://developers.cloudflare.com/cache/concepts/default-cache-behavior/
- https://developers.cloudflare.com/ssl/origin-configuration/authenticated-origin-pull/
- https://nginx.org/en/docs/http/ngx_http_realip_module.html
