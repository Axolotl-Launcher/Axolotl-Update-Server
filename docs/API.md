# HTTP API

所有响应包含 `X-Request-ID`。版本使用严格 SemVer，例如 `1.9.4`、`1.9.5-beta.1`。公开读取接口支持 GET 和 HEAD；HEAD 不返回响应体。下载目录额外支持 OPTIONS。

错误结构：

```json
{"error":{"code":"invalid_version","message":"Version must be a normalized SemVer.","request_id":"..."}}
```

管理认证为 `Authorization: Bearer <ADMIN_TOKEN>`。Webhook 使用下述独立 HMAC；没有旧上传令牌。未配置认证时返回 503，无效认证返回 401。`MAX_UPLOAD_SIZE` 限制请求体大小，超限返回 413。

## 公开读取

| 路径 | 行为 |
| --- | --- |
| `/api/health` | `{"status":"ok","service":"axolotl-update-server"}`，进程健康检查 |
| `/api/versions` | `{"versions":[...]}`，按 SemVer 降序，包含各状态版本和制品元数据 |
| `/api/versions/:version` | 单个版本详情；不存在返回 404 |
| `/latest` | Tauri 更新清单；无合适更新返回 204 |
| `/api/downloads/latest` | 最新下载目录；无合适版本返回 204 |
| `/api/downloads/:version` | 指定已发布版本的下载目录；不存在或未发布返回 404 |
| `/dist/:version/:filename` | 配置 `DIST_SERVER` 时 302，否则只提供本地 `manifest.json` |

版本详情字段保持旧协议：`version`、`channel`、`status`、`notes`、`release_tag`、`release_id`、`published_at`、`minimum_version`、`force_update`、`artifacts`。

制品字段：`platform`、`target_platforms`、`architecture`、`kind`、`variant`、`display_name`、`sort_order`、`is_public`、`filename`、`relative_path`、`size`、`sha256`、`signature`、`signature_filename`、`content_type`。数据库主键不在版本详情中输出，下载目录条目的 `id` 保留。

### `/latest`

请求头优先于查询参数，空请求头回退查询参数：

| 请求头 | 查询参数 | 默认值 |
| --- | --- | --- |
| `X-Axolotl-Channel` | `channel` | `release` |
| `X-Axolotl-Platform` | `platform` | 不筛选平台 |
| `X-Axolotl-Version` | `current_version` | 不筛选当前版本 |

`channel` 只允许 `release` 或 `beta`。Release 选择已发布稳定版本；Beta 选择已发布稳定 Release 与 Beta 预发布中的最高版本。指定当前版本时，只选择更高版本；撤销版本不参与。只返回含非空签名、元数据有效的 updater 制品，通用 macOS 更新器可以同时映射两个目标平台。

支持的平台：`windows-x86_64`、`linux-x86_64`、`linux-aarch64`、`darwin-x86_64`、`darwin-aarch64`。

```json
{
  "version":"1.9.4",
  "notes":"Release notes",
  "pub_date":"2026-08-29T15:19:00Z",
  "published_at":"2026-08-29T15:19:00Z",
  "force_update":false,
  "platforms":{
    "linux-x86_64":{
      "signature":"tauri-signature",
      "url":"https://update.axlmc.org/dist/1.9.4/update.tar.gz"
    }
  }
}
```

ETag 由响应字段计算，匹配 `If-None-Match` 返回 304。缓存默认 30 秒，最多 256 个变体；成功业务变更立即失效。ETag 是不透明值，切换实现后可能变化。

200/204/304 响应包含三项 `X-Axolotl-*` 的 `Vary`。查询参数方式允许短期 CDN 缓存；请求头方式默认 `private`，除非 `LATEST_CDN_HEADER_CACHE_KEY=true` 且 CDN 已正确配置三项请求头及查询参数缓存键。Authorization/Cookie 请求及错误响应为 `no-store`。

### 下载目录

`/api/downloads/latest` 接受 `channel`，默认 `release`。两种目录接口都接受：

- `include_updater`：默认 false，值不区分大小写，仅 `true` 启用。
- `include_revoked`：默认 false，为 true 时必须管理员认证；指定版本时允许读取撤销版本。latest 始终按已发布版本选择。

默认排除 updater、签名附件和非公开制品。目录格式：

```json
{
  "version":"1.9.4","channel":"release","status":"published",
  "published_at":"2026-08-29T15:19:00Z","force_update":false,
  "downloads":[{
    "id":42,"kind":"installer","platform":"windows",
    "target_platforms":["windows"],"architecture":"x86_64","variant":"modern",
    "label":"Windows installer","display_name":"Windows installer","sort_order":0,
    "filename":"installer.exe","url":"https://update.axlmc.org/dist/1.9.4/installer.exe",
    "size":123,"sha256":"64位十六进制摘要","signature":null,"signature_filename":null
  }]
}
```

目录响应包含 ETag 和 `no-cache, max-age=0, must-revalidate`。GET/HEAD/OPTIONS 的成功、空响应和错误均允许 `Access-Control-Allow-Origin: *`，不允许跨域凭据；Webhook 和管理写接口不开放 CORS。

### 分发

`DIST_SERVER=https://downloads.example.com/base` 时：

```text
/dist/1.9.4/a%20b.exe?x=y
  → https://downloads.example.com/base/dist/1.9.4/a%20b.exe?x=y
```

302 为 `no-store`。拒绝不安全路径、自指向配置或运行时循环。未配置分发服务器时只提供本地 `manifest.json`，支持 HEAD、条件请求和 Range；不存在返回 404。清单仍使用旧协议的 immutable 缓存，因此强制更新策略以动态 `/latest` 为准。

## Release Webhook

`POST /api/webhook/release`。工作流先发布公开 GitHub Release，再发送包含发布元数据和目录的 JSON；无须把安装包上传到本服务。

```text
X-Webhook-Timestamp: <Unix秒>
X-Webhook-Signature: sha256=<HMAC-SHA256十六进制>
```

签名输入为原始字节 `timestamp + "." + request_body`，默认时间窗 ±300 秒。计算签名之后不能重新格式化 JSON。

```json
{
  "event_id":"github-v1.9.4-123",
  "version":"1.9.4","tag":"v1.9.4","channel":"release","force_update":false,
  "release":{
    "id":123,"tag_name":"v1.9.4","draft":false,
    "body":"Release notes","published_at":"2026-08-29T15:19:00Z",
    "assets":[
      {"name":"update.tar.gz","size":100,"digest":"sha256:<摘要>","browser_download_url":"https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz"},
      {"name":"update.tar.gz.sig","size":20,"digest":"sha256:<签名文件摘要>","browser_download_url":"https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz.sig"}
    ]
  },
  "catalog":{
    "version":"1.9.4",
    "files":[
      {"filename":"update.tar.gz","size":100,"sha256":"<摘要>","downloadUrl":"https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz"},
      {"filename":"update.tar.gz.sig","size":20,"sha256":"<签名文件摘要>","downloadUrl":"https://github.com/Axolotl-Launcher/Axolotl/releases/download/v1.9.4/update.tar.gz.sig"}
    ],
    "artifacts":[{
      "filename":"update.tar.gz","kind":"updater","platform":"linux-x86_64",
      "architecture":"x86_64","targetPlatforms":["linux-x86_64"],
      "signatureFilename":"update.tar.gz.sig","is_public":true
    }]
  }
}
```

占位摘要和大小需要替换为实际文件信息。服务校验 catalog 与所签 Release 资产的一致性，签名文件额外下载校验；安装包摘要来自签名载荷，并不通过下载安装包重新计算。

- `release` 不能发布预发布版本，`beta` 必须是预发布版本，tag 必须为 `v<version>`。
- 制品 kind 支持 updater、installer、portable；架构支持 x86_64、aarch64、universal。
- updater 必须有对应 `<filename>.sig`，最大 64 KiB；验证大小、SHA-256、UTF-8 和非空内容。
- `targetPlatforms` / `target_platforms` 必须非空、无重复、包含主平台，不同 updater 不得覆盖同一个目标。
- 包平台可用 windows、macos、linux。可选 `variant`、`display_name`、`sort_order`、`is_public`。
- `event_id` 相同且原始体相同、已处理成功时返回 `{"status":"processed","idempotent":true}`；不同体返回 409。失败事件可使用相同原始体重试。
- 新发布返回 `{"status":"published","version":"1.9.4"}`。已经 published 的版本不可通过新事件覆盖，需先删除版本再发布。
- 同版本并发变更返回 `409 version_busy`。

## 版本管理

以下接口均要求管理员令牌。可选 JSON 元数据：`{"operator":"admin","reason":"..."}`，operator 默认 admin，必须为 1～128 字符的非空字符串；reason 必须为字符串。

| 方法与路径 | 行为 |
| --- | --- |
| `POST /api/admin/versions/:version/revoke` | 标记撤销、更新渠道指针、写审计 |
| `POST /api/admin/versions/:version/restore` | 验证元数据与 updater 签名后恢复，不下载安装包 |
| `PATCH /api/admin/versions/:version/force-update` | 必须提供 JSON 布尔 `force_update`，更新数据库和本地清单 |
| `DELETE /api/admin/versions/:version` | 删除版本、制品、关联 Webhook 和清单目录，保留审计与请求明细 |
| `POST /api/admin/versions/:version/cleanup` | 仅清理已删除版本的隔离文件，现存版本返回 `409 version_exists`，绝不删除其元数据 |
| `GET /api/admin/audit-logs` | `{"logs":[...]}`，按时间倒序 |

撤销/恢复返回 `{"status":"revoked或published","version":"..."}`。

强制更新返回 `version`、`status`、`force_update` 和 `idempotent`。重复设置不新增审计，不恢复撤销版本。缺清单为 `409 manifest_unavailable`，内容无效为 `409 invalid_manifest`；失败恢复原清单。

删除返回 `status`、`version`、`idempotent`、`deleted_artifacts`、`deleted_webhook_events`、`channel_pointers`、`files_cleanup_pending`。文件先移到公开目录外的隔离区；提交失败移回，提交后清理失败则 `files_cleanup_pending=true`，重试 DELETE 完成清理。清理完成后再次删除返回 404。重新发布前必须完成待处理清理。

管理界面的“重试清理”应使用专门的 POST cleanup 接口，不重复 DELETE：同版本号可能已被其他管理员重新发布。cleanup 使用相同版本锁，在版本存在时拒绝，版本不存在且已无隔离文件时返回幂等成功。旧 DELETE 的清理重试行为仍保留兼容。

审计字段为 `operator`、`action`、`channel`、`version`、`reason`、`request_id`、`ip_address`、`created_at`。版本删除不撤回已缓存的客户端文件，也不删除远端 GitHub 或分发服务器文件。

## 已移除接口

`PUT /api/artifacts/*` 返回 404。`GET /api/admin/stats` 已提供新的 dashboard API，使用管理员 Bearer 认证；子接口、字段、筛选和分页详见 [统计 API v2](STATS_API.md)。它替代旧 Python 统计结构。

