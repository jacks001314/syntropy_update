# Syntropy 升级服务设计（syntropy_update）

> 状态：待评审 · 初稿 · 2026-10-05
> 服务域名：`app.syntropy-lab.com`（生产环境强制 HTTPS / 443）
> 目录：`../syntropy_update`（独立于主仓库 `Syntropy` 的配套工程）

## 1. 三条不可动原则

1. **升级只由桌面应用实施**。桌面端是唯一升级执行器：桌面自升级、本地 Master/App Server
   升级、远端 Node 及其组件升级，全部由桌面端决策并发起。Master / Node 不自行检查更新、
   不自行下载、不自行升级。
2. **安装器分两种形态**：
   - **全包安装器（full）**：内嵌全部组件，离线可装；
   - **最新安装器（thin）**：只内嵌 Electron 外壳 + 引导器，`template / skills / plugins /
     codex cli / mbot / nbot / syntropy` 等组件在安装时从服务器按 BOM 下载。
3. **安装后按策略巡检组件变化并编排升级计划**：定期采集组件库存 → 与渠道基线比对出漂移 →
   按策略生成有序、分批、可回滚的升级计划 → 桌面端执行并回报。

## 2. 组件清单（受管范围）

受管组件只有 5 个：**产品**一个，加四个**插件**。

| 组件名 | 类型 | 成员 / 制品 | 版本线 |
| --- | --- | --- | --- |
| `product` | 产品 | `desktop` / `mbot` / `nbot` / `node-repl` / `app-server` 五个**槽位** | 产品版本 `0.4.3`（一处：`apps/desktop/package.json`） |
| `codex` | 插件 | 归档 `.tgz`，按 (os,arch) | 独立，**等于 SDK 的 `GeneratedCodexVersion`**（当前 `0.160.0`） |
| `node-runtime` | 插件 | 裸二进制，按 (os,arch) | 独立（`node-runtime-release.json`，当前 `24.14.0`） |
| `templates` | 插件 | 平台无关 `.tar.gz` 目录归档（`templates-<版本>.tar.gz`） | 内容版本（独立） |
| `skills` | 插件 | 平台无关 `.tar.gz` 目录归档（`skills-<版本>.tar.gz`） | 内容版本（独立） |

**规则**：产品只有一个版本号、一次发版全部一起出，不允许只发其中一个成员；插件各自有
版本号，可以随时单独发布并推进 BOM。产品成员不再各自成一行——每次版本变更前后端基本都
要一起改，拆开发布只换来「漏发一件」这类事故。

槽位是产品内部唯一的区分：制品存 `(slot, os, arch)`，同一个 `(os, arch)` 在产品里会有
五件（mbot / nbot / node-repl / app-server / desktop），客户端按槽位选件；非产品组件的
槽位等于组件名本身，于是选件、暂存、换件三处在两类组件上是同一个口径。

`plugins` 与 `browser-runtime` 不在受管清单里：前者走 Master 侧 marketplace（git 镜像，
见决策 15），后者当前没有生产者，等有产物再作为插件加入，不保留断链（见决策 17）。

## 3. 总体架构

```
                     CI（GitHub Actions）
                          │ import-bundle / publish
                          v
            ┌──────────────────────────────────────┐
            │  Syntropy Update Service             │
            │  app.syntropy-lab.com (HTTPS/443)    │
            │  ─ 制品仓库（内容寻址 blob）           │
            │  ─ 组件/版本/渠道/兼容约束             │
            │  ─ 分发定义 BOM + 签名清单             │
            │  ─ 灰度 / 熔断 / 升级策略              │
            │  ─ 库存上报 / 计划计算 / 车队状态 / 审计 │
            └──────────────────────────────────────┘
                  ▲                    ▲
      清单/制品/计划 │                    │ 发布管理
                  │                    │
        ┌─────────┴─────────┐     ┌────┴────┐
        │  桌面应用（执行器） │     │  运维/CLI │
        │  desktop_app      │     └─────────┘
        └─────────┬─────────┘
    本地 sidecar   │  组件制品经 web_backend → Master
   (Master/AppSrv) │
                  v
            ┌───────────┐   mdeploy(SFTP, 现状)   ┌───────────┐
            │  Master   │ ──────────────────────▶ │   Node    │
            └───────────┘                          └───────────┘
```

关键点：

- **升级服务只对桌面端与 CI 开放**；Node 不连升级服务，只连 Master（用户已确认"桌面不连 node"）。
- 字节通道：桌面下载并校验后，本地 sidecar 直接替换；远端 Node 组件经
  `web_backend → Master` 的新增上传接口交给 Master，Master 落到 `nodeArtifacts`
  并**复用现有 `mdeploy` SFTP 下发**。Master 只搬运，不做决策。
- 复用现有资产：`ControlHello.node_version`（库存上报）、`ControlCommand{domain,action}`
  （升级指令与 ack）、`sidecar-supervisor.ts`（本地生命周期）、
  `desktop_app/src/main/updates/update-service.ts`（自更新签名协议）。

## 4. 领域模型

- **component**：受管组件，含 `name`、`displayName`、`kind(binary|archive|installer|content)`、
  `versioning(product|independent)`、`compat`（如 `min_product_version`）。
- **release**：`component + version + channel + notes + publishedAt + minimumSupportedVersion +
  killSwitch(expiresAt, minimumSafeVersion) + artifacts[]`。
- **artifact**：`os + arch + filename + size + sha256 + blobDigest + kind`。
- **channel**：`alpha | beta | stable`，含 `currentRelease`（可空）与**分发策略**。
- **distribution（BOM）**：`productVersion + channel → components[]`（每个组件锁定版本），
  是"某一产品版本的已知良好组件集"。thin 安装器的下载依据、漂移检测的基线。
- **policy（策略）**：
  - `approval`：`auto | confirm | manual`（是否自动应用）；
  - `window`：维护窗口（如 `02:00-05:00`）；
  - `rollout`：`percentage`（`hash(node_id+release)` 确定性分桶）/ `batches`（分批 + 沉降时间）/
    `targets`（按 node_id / labels 白名单）；
  - `concurrency`：并发上限、单节点串行；
  - `preconditions`：无运行中任务、节点健康、磁盘余量；
  - `rollback`：失败自动回滚到上一版本。
- **node / inventory**：`node_id`、`labels`、各组件已装版本、`last_seen`、`product_version`。
- **plan / step**：`plan_id`、目标节点、组件、from→to、状态（pending/running/succeeded/
  failed/skipped/rolled_back）、`trace_id`、时间戳。
- **audit**：发布、改策略、回滚、忽略版本等管理动作留痕。

## 5. 清单与签名协议

### 5.1 桌面自更新清单（兼容现状，不改客户端）

沿用 `desktop_app/apps/desktop/src/main/updates/update-service.ts` 已定义的协议：

```
GET https://app.syntropy-lab.com/v1/manifest?product=desktop&channel=stable&os=windows&arch=amd64
→ { "payload": { artifacts[], channel, expiresAt, killSwitch, minimumSupportedVersion,
                 publishedAt, releaseNotesUrl, version }, "signature": "<base64 ed25519>" }
```

客户端已强制的约束（服务端必须满足）：

- 仅 `https`，`url.hostname === allowedHost`，**不允许端口/账号**，`redirect: "error"`；
- `releaseNotesUrl` 与 `artifact.url` 必须同属 `app.syntropy-lab.com`；
- 清单 ≤ 256 KiB；payload 字段严格 schema（多余字段会被拒）；
- `signature` = Ed25519 对 **规范化 JSON** 的签名，64 字节 base64；
- 客户端拒绝降级、拒绝渠道不符、拒绝过期；`killSwitch` 生效 → `blocked`，
  低于 `minimumSupportedVersion` → `required`。

> 规范化 JSON 定义（必须逐字节复刻）：对象键按 `localeCompare` 升序；无空白；
> 字符串用 `JSON.stringify` 语义（`\uXXXX` 只用于控制字符与非 ASCII 的转义规则与 JS 一致）；
> 数组保序。服务端需与 TS 实现用**共享测试向量**对齐，见 §12。

### 5.2 组件清单（BOM，新增，同样签名）

```
GET https://app.syntropy-lab.com/v1/distributions/{channel}/{productVersion}?os=&arch=
→ { "payload": { product, productVersion, channel, publishedAt, expiresAt,
                 components[{ name, version, kind, artifacts[{os,arch,filename,sha256,size,url}] }] },
    "signature": "..." }
```

`GET /v1/distributions/latest?channel=&os=&arch=` 返回该渠道当前 BOM 指针。

### 5.3 组件独立清单

```
GET https://app.syntropy-lab.com/v1/components/{component}/manifest?channel=&os=&arch=
```

用于 `codex` / `node-repl` / `node-runtime` / `browser-runtime` 等独立版本组件的单独升级。

### 5.4 公钥与下载

- `GET /v1/pubkey`：Ed25519 公钥（PEM，SPKI），供客户端与安装器内置/轮换校验。
- `GET /v1/blobs/{sha256}`：内容寻址下载，`ETag: "<sha256>"`，支持 `Range` 断点续传。

## 6. 两种安装器

### 6.1 全包安装器（full）

- 维持 `desktop_app/tools/build.mjs` 现状：全部组件内嵌。
- 产物内嵌一份**同版本 BOM**，安装后即达到 BOM 定义的状态，可与后续漂移检测对齐。

### 6.2 最新安装器（thin）

- 只内嵌 Electron 外壳 + 引导器 + **烘死的** 服务地址（`https://app.syntropy-lab.com`）、
  默认渠道、公钥。
- 首启引导（复用 Codex 的首启路由思路：`/first-run` → `/welcome` → `/select-workspace`）：
  1. 拉 BOM（默认渠道，可在引导中改渠道）；
  2. 按 (os,arch) 下载全部组件，逐件校验 sha256，支持续传与失败重入；
  3. 物化到组件根 `<userData>/components`（thin 外壳不带组件，也**不改写自己所在的包**），
     写库存锚点 `<组件根>/release-manifest.json`；
  4. 启动 Master / App Server（`--node-dir` 仍指向随包 `node` 拿 conf 模板，
     `--node-artifacts-dir` 指向 `<组件根>/artifacts`），进入主界面。
- **下载/校验/物化与后续升级执行器是同一套代码**，避免两套逻辑漂移。
- 两种安装器必须收敛到同一组件状态（同一 `productVersion` → 同一 BOM）；差别只是
  「随包内嵌」还是「首启下载」，取件位置始终是「组件根在前、随包目录兜底」。

构建（本次新增）：`desktop_app/tools/build.mjs resources --thin`（Windows 走
`build.cmd -Target Installer -Thin -UpdatePublicKeyFile <pubkey.pem>`）产出**只带外壳**的
资源树——`resources/config`（默认配置）、`resources/node/conf`（Master 部署节点要读的模板）、
`resources/bootstrap.json`（`baseUrl` + `channel` + `publicKeyPem`）；`resources/bin`、
`resources/node/artifacts`、`resources/data`、`resources/release-manifest.json` 一律不进包，
全包构建留下的残留会被主动删掉（否则在同一个工作区里先全包后 thin 会把上一轮几百 MB 的
组件悄悄打进 thin 包）。`forge.config.ts` 用 `SYNTROPY_THIN_BUILD=1` 切到这份
`extraResource`；安装包产物名后缀 `-thin`，同一份 Release 里两种安装器不会互相覆盖。
反向也做了对称清理：全包构建会删掉上一轮 thin 留下的 `resources/bootstrap.json`——
包里只要还留着它，首启就会把「全包」当成 thin 去服务端下组件（离线装机直接坏掉）。
地址必须 https、公钥必须是 Ed25519 SPKI PEM，构建期就拦住
（`tools/update-bootstrap-config.mjs`），不会把一份「首启才发现无效」的 bootstrap.json
发出去；`tests/security/update-bootstrap.test.ts` 把构建器产出的字节直接喂给运行时
`resolveUpdateConfig`，钉住两侧契约。

## 7. 巡检、漂移与升级计划（原则 3）

### 7.1 库存采集

桌面端周期性采集（并可手动触发）：

周期默认 **12 小时一轮**、启动后延迟 5 分钟起跑（`SYNTROPY_UPDATE_SWEEP_INTERVAL_MS=0`
可关闭定时巡检，设置页仍可手动检查与下载）；同一时刻只跑一轮，慢轮不堆叠。
定时巡检只做「采集库存 → 取计划 → 自动下载」，**不自动安装**。

- 桌面自身：`app.getVersion()`、构建日期、渠道、平台；
- 本地 sidecar：Master / App Server 版本（本地可执行文件版本或 `release-manifest.json`）；
- 远端 Node：`node_version`（`ControlHello`）+ 各组件版本
  （`nbot` / `codex` / `node-repl` / `node-runtime` / `browser-runtime`），经 Master 读回；
- 内容制品：`templates` / `skills` / `plugins` 清单与版本。

### 7.2 漂移

`inventory` 对比 `渠道基线（component manifest + BOM）`，产出
`missing | outdated | ahead | unknown`，并按兼容约束（如 `min_product_version`）过滤掉不可用版本。

### 7.3 策略与计划

- 服务端按策略计算计划：`POST /v1/plan`（提交库存）→ 返回漂移 + 有序步骤 + 每步制品 +
  前置检查 + 回滚点；桌面端**只执行与回报**。
- 计划可 暂停 / 继续 / 回滚；所有步骤带 `trace_id`、`plan_id`、`operation_id`。
- 前置检查示例：无运行中任务/会话、节点在线且健康、磁盘余量、维护窗口内、未处于 freeze。

### 7.4 审批口径（对齐 Codex app 逆向结论）

- **自动检查 + 自动下载是默认**（对应 `appUpdate.recovery.downloadingUpdate`、
  `~/.codex/version.json` 的 `latest_version` / `last_checked_at`）。
- **安装前人工确认**（对应 `appHeader.installUpdate.confirmTitle` "Update {appName} now?"，
  副标题明确"会退出并中断本机活动会话"）→ 与前置检查"无运行中任务"同源。
- **内容类**（templates/skills/plugins）默认 `auto`；**二进制/运行时类**默认 `confirm`；
  是否放开为全自动由服务端策略决定。
- 支持**忽略此版本**（对应 `dismissed_version`）。忽略记在**目标**身上（`(targetId, component)
  → version`，对应 Codex app 落在本机 `version.json` 的那个字段），不是 `(组件, 渠道)` 策略——
  一台机器不想升级不该拖住整条渠道。计划里被忽略的项**照样出现**（`skip` + `deferred` +
  `ignored: true` + 原因「已忽略此版本 x.y.z」），界面才能说明白「为什么没升」并提供撤销；
  只记一个版本，所以渠道上出现更新的版本时忽略自动失效、升级重新被提出。撤销走
  `DELETE /v1/targets/{id}/ignores/{component}`。
- **强制升级/限时重启**（对应 `appUpdate.relaunchNotice.titleDays/Hours/Minutes/Now` +
  `restartNow`）由 `minimumSupportedVersion` / `killSwitch` 驱动，客户端只读展示。
- 更新开关在设置页是**只读策略行**（对应 `settings.general.inAppUpdates`，托管时显示
  `Managed` / "Your organization has turned off in-app updates"）。

## 8. 桌面端 UX 落点（对齐 Codex app settings）

- 新增数据驱动设置页 slug `updates`（沿用注册表 + capability 门控 + 可见性 hook + 搜索索引
  的既有机制），页内含：
  - 只读策略行 `In-app updates`（`Auto` / `Confirm` / `Managed`）；
  - 当前各组件版本与渠道、检查更新、逐行「忽略此版本 / 不再忽略」、查看发布说明；
  - 升级计划视图：步骤、进度、暂停/继续/回滚。
- 菜单栏「检查更新…」沿用现有 `update.check`，结果回填对话框。
- 升级过程 UI 对齐 `appUpdate.installProgress.*`（进度、preparing、restart 提示）。
- 首启引导对齐 `/first-run`、`/welcome`、`/select-workspace`。

## 9. 字节通道

| 目标 | 通道 |
| --- | --- |
| 桌面自身 | 下载安装器 → 校验 → `shell.openPath` 交给平台安装器（Windows NSIS / macOS dmg·pkg / Linux deb·rpm）→ 退出应用 → 安装器覆盖程序目录并重新拉起 |
| 本地 Master / App Server | 桌面 → 本机 App Server `POST /api/master/registry/{id}/binary`（`?sha256=` 校验、`?restore=` 回滚）→ 停进程 → 备份 → 原子替换 → 启动 → 失败自动回滚并重新拉起 |
| 远端 Node 组件 | 桌面校验后落本机 `<组件根>/artifacts/<组件>-<os>-<arch>`（`<userData>/components/artifacts`；Master 以 `--node-artifacts-dir` 按「组件根在前、随包目录兜底」搜索；远端 Master 时改走「上传 web_backend → Master」）→ `mdeploy` SFTP 下发 → `ControlCommand{domain=node, action=upgrade}` 编排与 ack（**触发重部署待实现**） |

要求：原子替换、保留上一版本用于回滚、每步 sha256 校验、失败可重入。

## 10. HTTP API（v1）

客户端读取（默认匿名放行——thin 安装器首启还没有任何令牌；`requireReadTokens: true` 时才校验令牌）：

- `GET /v1/pubkey`
- `GET /v1/manifest`（桌面自更新签名信封）
- `GET /v1/distributions/{channel}/{productVersion}`、`GET /v1/distributions/latest`
- `GET /v1/components/{component}/manifest`
- `GET /v1/blobs/{sha256}`（`Range` / `ETag`）

客户端上报（设备作用域：配了 `clientTokens` 就校验令牌；`requireDeviceTokens: true` 时即使
没配令牌也拒绝匿名写入）：

- `POST /v1/plan`（提交全量库存 → 漂移 + 计划，并落库）
- `POST /v1/inventory/{id}`（单目标库存上报：版本 / 组件）
- `POST /v1/targets/{id}/ignores`（上报「这个目标忽略这个组件的这个版本」）
- `DELETE /v1/targets/{id}/ignores/{component}`（撤销忽略）
- `POST /v1/targets/{id}/results`（上报一次组件升级的执行结果：installed / handed-off / failed / unsupported）

管理（Bearer / `X-Api-Key`，未配置管理令牌时一律拒绝）：

- `GET|POST /v1/admin/components`
- `GET|POST /v1/admin/releases`、`GET /v1/admin/releases/{id}`
- `PUT /v1/admin/releases/{id}/artifacts/{slot}/{os}/{arch}`（流式上传 + 校验；文件名走
  `X-Filename`、期望摘要走 `X-Sha256`。槽位是必填段：产品一次发布装着多件同平台制品，
  没有槽位就分不清传的是哪一件；非产品组件的槽位等于组件名，平台无关制品用 `any/any`）
- `GET /v1/admin/artifacts/{sha256}`（按内容摘要取件，`?filename=` 只决定下载名）
- `POST /v1/admin/releases/{id}/publish`、`POST /v1/admin/releases/{id}/yank`
- `GET|POST /v1/admin/distributions`
- `GET /v1/admin/inventory`、`GET /v1/admin/plans`、`GET /v1/admin/plans/{id}`
- `GET /v1/admin/ignores`（可带 `?target=`）
- `GET /v1/admin/results`（升级结果，可带 `?target=&limit=`）
- `GET /v1/admin/policies`、`PUT /v1/admin/policies/{component}/{channel}`
- `GET /v1/admin/audit`

探针：`GET /healthz` 返回 `status` / `adminEnabled` / `clientAuthRequired` /
`deviceAuthRequired`——管理页据此区分「服务端压根没配 `adminTokens`」与「令牌填错」，未登录时
也能给出可执行的排查步骤；上报接口是否要求令牌同样一眼可见。

运维页（`webui` 内嵌单页）：

- `GET /admin` → `302 /admin/`；`GET /admin/` 及其静态资源（SPA fallback、`Cache-Control: no-store`）。
  页面本身不校验令牌，其调用的 `/v1/admin/*` 逐条走 Bearer Token；响应另带
  `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'`、
  `Referrer-Policy: no-referrer`、`X-Frame-Options: DENY`。
- 11 个页面：概览 / 组件 / 发布 / 分发定义 / 库存 / 忽略集合 / 升级计划 / 升级结果 / 分发策略 / 审计 / 说明。
  表格都带过滤（过滤条件按页缓存在内存），写操作走「忙碌 → 结果 → 重渲染」，重渲染后保留
  正在查看的发布制品面板；上传时在浏览器端算 sha256。

本地 Master 组件换件（**主仓库** `web_backend`，不属于本服务）：

- `POST /api/master/registry/{id}/binary`（流式上传单体二进制；`?sha256=` 校验、`?restore=` 从
  `<runDir>/backups/<时间戳>` 回滚；停 → 备份 → 换 → 起，失败自动回滚并重新拉起）。

CLI：`serve | keygen | pubkey | publish | import-bundle | distribution <publish|show> | manifest |
plan | inventory <list|report> | results <list|report> | policy <show|set> |
ignore <list|set|clear> | list | audit`

## 11. 存储与目录布局

- 元数据：**bbolt**（延续主仓库约定）。bucket：`components`、`releases`、`artifacts`、
  `channels`、`distributions`、`policies`、`inventories`、`plans`、`results`、`ignores`、`audit`。
- 制品：**内容寻址 blob**，`data/blobs/<sha256[0:2]>/<sha256>`，原子写 + fsync + 去重；
  `ETag` 与 URL 都基于 sha256。
- 工程布局：

```
syntropy_update/
  go.mod  README.md  .gitignore
  docs/design.md
  conf/update.json.tpl
  cmd/syntropy-update/main.go     # CLI：serve/keygen/pubkey/publish/distribution/manifest/
                                  #      plan/inventory/results/policy/list/audit
  internal/config/                # JSON 配置
  internal/model/                 # 领域类型
  internal/blob/                  # 内容寻址 blob 存储
  internal/store/                 # bbolt 元数据
  internal/sign/                  # 规范化 JSON + Ed25519
  internal/release/               # 发布/制品/签名清单领域服务
  internal/policy/                # 版本比较、灰度桶、维护窗口、目标匹配
  internal/api/                   # HTTP 接口（客户端面 + 管理面）
  internal/auth/                  # Bearer Token 鉴权
  internal/{webui}/               # 预留（M4 管理页）
  pkg/updclient/                  # Go 客户端：拉签名清单 + 验签 + 下载校验
  tests/vectors/                  # 跨语言签名测试向量（Node 生成，Go 校验）
```

## 12. 安全与一致性

- **TLS**：生产强制 `https://app.syntropy-lab.com`（443），由服务内置 ACME 客户端自动签发与
  续期（`tlsDomains` 非空即启用，走 TLS-ALPN-01，只需 443 可被外网访问；证书缓存
  `data/acme`，80 不必放行）。客户端 host 白名单硬性要求无端口，因此必须自己占住 443，
  不能挂在非 443 端口上。
- **签名**：Ed25519；私钥 PKCS8 PEM、`0600`、不随镜像分发；公钥经 `/v1/pubkey` 与客户端内置双通道。
  **跨语言一致性测试**：同一 payload 在 TS 与 Go 下必须产出相同 canonical 串与相同签名，
  测试向量固化在 `tests/vectors/`。
- **完整性**：每个制品 sha256 随清单签名；下载后再校验；blob 写入先校验后落盘。
- **鉴权**：读接口（清单 / 制品 / 分发定义）**默认匿名**——thin 安装器首启必须能拉件，要收紧得
  显式打开 `requireReadTokens`（打开后没配令牌就是谁都进不来）；上报接口（`/v1/plan`、库存、
  忽略、升级结果）按设备作用域收紧：配了 `clientTokens` 就校验，另设 `requireDeviceTokens` 让
  「没配令牌」也能拒绝匿名写入。`deploy/install.sh` 新建配置即生成客户端令牌并写
  `requireDeviceTokens: true`（读取保持默认公开，否则新装出来的 thin 安装器引导会 401），
  同时打印令牌；管理接口用 API Key + scope，未配管理令牌时一律拒绝。老配置没有这两个开关时
  保持原行为，升级二进制不会打断已有车队。
- **审计**：发布、改策略、回滚、忽略版本、计划执行全部留痕，字段含 `trace_id` / `plan_id` / `node_id`。
- **熔断**：`killSwitch` 支持"某版本以下不安全"与"停止下发"两类语义，且**两端都真的执行**：
  计划引擎（`internal/release/plan.go`）在熔断生效期间「要发的这一版低于最低安全版本 → 该项
  `blocked`（停止下发）」、「目标当前版本低于最低安全版本 → 强制 `pending` + `auto`（忽略集合、
  灰度、维护窗口都不再拦）」；minimumSupportedVersion 同属安全下限，命中即强制。thin 安装器的
  首启引导没有计划引擎兜底，因此逐组件拉 `/v1/components/{c}/manifest` 核对下限，核对不上
  就不装（宁可暴露 BOM 指向被叫停版本，也不静悄悄装出去）。

## 13. 部署

- 服务以单进程方式部署在 `app.syntropy-lab.com`：明文管理面 `127.0.0.1:8443` + 公网 HTTPS
  `:443`（内置 ACME 自动签发，证书缓存 `data/acme`；绑定 443 用 `CAP_NET_BIND_SERVICE`）。
  443 归本服务，同机其它占用 443 的服务（xray REALITY）需挪到已放行的端口。
- 数据目录与 blob 目录落独立磁盘；备份 = bbolt 快照 + blob 目录。
- 运维管理页随服务二进制内嵌（`GET /admin/`），不需要单独部署前端或前端构建链。
- CI 灌库：`desktop-release.yml` 在 `publish` 后新增一步 `syntropy-update import-bundle`，
  把本次 Release 资产与 `release-manifest.json` 整体灌入服务并推进渠道指针：

  ```bash
  syntropy-update import-bundle \
    --manifest release-manifest.json --dir <产物根> \
    --channel stable --desktop windows/amd64=SyntropySetup-0.3.1.exe
  ```

  `--desktop` 的平台名用集群口径（`windows|darwin|linux` + `amd64|arm64`），**不是** Electron 的
  `win32/x64`；服务对外发桌面自更新清单时才映射回 `platform/arch`。`--dry-run` 只打印计划（CI 里
  可先当校验跑一遍），`--no-publish` 只建发布、传制品，不动渠道指针与 BOM。同一份输入重跑是幂等的；
  同一 (组件, 渠道, 版本) 换了制品字节则直接失败——已发布的版本不可变。

## 14. 分期与验收

| 阶段 | 内容 | 验收 |
| --- | --- | --- |
| **M1** | 服务骨架（bbolt + blob + 签名 + 管理 API/CLI）；桌面自更新清单打通（`UpdateService` 接进 `main.ts`） | 桌面菜单"检查更新…"能拿到真实清单并正确判定 `upToDate/available/blocked` |
| **M2** | 组件仓库 + BOM + thin 安装器引导下载/校验/物化 | thin 安装器在空机首启可从服务拉齐全部组件并启动 |
| **M3** | 库存采集 + 漂移检测 + 计划引擎 + 桌面升级执行器（本地 sidecar → 远端 Node），含回滚与熔断 | 能对一台远端 Node 完成 nbot/codex 升级并成功回滚 |
| **M4** | 桌面「升级」设置页 + 服务端管理页 + CI 自动灌库 | 运维可在 UI 完成发布、灰度、回滚；车队状态可视 |

## 15. 开放问题

1. ~~**Linux 桌面**~~（已决）：自更新清单 `platform` 枚举已扩为 `darwin|linux|win32`，
   三平台共用一条自更新通道（见决策 5）。
2. ~~`templates/skills/plugins` 三类内容制品的打包格式与版本粒度~~（已决）：三类共用一套
   「内容制品」口径——`kind=content` + `versioning=independent`，各自一条发布渠道
   （组件名就是 `templates` / `skills` / `plugins`）；制品是与 (os, arch) 无关的目录归档
   `.tar.gz`（`<组件>-<版本>.tar.gz`，解包后整目录替换组件根下对应子树），默认审批 `auto`
   （`DefaultApprovalFor` 已实现）。依据：Codex app 逆向材料里 plugins/skills 走的是
   marketplace 独立升级线（`marketplace/upgrade`），与 app 版本解耦；见决策 13。
3. ~~服务端管理页的技术形态~~（已决）：内嵌单页（`internal/webui`，`//go:embed` + 原生 DOM +
   `textContent` 防注入），不引入前端构建链；页面引用的接口由 `internal/api` 结构测试与
   `routes()` 双向比对。
4. ~~桌面端「安装执行器」的落地细节~~（已决）：自更新**交给平台安装器**，不做进程内替换
   （Codex app 在 macOS 走 Sparkle、Windows 走 MSIX，同样是"交给平台安装器"；本仓库是
   NSIS/DMG/DEB，`shell.openPath` 后退出，由安装器覆盖程序目录并重新拉起）。
5. ~~**可升级组件的落点（组件根）**~~（已决）：引入 `<userData>/components/` 组件根。
   运行时解析**逐条目**先组件根、再安装目录（`resolveResourceRoot`），thin 安装器与组件
   升级都物化到组件根；macOS 上 .app 包内任何文件都被代码签名覆盖，组件因此一律不进包。
   制品的落点是 `<组件根>/artifacts/<名>`（**不是** `<组件根>/node/artifacts/`）：组件根
   里没有 `node/` 这一层，`resolveResourcePath("node")` 就不会把一个只有 artifacts、没有
   `conf/*.tpl` 模板的半成品目录当成节点目录用。Master 侧新增 `--node-artifacts-dir`
   与制品搜索列表（`os.PathListSeparator` 分隔、靠前优先），全包/最新两种安装器因此共用
   同一条取件路径：升级过的先命中，没升级过的仍从随包目录取。

## 16. 决策记录

| # | 决策 | 结论 | 来源 |
| --- | --- | --- | --- |
| 1 | 字节通道 | 桌面不直连 Node；制品经 `web_backend → Master`，Master 复用 `mdeploy` 下发 | 用户确认 |
| 2 | 生产域名 | `https://app.syntropy-lab.com`（443，客户端 host 白名单要求无端口） | 用户确认 |
| 3 | 审批口径 | 自动检查 + 自动下载 + **安装前人工确认**；内容类可自动；策略在设置页只读展示 | Codex app `appUpdate.*` / `settings.general.inAppUpdates` |
| 4 | UI 形态 | 数据驱动设置页（新增 `updates` slug）+ 检查更新对话框 + 安装进度 + 限时重启提示 | Codex app settings 注册表 |
| 5 | Linux 桌面 | 自更新清单 `platform` 枚举扩为 `darwin|linux|win32`，三平台共用一条通道 | 用户确认 |
| 6 | thin 安装器引导 | 服务地址与公钥**构建期烘死**（`https://app.syntropy-lab.com` + 内置公钥），渠道在首启引导可选；开发/自建用 `update.json` / 环境变量覆盖，不做"安装时手填地址" | Codex app 的构建期占位符（`webview/index.html` 的 `PROD_BASE_TAG_HERE`）+ 用户确认 |
| 7 | 自动下载 | 检查 → 下载是自动的（无人值守），**安装**才需要确认；只下载计划里 `pending` 的组件，`deferred`/`blocked` 不下 | Codex app `appUpdate.recovery.downloadingUpdate` + 用户确认 |
| 8 | 服务端管理页 | 内嵌单页（`//go:embed`），令牌存 `sessionStorage`，页面与 `routes()` 双向结构测试锁定 | 本次实现 |
| 9 | 本机二进制换件 | 桌面 → 本机 App Server `POST /api/master/registry/{id}/binary`；停 → 备份（`<runDir>/backups/<时间戳>`）→ 换 → 起，失败自动回滚并重新拉起 | 本次实现 |
| 10 | 桌面端自更新 | 下载校验后 `shell.openPath` 交给平台安装器并退出本进程；安装器后缀按平台白名单校验（win32 `.exe/.msi`、darwin `.dmg/.pkg`、linux `.deb/.rpm/.AppImage`），桌面项永远排在计划最后 | Codex app 交给 Sparkle/MSIX + 用户确认 |
| 11 | 节点制品落点 | 落到组件根 `<userData>/components/artifacts/<组件>-<os>-<arch>`，名字与 `mdeploy` 的 `*ArtifactName` 完全一致；Master 以 `--node-artifacts-dir` + 制品搜索列表按「组件根在前、随包目录兜底」取件，三平台（含 macOS）适用 | 本次实现 |
| 12 | 本机 App Server 换件 | 桌面自己停/换/起：停止所有从该可执行文件起的实例（`clusterInstances`）→ 备份 → 原子替换 `<组件根>/bin/<platform>-<arch>/syntropy-app-server[.exe]` → `ready()` 重新拉起 → 健康检查失败就还原备份再拉起。**不复用** `POST /api/master/registry/{id}/binary`：那条路是让 App Server 换本机 Master（决策 9），换自己既拿不到稳定窗口，也违背「进程不给自己做手术」 | 本次实现 |
| 13 | 内容制品（templates/skills/plugins） | `kind=content` + `versioning=independent` + `.tar.gz` 目录归档 + 默认审批 `auto`；落点是本机 Master 数据目录 `<runDir>/data/skills` / `<runDir>/data/templates`（`syntropy_master` 的 `skillsDir` / `templatesDir`；plugins 走 marketplace 目录，落点实施时按 Master 侧确认），改完用已有的 `POST /api/node/redeploy` 把内容随 `data` 目录带到工作节点。需要主仓库新增「App Server 收归档 → 本机 Master 解包（备份 + 失败回滚）」的通道；**具体落点以决策 15 为准** | Codex app marketplace 独立升级线 + 本次讨论 |
| 14 | mbot 的双落点 | 本机 mbot 换件（决策 9）之外，同一份制品还要落到 `<组件根>/artifacts/mbot-<os>-<arch>[.exe]`：远端主节点部署（含跨平台）是从制品搜索目录取 mbot 的，只换运行目录会让「部署远端主节点」继续发老件 | 本次实现 |
| 15 | 内容制品落点 | 制品是平台无关的 `.tar.gz` 目录归档；解包后同时替换两处：部署源树 `<组件根>/data/<组件>`（本地 re-deploy 与远端节点 payload 都从这里取件）与本机 Master 的持久数据目录 `<runData>/master-data/<组件>`（`syntropy_master` 的 `skillsDir` / `templatesDir` 指向它，且 Master 逐次调用读盘，替换即生效、无需重启）。两处各自留备份与回滚；主节点播种时把部署源树当作**优先级更高的数据源**（`--master-data-dir` 指到本机 Master 持久目录，按「持久目录 → 随包 `data`」补缺、已存在的文件不覆盖）；`plugins` 走 Master 侧 marketplace，不在目录归档范围内。**已落地**，见 §17.3 | 本次讨论（依据：`web_backend/service/master` + `syntropy_master/handle/skill` 代码核实） |

| 16 | 443 归属 | 生产 443 归升级服务（内置 ACME 自动签发/续期，走 TLS-ALPN-01，80 不必放行）；同机既有 xray REALITY 挪到 `4430` 并同步安全组与客户端。xray 换非 443 端口会提示「可能被 GFW 封 IP」，是为让出 443 付出的已知代价 | 用户确认（「xray换个端口」） |
| 17 | 组件模型收敛为 5 行 | 受管组件只有 `product` + `codex` / `node-runtime` / `templates` / `skills`。产品一次发版装五件、共用一个版本号，靠 `slot` 区分；插件可独立升级。`browser-runtime` 移出清单（没有生产者），`node-repl` 归产品。制品键从 `(releaseID, os, arch)` 改为 `(releaseID, slot, os, arch)`，上传路由与清单都带 `slot`；服务端组件表（`internal/model`）与客户端（`component-layout.ts`）由门禁测试锁定一致 | 本次讨论（依据：`../Syntropy/docs/system-upgrade-method.md`） |
| 18 | 发布链路只有一条 | 本地与 CI 都跑 `desktop_app` 的 `pnpm release:build`（整理发布清单）+ `pnpm release:publish`（`import-bundle -remote`，走管理面 HTTP API）。组件表、槽位与制品命名只由服务端 `bundle.PlanAssets` 一处决定，桌面端脚本不复制第二套规则；本地不再需要人碰服务端数据目录 | 本次讨论 |
| 19 | BOM 的锚点是产品版本 | `PublishDistribution` 允许同一 `(channel, productVersion)` 被**重新发布**，条件只有一个：BOM 里的 `product` 条目与已发布的那份是同一份发布（`ReleaseID` 与版本都相同）。产品自己换了必须发新的产品版本；插件（codex / node-runtime / templates / skills）各有版本号，可以随时单独发布并推进 BOM。推进保留 `CreatedAtMS`、刷新 `PublishedAtMS`，审计记 `distribution.update`——不可变的是「产品这一格」，不是整份文档 | 本次讨论（规则 3「插件可单独升级」在服务端的落点） |

## 17. 实施状态（2026-10-05）

### 17.1 服务端（`../syntropy_update`）

已落地，`gofmt` / `go vet` / `go test ./...` 全绿：

- `internal/sign`：规范化 JSON + Ed25519 签名；跨语言测试向量（`tests/vectors/`）由 Node 生成、
  Go 校验，锁定与 `update-service.ts` 的字节一致性。
- `internal/blob`：内容寻址存储（原子写 + fsync + 去重 + digest 校验）。
- `internal/store`：bbolt 元数据（components / releases / artifacts / channels /
  distributions / policies / inventories / plans / audit）。
- `internal/release`：发布、制品入库（sha256 校验）、渠道指针、撤回、**桌面自更新清单**、
  **集群组件清单**、**分发定义 BOM** 的构建与签名，以及**策略 / 计划引擎**
  （`BuildPlan`：漂移比较、审批口径、灰度桶、维护窗口、目标匹配、产品版本门限）。
- `internal/policy`：纯函数——版本比较、确定性灰度桶、维护窗口、glob 目标匹配。
- `internal/api`：
  - 客户端：`GET /v1/pubkey`、`/v1/manifest`、`/v1/components/{component}/manifest`、
    `/v1/distributions/{channel}/{productVersion}`、`/v1/distributions/latest`、
    `/v1/blobs/{sha256}`（Range/ETag）、`POST /v1/plan`、`POST /v1/inventory/{id}`；
  - 管理：`/v1/admin/components`、`/v1/admin/releases`（含制品流式上传、发布、撤回）、
    `/v1/admin/distributions`、`/v1/admin/inventory`、`/v1/admin/plans`（含详情）、
    `/v1/admin/policies`（含 `PUT`）、`/v1/admin/audit`；Bearer Token 鉴权。
  - `routes()` 是唯一事实来源：注册路由与「管理页引用的接口是否存在」共用同一张表。
- `internal/webui`：`//go:embed assets` 的内嵌管理页（原生 DOM，`textContent` 防注入，
  令牌存 `sessionStorage`），SPA fallback + `Cache-Control: no-store` + CSP/防嵌入响应头。
  11 个页面（概览…说明）各自带过滤与空态；未登录时按 `/healthz` 的 `adminEnabled` 区分
  「服务端没配令牌」与「令牌错」并给出 systemd 步骤；`tests/ui/dom-smoke.mjs` 用 jsdom 把
  `app.js` 真跑一遍（含内容制品上传、过滤、注入转义），可对本地或线上服务执行。
  静态资源挂在 `/admin/` 时必须**剥掉前缀**再交给 FileServer：否则 `app.css` / `app.js`
  会被 SPA 回落成 `index.html`，页面仍是 200，但只剩一个无样式、无脚本的静态壳
  （`admin_ui_test.go` 按 Content-Type 钉住，DOM 冒烟也会先探一次真实资源）。
- `deploy/`：`install.sh`（幂等：建 `syntropy-update` 系统用户、`/opt/syntropy-update` 二进制、
  `/etc/syntropy-update` 配置与签名私钥、`/var/lib` 数据、`/var/log` 日志、systemd 单元；
  首次部署自动生成 48 位十六进制管理令牌并打印）+ `syntropy-update.service`
  （`ProtectSystem=strict`、`/etc` 与 `/opt` 只读、只有数据与日志目录可写；明文管理面
  `127.0.0.1:8443`，公网 HTTPS `:443` 靠 `CAP_NET_BIND_SERVICE`）。
- 结构测试：`internal/api/routes_test.go` 解析 `app.js` 的 API 表并与 `routes()` 双向比对
  （页面指向不存在的接口、或管理接口没接进页面都会失败）。
- `pkg/updclient`：Go 客户端（拉签名清单 + 验签 + 挑选 (os,arch) 制品 + 下载并校验 sha256），
  供 Master / Node 复用；已用真实的 httptest 服务做端到端测试。
- `cmd/syntropy-update`：`serve / keygen / pubkey / publish / distribution / manifest /
  plan / inventory / policy / list / audit`（`import-bundle` 是 CI 灌库入口，契约见 §13）。
- `internal/bundle`：CI 灌库的规划与执行。`PlanAssets` 是纯函数（构建清单 → 要发什么），
  `Import` 按「建发布 → 传制品 → 推渠道指针 → 发 BOM」执行：指针一旦指过去，客户端立刻就能
  拉到这份清单，所以制品必须先在库里，BOM 放在全部组件就绪之后再发。
  灌库只认客户端真正会去取的那套命名——os/arch 仅 `windows|darwin|linux` + `amd64|arm64`，
  制品名必须是客户端落盘名（`<组件>-<os>-<arch>[.exe]`、codex 用 `codex-<os>-<arch>.tgz`、
  app-server 用 `syntropy-app-server[.exe]`），命名或平台对不上就当场失败，不让「服务发了、
  客户端找不到」这种账留到运行期。mbot 的两处来源（宿主 `bin/…/syntropy-master.exe` 与
  `node/artifacts/mbot-<os>-<arch>`）按 (os,arch) 去重并保留 `node/artifacts` 那份——那才是本地
  Master 部署与节点重下发真正取的一份；被跳过/降级的项写进 note 如实打印，不静默吞掉。
  产品版本 `0.3.1.2026-10-05` 归一成 `0.3.1`，codex 用清单里的独立版本线（现在是 `0.160.0`）。
- 内容制品（本轮新增）：`kind=content` + `versioning=independent`，制品与 (os,arch) 无关——
  `os` / `arch` 置空、`filename = <组件>-<版本>.tar.gz`。上传走
  `PUT /v1/admin/releases/{id}/artifacts/any/any`（Go 1.22 的 `PathValue` 匹配不了空路径段，
  用 `any` 当哨兵）；`bundle.PlanAssets` 新增 `Content []ContentArtifact`，`import-bundle`
  与 `publish` 都新增 `-content`；`pkg/updclient` 的 `Manifest.Pick` 在精确匹配落空时回落到
  平台无关件；`release.UploadArtifact` 要求 `os`/`arch` 同时给出或同时为空，文件名必须等于
  `<组件>-<版本>.tar.gz`。`publish -content` 与 `import-bundle -content` 都实跑过
  （`-dry-run` 计划与真实灌库各一次，内容制品以 `os:""/arch:""` 进 BOM）。
- 「忽略此版本」（本次新增）：`bbolt` 的 `ignores` bucket（key = `targetId/component`）+ 服务层
  `SetIgnore/ClearIgnore/ListIgnores`（校验、审计 `ignore.set` / `ignore.clear`、保留创建时间）+
  计划引擎按目标一次性取忽略集合，命中 `(target, component, version)` 时把该项标成
  `skip`/`deferred`/`ignored=true`。客户端作用域的 `POST /v1/targets/{id}/ignores` 与 `DELETE`、
  管理端的 `GET /v1/admin/ignores`（运维页新增「忽略集合」页，结构测试要求接口与页面双向对齐）、
  CLI `ignore <list|set|clear>` 都已接通。
- 「升级结果回收」（本次新增）：`bbolt` 的 `results` bucket + 服务层 `RecordResult/ListResults`
  （状态白名单 `installed|handed-off|failed|unsupported`、审计 `result.report`、服务端补 id 与时间戳）+
  客户端作用域 `POST /v1/targets/{id}/results` 与管理端 `GET /v1/admin/results`（运维页新增
  「升级结果」页）、CLI `results <list|report>`。桌面端 `updates/upgrade-results.ts` 在 `update.install` 里每装完一步回传一次
  ——桌面端自己那一项交给平台安装器后进程随时退出，攒到最后一起报会把前面几步丢掉；回传是
  尽力而为，失败只计入安装说明（`N 条升级结果未能回传。`），不改写这一步的成败。
  这一步补齐的是「库存 vs 结果」的另一半：库存回答现在跑的是哪一版，结果回答这次升级成没成，
  也是将来自动回滚的判据。
- 「熔断真的被执行」（本次修正）：此前 `killSwitch` / `minimumSupportedVersion` 只是签名清单里
  的两个字段，没有任何一处消费它们——发出去也不会生效。现在计划引擎按上文两类语义执行，thin
  首启引导逐组件核对；桌面端 `cluster-update.ts` 的 `safetyViolation` 与服务端同一口径，
  两侧对「什么算低于安全线」不会各说各话。
- 「计划按目标分档」（本次修正）：此前所有目标都拿「渠道基线的全部组件 + 所有目标上报过的组件」
  当候选，于是给一台只报了 `nbot` 的工作节点计划 `mbot` / `app-server` / `desktop` 这些它根本
  不承载的条目。现在 `targetComponentNames` 按目标类型分档：`desktop` 是整条 BOM 的落地端
  （制品都要拉到本机再决定谁生效、给节点重下发），整条基线都参与它的计划；`master` / `node`
  只计划它自己上报过的组件。已有目标的计划不变（桌面端仍拿全量）。
- 「客户端鉴权分档」（本次修正）：此前 `clientTokens` 为空即所有客户端接口匿名放行，
  `deploy/install.sh` 又恰好默认写空数组——新装出来就是公开的上报入口。现在拆成读取 / 上报两档
  （`scopeClient` / `scopeDevice`，配 `requireReadTokens` / `requireDeviceTokens` 两个开关）：
  读取默认公开（thin 安装器首启不能先有令牌），上报默认按 `clientTokens` 校验；`install.sh` 新建
  配置时生成客户端令牌并写 `requireDeviceTokens: true`、`requireReadTokens: false`。桌面端
  `clientToken`（环境变量 `SYNTROPY_UPDATE_CLIENT_TOKEN` 优先）在计划 / 忽略 / 结果 / 清单请求上
  以 Bearer 带上，thin 安装器可把令牌烘进 `bootstrap.json`（`--update-client-token`）。

已验证：桌面自更新清单能被**真实桌面 schema**（zod `.strict()` + 平台枚举）解析、
Ed25519 签名可校验、URL 落在白名单；组件清单与 BOM 的签名在同仓库测试与 CLI 冒烟中校验通过。
真实环境（`app.syntropy-lab.com` 那台腾讯云 CVM）也已验证：`deploy/install.sh` 幂等装好
systemd 服务（明文管理面 `127.0.0.1:8443`、公网 HTTPS `:443`），管理页 11 个页面全部渲染，
组件登记 / 内容发布 / 制品上传（`any/any`）经页面走通并落库；`tests/ui/dom-smoke.mjs` 直接打
`https://app.syntropy-lab.com`（不再需要 SSH 隧道）读/写全绿；
服务器公钥验过 `/v1/distributions/latest` 的 BOM 与 `/v1/components/{c}/manifest`，
`pkg/updclient` 完成制品下载 + sha256 校验。**443 已让给本服务**：证书由内置 ACME 于
2026-10-05 首次签发（`CN=app.syntropy-lab.com`，Let's Encrypt，TLS-ALPN-01，缓存
`/var/lib/syntropy-update/data/acme`）；原来的 xray REALITY 从 443 挪到 `4430`
（`/usr/local/etc/xray/config.json`，备份 `/root/xray-config.json.bak-*`）。**腾讯云安全组
仍需放行 4430/tcp，否则 xray 客户端连不上**（本机 `~/xray-client.txt` 已同步改为 4430）。

### 17.2 桌面端集成（`../Syntropy/desktop_app`）

已落地，`tsc -b`、`oxlint`、`depcruise`、`oxfmt --check`、`vitest run --dir tests/security`（22 文件 / 163 用例）、
`vitest run --dir tests/component`（120 文件 / 1133 用例）全绿：

- 自更新通道：`update-service.ts`（平台枚举扩到 `darwin|linux|win32`，受控的本地开发模式）；
  `update-config.ts`（环境变量或 `<userData>/update.json` → 清单地址/渠道/公钥 → `allowedHost`）；
  `canonical-json.ts`（规范化 JSON，与 Go 端 `internal/sign` 同一份实现）；
  `main.ts` 的 `update.check` 接真实签名检查。
- 组件升级通道（本次新增）：
  - `cluster-update.ts`：拉取并验签渠道 BOM / 单组件清单，按 (os,arch) 挑制品，
    流式下载 + sha256/大小校验 + 原子改名；
  - `upgrade-plan.ts`：`POST /v1/plan` 提交库存（桌面端自己 + 本机节点）并严格解析返回的计划；
    下载与安装只认 `targetId=desktop` 的计划项（`isDesktopPlanItem`）——节点目标的项由重下发负责；
  - `desktop-inventory.ts`：读 `release-manifest.json` + 本机 Master 对账结果，产出
    `desktop` / `codex` / `mbot` 组件库存；本机 Master 与安装包不一致时不伪造版本号
    （不上报 mbot，让计划按「需要重新部署」处理）；
  - `node-inventory.ts`（本次新增）：向 App Server 取本机 Master 登记的 `deployMode=local`
    工作节点，逐个对账 `~/.syntropy-run-<节点 id>` 里的 `nbot` 与「要装的那一份」是否同一份字节，
    产出 `kind=node` 的库存目标（一致才写 `components.nbot`，否则只在
    `labels.binaryState` 记 `stale/missing/unknown`）。这些目标随桌面端库存一起提交，
    库存页因此看得见本机节点；远端节点不在本机，不参与对账；
  - `updates-view.ts`：把「本机库存 + 渠道 BOM + 服务端计划」合成设置页只读快照。
  - `staging.ts` + `runStageUpdates`：把计划里 `pending` 的组件制品下载到
    `<userData>/updates/staging/<component>/<version>/<filename>`——边下边校验 sha256、
    原子改名、幂等复用（已存在且摘要一致直接命中，`reused: true`）；组件名/版本/文件名
    落盘前一律按白名单校验，杜绝路径穿越。
  - `update-sweep.ts`：原则 3 的定时巡检（默认 12h 一轮 / 首轮延迟 5min，可关），
    每轮走一遍「库存 → 计划 → 自动下载」，失败只记日志，不打断主流程。
- IPC：`DesktopBridge.command` 从 `update.check` 扩到 `check | plan | stage | install | rollback`；
  `install` 带 `confirm`（缺省只预演，返回 `needs-confirmation`），`rollback` 带 `backup`/`component`，
  与 `check` / `plan` / `stage` 共用 `executeUpdateCommand`。
- 设置页：新增数据驱动页 slug `updates`（「设置 → 常规 → 升级与组件」）——只读策略行
  （自动 / 安装前确认 / 仅手动，取最严口径）、桌面端自更新状态、本机集群对账状态、
  各组件「已装 → 可用」与服务端计划状态、「重新检查」「下载更新」（`update.stage`）、
  「安装已下载的更新 / 确认安装 / 回滚」，以及下载结果（已下载 / 复用 / 失败逐条）。
- 安装执行器（本次新增）：`install-executor.ts` 的 `runInstallUpdates` 默认只**预演**
  （`confirm: false` → `needs-confirmation`），确认后才换件；`runRollback` 回到指定备份。
  本机二进制（`LOCAL_BINARY_COMPONENTS = {mbot}`）经 `main.ts` 的
  `localBinarySwapper` 走本机 Master 的 `POST /api/master/registry/{id}/binary`，
  换件前先停进程、失败拉起旧件。
- 桌面端自更新（本次新增）：计划里的 `desktop` 项由 `main.ts` 的 `desktopInstallerRunner`
  `shell.openPath` 交给平台安装器，随后退出应用（缓冲 1.5s 让结果先回显）；安装器后缀
  按平台白名单校验，不符当场失败而不会把 `.dmg` 丢给 Windows。桌面项在预演与执行时都被
  排到计划最后——它一交出去进程随时退出，后面就没机会跑了。
- 节点制品（本次新增）：`component-layout.ts` 把组件名翻成运行时约定的文件名
  （与主仓库 `common/mdeploy` 的 `*ArtifactName` 一一对应），`component-store.ts` 负责
  落盘——现算 sha256 比对声明、文件名必须等于约定名、旧件先备份进
  `<userData>/updates/backups/<组件>/<时间戳>/`（含 `backup.json`）、新件写临时文件再
  原子改名。覆盖 `nbot` / `node-repl` / `node-runtime` / `codex` / `browser-runtime`，
  本机 Master 下次部署节点时就取到新版。`main.ts` 用一个分派臂把「本机 Master 换件」
  与「节点制品落盘」合成同一条 `handles()` 路线，安装与回滚不会走岔。
  `app-server` / `master` 可执行文件与内容制品仍返回 `unsupported`。
- 测试：`tests/security/update-staging.test.ts` 覆盖暂存路径白名单、幂等复用、
  摘要不符不留残件、`runStageUpdates` 只下 `pending`、单组件失败不影响整体。
  `tests/security/update-sweep.test.ts` 覆盖巡检排期、不重叠、可关闭与异常吞掉。
  `tests/security/update-install.test.ts` 覆盖「预演不换件、确认后才换」、非白名单组件
  `unsupported`、回滚与失败上报、桌面端交接与排序、安装器平台校验。
  `tests/security/update-component-store.test.ts` 覆盖命名契约、落盘后回滚、首次安装回滚
  即删除、摘要不符 / 文件名不符 / 越界回滚点一律拒绝；另有一条把执行器与落盘串起来的
  集成用例（nbot 计划 → 落 `<组件根>/artifacts/nbot-linux-amd64` → 回滚点可查）。
- 跨语言与端到端验证：`tests/fixtures/updates/` 固化了 **Go 端签发的 BOM 信封**，桌面端
  验签测试直接消费它（两侧规范化 JSON 任何字节漂移都会断）；另用真实 `syntropy-update serve`
  做过一次线上往返：拉 BOM → 验签 → 下载 blob 校验 sha256 → `POST /v1/plan` 得到
  `mbot:upgrade:pending`。

组件根（§15.5）已落地：制品落 `<userData>/components/artifacts/`，本机 App Server 以
`--node-artifacts-dir` 把这份目录排在随包目录之前（`web_backend` 的
`NewWithArtifactSearch`），Master 侧按 `nodeArtifacts` 搜索列表取件
（`syntropy_master/core`）。`app-server` 的落点 `<组件根>/bin/<平台>-<架构>/` 也已约定。

「触发节点重部署」的通道也打通了：proto 新增 `RedeployNode`
（`POST /api/node/{node_id}:redeploy`，App Server 侧 `POST /api/node/redeploy`），Master 用制品目录里
**当前那一份**重新下发既有工作节点——节点 id、注册记录与运行目录都不变，只换二进制与随节点运行时。
凭据不落库：远端重下发由 App Server 用自己保存的连接配置补全，已建过反向隧道的节点则复用隧道记录
里那套凭据（口径与删除节点一致，绝不把请求里的地址和持久化密钥混用）。重下发是幂等的，**失败不删
记录、不清运行目录**——节点 id 必须留住，否则一个仍在心跳的实例会变成幽灵节点；恢复手段是重试，
要退回上一版则由桌面把上一版制品放回制品目录后再调一次。重下发期间节点被标记为不可调度（与删除
共用同一把「忙碌」锁），本机节点的自愈看门狗也会跳过它，免得跟部署抢同一个安装目录。

桌面端的重下发触发面也接上了（本次新增），两条路都只发起、不直连节点：

- 手动：「节点管理」面板每个工作节点行内与详情页各有「重新下发」按钮，走
  `POST /api/node/redeploy`（本机 Master + 该节点 id），复用既有的轮询 / 断线恢复 / 收尾流程，
  与「部署智能体」同一套进度回报；主节点自身不给这个按钮。
- 自动：`install-executor.ts` 里安装成功后，只要这次装成过任何一个被认领的节点制品
  （`component-layout.ts` 的 `isNodeArtifact`：nbot / node-repl / node-runtime / codex /
  browser-runtime），就用当前制品对**在线**工作节点重下发一次；一次重下发会把制品目录里
  全部节点件一起带上，所以整条安装只重下发一次、不按组件重复。重下发失败不回滚已经装好的
  制品（制品本身是对的、只是还没生效），失败原因写进结果说明；掉线节点跳过，交给手动重试。
- 测试：`tests/security/update-install.test.ts` 新增 5 例——节点制品装成功后调用一次、
  只装非节点制品（mbot）不调用、预演阶段提示「随后用新制品重下发工作节点」、重下发部分失败
  与整体抛错都不影响安装结果（进 message）。

thin 安装器（构建期烘死地址与公钥、只带外壳的资源树）也已落地（见 §6.2）。

「忽略此版本」（本次新增）：「设置 → 升级与组件」每个组件行按状态给出「忽略此版本」或
「不再忽略」按钮，走 `update.ignore` 命令（`update-ignore.ts` 写回服务端目标级记录，写完重新
问一次计划，界面只显示服务端刚算出来的那一份）；契约里 `UpdatesComponent` 带上 `targetId` 与
服务端给的 `ignored` 标记，界面不靠文案猜。测试 `tests/security/update-ignore.test.ts` 覆盖
POST 路径与请求体、DELETE 撤销、目标 id / 组件名的 URL 编码、白名单主机在发请求前就拒绝、
服务端拒绝要如实抛错、响应不是忽略记录时拒绝。

三块收尾工作（mbot 双落点、app-server 换件、内容制品通道）都已落地，见 §17.3。

本机集群对账（`local-cluster-version.ts`）也跟着走了：判断「运行目录里那份 == 要装的
那一份」时，先看组件根（`component-artifact-directory`），那里没有才回落到安装包。
否则升级完 mbot 之后平台会一直报 `stale`，计划每次都重提同一个升级。

> 注意：主仓库工作区仍有并行的未提交改动（drawio / tool-integrations），本次的 `updates`
> 设置页是追加在该工作区之上的（`SettingsPage.tsx` / `settings-registry.ts` / `contracts.ts`），
> 与那些改动不冲突；提交时需要一并带上。

### 17.3 组件落地（本轮实现）

三块收尾工作都已落地，`gofmt` / `go vet` / `go test ./...` 与桌面端 `tsc` / `oxlint` / `depcruise` / `vitest` 全绿。

1. **mbot（本机 Master 二进制）——双落点已落地。**
   桌面 `localBinarySwapper` → `POST /api/master/registry/{id}/binary?sha256=`；主仓库
   `web_backend/service/master/master_component.go` 的 `replaceLocalBinary` 做「停 → 备份到
   `<runDir>/backups/<时间戳>/` → 原子替换 → 拉起」，换件失败把旧进程拉回、重启失败先回滚
   二进制再拉起；回滚走 `?restore=<backups 下的路径>`（`resolveRestorePoint` 把回滚点限制在
   该运行目录的 `backups` 内）。桌面端的执行臂改成「全部命中、顺序执行、逆向回滚」的合成臂
   （`updates/component-swapper.ts`），所以 mbot 一次安装做两件事：先把同一份制品落到
   `<组件根>/artifacts/mbot-<os>-<arch>[.exe]`（远端主节点部署从这里取件），再换掉正在跑的
   那一份；中途失败会把已经装好的那条臂一起退掉，不留半态。
2. **app-server（本机 App Server 二进制）——已落地。**
   新增执行臂 `updates/app-server-swapper.ts`：停掉全部实例 → 备份 → 原子替换
   `<组件根>/bin/<platform>-<arch>/syntropy-app-server[.exe]` → `ready()` 重新拉起；起不来
   就还原备份再拉起。首次升级（组件根里还没有这份文件）的回滚是删掉它并把空的 `bin/` 清掉，
   让运行期回落到随包那一份——留着空目录会让解析停在组件根又找不到可执行文件。库存侧也补上
   了：桌面问本机 App Server 的 `/api/health` 拿它**正在跑**的版本
   （`desktopInventoryFromReport` 上报 `app-server`），否则计划每次都会重提同一次安装。
3. **内容制品（templates / skills）——已落地。**
   制品形态是与 (os,arch) 无关的 `.tar.gz` 目录归档，文件名 `<组件>-<版本>.tar.gz`；归档的
   顶层就是组件目录的内容（打包用 `tar -C <目录> .`），解包后**整目录替换**两个落点：
   - `<userData>/data/master-run/master-data/<组件>`：本机 Master 正在读的数据目录
     （`syntropy_master` 的 `skillsDir` / `templatesDir`），Master 每次调用读盘，换完即生效；
   - `<组件根>/data/<组件>`：部署源树，本地 re-deploy 与远端节点 payload 都从这里取件。
   两处各自留回滚点（`<userData>/updates/backups/<组件>/<时间戳>/`，内含 `content-backup.json`
   记录 `existed[]`）：先把旧目录整棵移到回滚点，再把新目录原子换名顶上；第二个落点失败会把
   第一个也退回去，不留「换了一半」的半态。回滚按 marker 反序还原，退不干净时保留回滚点并在
   错误信息里给出路径（那是上一版内容的唯一副本）。版本标记落在
   `<userData>/updates/content/<组件>.json`——**不能**放进内容目录本身，那会被主节点播种、
   也可能整棵复制到远端主节点；库存读它上报版本，没有标记就按「还没装」处理（不替用户猜）。
   执行臂是 `updates/content-swapper.ts`，归档解析与安全校验在 `updates/content-archive.ts`
   （自写 tar 解析，不引新依赖：只认普通文件与目录，拒 symlink / 硬链接 / 设备 / FIFO、绝对
   路径与盘符、`..`、超限条目数与体积、截断的归档与校验和不符的头；落盘前再查一次越界）。
   `tar -C <目录> .` 写出的 `./` 归档根条目会被剥掉（`./nested/` 归一成 `nested`），不当成
   「空条目名」拒收——那正是文档给发布方的打包命令，bsdtar/GNU tar 都会写它，收到就拒会把
   正常制品整个挡在门外；只有**普通文件**的名字归一化后为空才仍然拒收。
   服务端侧见 §17.1「内容制品」一条；thin 安装器的首启引导也认内容组件（按平台无关回落取件、
   按 `<组件>-<版本>.tar.gz` 校验文件名、解包到 `<组件根>/data/<组件>`）。`plugins` 走 Master
   侧 marketplace（git 镜像），不在目录归档范围。
   测试：`tests/security/update-content-archive.test.ts`（正常解析 / 各类拒收 / 落盘越界）、
   `update-content-swapper.test.ts`（双落点替换、首次安装回滚即删除、只存在一处时的回滚、
   坏归档与错误摘要不污染现有内容、回滚点损坏）、`update-content-version.test.ts`（标记读写与
   版本保护）、`update-bootstrap.test.ts`（thin 首启装内容组件、文件名不符即失败）、
   `upgrade-client.test.ts`（平台无关选件回落与 Schema 校验，含 Go 签发的含内容组件 BOM）。
   **端到端冒烟（已实跑）**：`publish -kind content -content <tgz>` → `distribution publish` →
   `serve` → 桌面端 `runStageUpdates` 拉签名 BOM、验签、下载并按 `<组件>-<版本>.tar.gz` 落进
   暂存区 → `createContentSwapper().replace()` 同时换掉两个落点并写版本标记 → `rollback()`
   还原。跨语言那段另固化为 `tests/fixtures/updates/cluster-bom-content-go-signed.json`
   与 `cluster-bom-content-pubkey.pem`（Go 端 `distribution show` 的真实输出），
   `upgrade-client.test.ts` 直接对它验签并挑件，两侧口径漂移会立刻断掉。

### 17.4 组件模型收敛与远端灌库（2026-10-09）

- **组件模型**：`internal/model` 一处定义 5 个受管组件、`KindProduct` 与 5 个产品槽位
  （`ProductSlots`）；`internal/bundle` 的 `componentDefs` 随之收敛为 5 行，`PlanAssets` 校验
  「槽位是否属于这个组件」「制品名是否等于客户端认的那一个」。
- **制品键**：`artifactKey(releaseID, slot, os, arch)`；管理面路由改为
  `artifacts/{slot}/{os}/{arch}`；签名清单的制品条目加 `slot`，`/v1/manifest`（桌面自更新）
  只挑产品里 `desktop` 槽位那一件，`GET /v1/components/{component}/manifest` 对
  `component == "" || "desktop"` 归一成 `product`，旧调用不破。
- **远端灌库**：`import-bundle -remote <url>` 走新增的 `internal/adminclient`（实现
  `bundle.Service` 的管理面 HTTP 客户端），于是「本地写数据目录」与「远端调 API」共用同一段
  `bundle.Import`。凭证优先 `-token`，否则取配置里 `adminTokens` 的第一项。
- **回归**：`internal/adminclient/adminclient_test.go` 用真实 `api.Server`（httptest）跑
  「构建清单 → `PlanAssets` → `Import` → 落库 → 验签 BOM」，并验证第二次重跑只跳过、
  错误码映射回 `ErrValidation` / `ErrNotFound`。**实测到一次真实缺陷**：`http.Transport`
  发送完会关闭请求体，而 `bundle.Import` 之后还要自己关那个文件（本地后端不关），
  于是远端路径会冒 `file already closed`；客户端因此用「只读不关」的壳包一层请求体。

### 17.5 内容组件进渠道与 BOM 锚点（2026-10-09）

- **BOM 锚点**：`internal/release/distribution.go` 的 `PublishDistribution` 允许同一
  `(channel, productVersion)` 重新发布，条件只有一个——BOM 里的 `product` 条目是同一份发布
  （`sameProductRelease`）。产品换了仍然报 `ErrValidation`（请升产品版本），插件换了直接推进：
  保留 `CreatedAtMS`、刷新 `PublishedAtMS`，审计记 `distribution.update`。回归
  `TestPublishDistributionAdvancesPluginsOnly`；`internal/bundle` 的
  `TestImportSurfacesDistributionConflict` 只验证把服务端的拒绝透出来——规则不在灌库层复制一份。
- **内容制品打包**：主仓库新增 `desktop_app/tools/content-archive.mjs`（自己写 ustar 头、固定
  mtime/uid/gid/模式、条目按名字排序、gzip 头不带时间戳，于是同一份内容永远打出同样的字节），
  `update-bundle.mjs` 按 `tools/content-release.json` 把仓库根 `data/<组件>` 打成一个
  `<组件>-<内容版本>.tar.gz` 放进发布包。计较字节是必须的：服务端按 sha256 判定「同一版本不可
  换字节」，而 CI 每次都是全新 checkout。
- **生产实测（2026-10-09）**：`templates@1.0.0`（541 条目 / 197,211 字节）与 `skills@1.0.0`
  （737 条目 / 1,419,696 字节）灌库后，`stable/0.4.3` 的 BOM 从 3 个组件推进到 5 个；重跑整份
  发布包全部 skipped；从线上 blob 取回归档，客户端解析器解出的目录树与仓库 `data/<组件>`
  逐字节一致。

见 `docs/releases/v0.1.1.md`。
