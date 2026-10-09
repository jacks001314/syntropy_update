# 变更日志（syntropy_update）

本仓库是主仓库 `../Syntropy` 的配套工程：Syntropy 集群的组件分发与升级服务。
受管组件的口径、版本规则与发布方式只有一个权威说明，见主仓库
`docs/system-upgrade-method.md`；本服务的总体设计见 `docs/design.md`。

## 版本号规则

- 对外版本号形如 `vX.Y.Z`，跟 Git 标签一一对应。
- 接口（HTTP API、签名清单、发布包格式）或数据格式的**破坏性变更**进 minor；
  其余进 patch。
- 每个版本一份说明：`docs/releases/vX.Y.Z.md`。发版时的动作只有三步——
  写说明、在下面的表里加一行、打 tag。

## 发布记录

| 版本 | 日期 | 说明 |
| --- | --- | --- |
| [v0.1.0](docs/releases/v0.1.0.md) | 2026-10-09 | 首个版本：受管组件收敛为 5 行，新增远端灌库链路与槽位寻址 |
