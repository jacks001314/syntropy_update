# Syntropy 升级服务（syntropy_update）

Syntropy 集群的组件分发与升级管理服务。规划中的生产域名为
`https://app.syntropy-lab.com`。

这一目录是主仓库 `../Syntropy` 的**配套独立工程**，不参与主仓库 `go.work`。

## 职责

- 组件制品仓库：内容寻址 blob 存储，sha256 校验，支持 Range 下载。
- 组件与版本管理：`mbot` / `nbot` / `app-server` / `codex` / `node-repl` /
  `node-runtime` / `browser-runtime` / `desktop` / 内容制品。
- 渠道与分发定义（BOM）：`alpha` / `beta` / `stable`。
- 签名清单：Ed25519 签名的自更新清单与 BOM，供桌面端与安装器消费。
- 策略与计划：灰度、维护窗口、审批口径、熔断、回滚；按库存计算升级计划。
- 车队与审计：节点/桌面端库存、升级结果、管理动作留痕。

升级只由桌面应用实施；本服务只提供事实与策略，不执行升级。

## 文档

- [docs/design.md](docs/design.md)：总体设计（权威）。

## 部署（systemd）

生产域名 `https://app.syntropy-lab.com`。部署是「一份幂等脚本 + 一个 systemd 单元」：

```bash
# 本地交叉编译（纯 Go，CGO_ENABLED=0）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" \
  -o deploy/syntropy-update ./cmd/syntropy-update

# 拷到服务器后（sudo）
sudo ./deploy/install.sh --binary ./syntropy-update --addr 127.0.0.1:8443 \
  --tls-email ops@example.com
```

脚本建 `syntropy-update` 系统用户与目录（二进制 `/opt/syntropy-update`、配置与签名私钥
`/etc/syntropy-update`、数据 `/var/lib/syntropy-update`、日志 `/var/log/syntropy-update`），
首次部署自动生成管理令牌与签名私钥并打印管理页地址；重复执行只替换二进制并重启服务。

监听器有两个：明文管理面固定 `127.0.0.1:8443`（令牌门禁，不建议改），公网 HTTPS 默认
`:443`——`tlsDomains` 默认取 `publicBaseURL` 的域名，传 `--no-tls` 可关掉。证书由内置
ACME 客户端自动签发与续期，缓存 `/var/lib/syntropy-update/data/acme`（systemd 开着
`PrivateTmp`，所以缓存必须落在这个持久目录，否则重启会重复签发并撞限流）；80 端口通常
进不来，签发走 TLS-ALPN-01，**只要 443 能被外网访问就能签发**，不需要放行 80。绑定 443
靠单元里的 `CAP_NET_BIND_SERVICE`，进程仍是 `syntropy-update` 非 root。

- 管理页：`http://127.0.0.1:8443/admin/`（对外 `https://app.syntropy-lab.com/admin/`）；
  打开后把令牌粘到右上角「管理令牌」→「使用」。
- 没配 `adminTokens` 时 `/v1/admin/*` 一律 401，管理页会直接说明原因并给出修复步骤。
- 日志：`journalctl -u syntropy-update -n 50 --no-pager`；重启：`systemctl restart syntropy-update`。
- 443 归属：客户端 host 白名单不带端口，所以生产 443 必须让给本服务。同机原来占用 443 的
  xray REALITY 要挪到别的端口（本机现为 `4430`），并在腾讯云安全组放行新端口、同步更新
  客户端配置；xray 监听非 443 端口时会提示「可能被 GFW 封 IP」，属已知代价。

## 开发

```powershell
Set-Location D:\qax\reagent\dev\git\syntropy_update
go build ./...
go vet ./...
go test ./...

# 生成签名密钥
go run ./cmd/syntropy-update keygen -config conf/update.json

# 发布一次桌面版本（上传各平台安装器）
go run ./cmd/syntropy-update publish -config conf/update.json `
  -component desktop -version 0.4.0 -channel beta `
  -artifact "windows/amd64=dist/SyntropySetup.exe" `
  -artifact "darwin/arm64=dist/Syntropy-arm64.dmg"

# 发布集群组件与分发定义（BOM）
go run ./cmd/syntropy-update publish -config conf/update.json `
  -component mbot -kind binary -version 1.2.0 -channel stable `
  -artifact "linux/amd64=dist/mbot-linux-amd64"
go run ./cmd/syntropy-update distribution publish -config conf/update.json `
  -channel stable -product-version 1.2.0 -component mbot=1.2.0 -component codex=0.160.0
go run ./cmd/syntropy-update distribution show -config conf/update.json -channel stable

# 发布内容制品（templates / skills）：平台无关的 .tar.gz，顶层就是组件目录的内容
tar -czf templates-1.2.0.tar.gz -C path/to/templates .
go run ./cmd/syntropy-update publish -config conf/update.json `
  -component templates -kind content -version 1.2.0 -channel stable `
  -content templates-1.2.0.tar.gz
go run ./cmd/syntropy-update distribution publish -config conf/update.json `
  -channel stable -product-version 1.2.0 -component templates=1.2.0

# CI 一次灌库：读桌面构建的 release-manifest.json，内容制品随行（-component-version 覆盖版本）
go run ./cmd/syntropy-update import-bundle -config conf/update.json `
  -manifest dist/release-manifest.json -channel stable `
  -desktop windows/amd64=dist/SyntropySetup.exe `
  -content templates=templates-1.2.0.tar.gz -component-version templates=1.2.0

# 设置分发策略、上报库存、计算升级计划
go run ./cmd/syntropy-update policy set -config conf/update.json `
  -component mbot -channel stable -approval confirm -percentage 20 -window 18:00-22:00
go run ./cmd/syntropy-update policy show -config conf/update.json
go run ./cmd/syntropy-update inventory report -config conf/update.json `
  -target node-a -kind node -component mbot=1.1.0
go run ./cmd/syntropy-update inventory list -config conf/update.json
go run ./cmd/syntropy-update results report -config conf/update.json `
  -target node-a -component mbot -state installed -from 1.1.0 -to 1.2.0
'{"channel":"stable","targets":[{"id":"node-a","kind":"node","components":{"mbot":"1.1.0"}}]}' |
  go run ./cmd/syntropy-update plan -config conf/update.json -inventory -

# 打印签名清单 / 公钥 / 列表 / 审计
go run ./cmd/syntropy-update manifest -config conf/update.json -channel beta
go run ./cmd/syntropy-update manifest -config conf/update.json -component mbot -channel stable
go run ./cmd/syntropy-update pubkey   -config conf/update.json
go run ./cmd/syntropy-update list     -config conf/update.json
go run ./cmd/syntropy-update results list -config conf/update.json -limit 20
go run ./cmd/syntropy-update audit    -config conf/update.json -limit 50

# 启动服务
go run ./cmd/syntropy-update serve -config conf/update.json

# 管理页端到端：jsdom 真跑 app.js（可打本地或线上服务；--write 会真的写数据）
mkdir -p .work/uiharness && (cd .work/uiharness && npm install jsdom)
node tests/ui/dom-smoke.mjs --base http://127.0.0.1:8443 --token <admin-token> [--write]
```

## 状态

- 服务端：组件/发布/制品（含平台无关的内容制品）/渠道/BOM、签名清单（桌面自更新 + 集群组件 + 分发定义）、
  升级结果回收（`POST /v1/targets/{id}/results` 目标端上报、`GET /v1/admin/results` 管理面只读、
  CLI `results <list|report>`）、熔断执行（计划引擎按 `killSwitch` / `minimumSupportedVersion`
  停发或强制升级，thin 首启引导逐组件核对）、客户端鉴权分档（`scopeClient` 默认公开、
  `requireReadTokens` 可收紧；`scopeDevice` 按 `clientTokens` + `requireDeviceTokens` 校验，
  `install.sh` 新建配置即注入客户端令牌）、
  策略 / 计划引擎（灰度、维护窗口、审批口径、目标匹配）、库存与计划 HTTP 接口及 CLI
  已落地；运维管理页内嵌在 `GET /admin/`（原生单页，无前端构建链），11 个页面
  （概览 / 组件 / 发布 / 分发定义 / 库存 / 忽略集合 / 升级计划 / 升级结果 / 分发策略 / 审计 / 说明）都带
  过滤与空态，未登录时按 `/healthz` 的 `adminEnabled` 区分「没配令牌」与「令牌错」并给出修复步骤。
  页面引用的接口与服务端 `routes()` 由结构测试双向比对；`tests/ui/dom-smoke.mjs` 用 jsdom
  把页面真跑一遍（含内容制品上传与注入转义），可打本地或线上服务。`deploy/` 提供幂等的
  systemd 安装脚本。`gofmt` / `go vet` / `go test ./...` 全绿。
- 桌面端：`update.check` 接入真实签名更新服务；组件升级通道已打通——验签渠道 BOM、
  按平台取制品并校验下载、采集本机库存、`POST /v1/plan` 取回升级计划，并落到
  设置页 `设置 → 常规 → 升级与组件`（只读策略行 + 组件状态 + 计划状态 + 下载更新）。
  自动下载已落地：`update.stage` 把计划里 `pending` 的组件制品下到
  `<userData>/updates/staging/<component>/<version>/<filename>`，边下边校验 sha256、
  原子改名、幂等复用，安装前仍需人工确认。
  原则 3 的巡检也落地了：进程内定时（默认 12h / 首轮延迟 5min）跑「库存 → 计划 → 自动下载」，
  `SYNTROPY_UPDATE_SWEEP_INTERVAL_MS=0` 可关闭定时（设置页仍可手动）。
  安装与回滚已落地（本机二进制）：`update.install` 默认只预演，`confirm: true` 才经本机
  Master 的 `POST /api/master/registry/{id}/binary` 停-备份-换-起，`update.rollback` 可回到
  指定备份；桌面端自身走 `shell.openPath` 交给平台安装器（NSIS/dmg/deb）后退出并由安装器
  重新拉起；节点制品（`nbot` / `node-repl` / `node-runtime` / `codex` / `browser-runtime`）
  按 `mdeploy` 的命名落到组件根 `<userData>/components/artifacts/` 并留回滚点，本机
  App Server 以 `--node-artifacts-dir` 把它排在随包目录之前，Master 因此按「组件根在前、
  随包目录兜底」取件（三平台一致，macOS 不再受 .app 签名限制）。内容制品（`templates` /
  `skills`）也走同一条通道：下载的是平台无关的 `.tar.gz`（`<组件>-<版本>.tar.gz`，顶层即
  组件目录内容），解包后同时替换本机 Master 数据目录与部署源树 `<组件根>/data/<组件>`，
  两处各自留回滚点；装过哪一版写在 `<userData>/updates/content/`，库存据此上报，默认审批
  `auto`。thin 安装器的首启引导同样会装内容组件。`plugins` 走 Master 侧 marketplace，不在
  本服务范围内；其余组件返回 `unsupported`。
  `tsc -b` / `oxlint` / `oxfmt --check` / `tests/security` / `tests/component` 全绿；
  `tests/fixtures/updates/` 固化了 Go 端签发的 BOM，跨语言验签有回归测试。
- 详见 [docs/design.md](docs/design.md) 第 17 节。


