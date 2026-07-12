# 5GPN-Go

[English](README.md) · [中文](README.zh-CN.md)

实验性的个人 DNS 与网关控制面。单个 daemon 二进制 + 内嵌 React + Tailwind + Catalyst UI
面板。前身是 [5GPN-X](https://github.com/Xiuyixx/5GPN-X)（Go + Python
+ 3372 行 install.sh 三合一），这一版重构成一个干净的守护进程 + 一个面板。

不是 SaaS。单用户、单机、单 daemon 进程。目标用户是"自己在 VPS 上跑个网关，
希望这套东西自己看得懂、改得动"的人。

## 能做什么

- **规则可视化路由**：DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD /
  GEOSITE / GEOIP / IP-CIDR / RULE-SET / MATCH，每条规则可设优先级、
  出口动作、启用开关，由面板编辑。TG Bot 提供较小的文本运维命令面，
  已接入的动作复用同一套 store/Applier。
- **规则校验 + 观测式 Apply**：Dry-run 校验规则模型，并用进程内 matcher
  运行用户提供的 fixture；它不会执行 mihomo 或 dnsdist config-check。
  systemd orchestrator 会在重载后观察进程状态，失败时尝试恢复之前
  渲染的文件。默认探针只检查 dnsdist 与 mihomo 是否 active，不检查
  sniproxy、DNS 回答或公网出口 IP。Apply 状态持久化为 `submitted`、
  `confirmed`、`rolled_back` 或 `failed`；`failed` 表示无法确认完整回滚。
- **规则版本快照**：每个不同规则 hash 写一条 `snapshots`，相同重试复用；
  每次 apply 写 `rule_versions` 记录。快照是与规则 YAML 配对的数据库记录，不是每次
  apply 生成的 tarball；回滚会重新应用对应规则版本。
- **面板**：React 19 + Tailwind v4 + Catalyst UI Kit。包含 setup、login、
  首次 wizard、Dashboard、Rules、Exits、Snapshots、Backup、Logs（SSE）
  和 Settings。
- **TG Bot**：为部分运维动作提供文本命令，chat_id 白名单强制启用（白名单
  为空 = daemon 拒绝启动 Bot）。
- **iOS 加密 DNS 描述文件**：面板提供未签名的 XML `.mobileconfig`，
  主 payload 使用 DoH、fallback payload 使用 DoT，其 URL 可生成二维码。
  启用前的 preflight 只探测 daemon 的 loopback DoT listener，不验证
  描述文件实际使用的公网 DoH 链路。
- **WA-shim 库**：旧版 parser/relay 逻辑已有 Go 移植，但 v0.4.0 的 daemon
  与 installer 都不会启动它。
- **发布版本检查**：面板可以查询最新 release。进程内 update apply 已
  禁用，安装更新必须由外部 installer 或未来能跨 daemon 重启存活的
  privileged supervisor 执行。
- **备份导出与规则导入**：导出得到明文 tar.gz，包含活动规则、snapshot
  元数据、通过 `VACUUM INTO` 生成的 WAL 安全 SQLite 热复制及 README。
  面板导入只恢复 `rules/active.yaml`；数据库与 manifest 仅供管理员离线
  恢复参考。
- **带 `--dry-run` 的安装器**：`5gpn-installer install / upgrade /
  uninstall / status / doctor / migrate-from-legacy`，加上
  `--os-fixture` 可以在不起 VM 的前提下预演在 Ubuntu 22.04/24.04、
  Debian 12/13、CentOS 9、AlmaLinux 9、Rocky 9、RHEL 9、Fedora 40
  上的行为。

## 当前生产限制

- 标准安装单元以非特权 `5gpn` 用户运行，并启用
  `NoNewPrivileges=yes`、`ProtectSystem=strict`。运行时 apply、daemon
  重启和 MTG 控制仍需写系统路径或调用 `systemctl`，但项目尚未提供
  privileged helper、polkit policy 或等价的窄权限提升路径。因此这些
  操作在已安装单元下尚不能用于生产，会返回权限错误。
- 面板 JWT 使用 HS256，并通过 SQLite session 支持撤销。浏览器目前把
  bearer token 持久化在 `localStorage`；一旦同源脚本注入成功，token
  仍可能被读取。
- 备份归档不加密，SQLite 热复制可能包含秘密。必须按敏感文件保管；
  面板导入有意不执行完整数据库恢复。
- 实验性的 SNI/QUIC 转发器从主机直接连接公网目标，不经过所选 mihomo
  出口。因此当前 Path B 不是按 active exit 转发的透明代理。
- internal-only Gate 只覆盖面板与进程内 SNI/QUIC 转发，不限制外部
  `mtg.service`；MTProxy 需要单独配置主机防火墙或 systemd 访问策略。

## 数据面保留外部组件

dnsdist + mihomo + sniproxy 仍然作为外部 systemd 单元运行，
Go daemon 不重写它们。有效状态由基础 YAML 加 SQLite 中的规则、出口和
面板设置组装，再渲染三个组件的配置并执行：

- dnsdist：`systemctl reload dnsdist`（SIGHUP 重载配置）
- mihomo：`systemctl reload mihomo`
- sniproxy：`systemctl restart sniproxy`

这些命令需要尚未落地的特权执行设计。每次 systemd apply 都会执行
sniproxy restart，因此可正常控制的 `sniproxy.service` 是 apply 的必备
条件。当前不承诺 apply 延迟 SLO。

## 目录结构

```
cmd/                Go 入口（5gpn 守护进程、5gpn-installer、5gpn-ctl）
internal/
  api/              chi 路由、JWT 鉴权、服务端 session 撤销、rate limiter
  config/           YAML schema + 三方组件渲染器（dnsdist / mihomo / sniproxy）
  db/               SQLite migration（goose）
  exit/             10 种出口协议解析（SS / VMess / Trojan / VLESS+reality /
                    Hy2 / TUIC / AnyTLS / SS2022 / SOCKS / HTTP）
  installer/        Install / Upgrade / Uninstall / Status / Doctor / Migrate；
                    Env + Executor 抽象让 --dry-run、测试、真实执行走同一路径
  ios/              mobileconfig 渲染 + inetd 风格 ServeConn
  metrics/          /proc 采样 → SQLite 环形表
  orchestrator/     systemd apply 流水线 + 进程状态探针/失败回滚
  proxy/washim.go   wa-shim.py 的 Go 版
  rules/            model / parse / dry-run / hash / import-legacy
  tgbot/            文本命令 Bot + chat_id 白名单
  updater/          release 元数据/下载基础能力；运行时 apply 已禁用
  web/              面板 dist 的 go:embed
web/                React 19 面板（Vite + Tailwind v4 + Catalyst UI）
deploy/             精简的引导 install.sh（下载 + sha256 校验 + exec）
configs/            静态启动配置示例；运行时状态还来自 SQLite
tests/e2e/          真实 daemon 冒烟套件（build-tag `e2e` 门禁）
scripts/            check-file-size.sh、pre-commit
5GPN-X/             旧代码库 —— 只读参考（子树 + 独立 go.mod shim 隔离）
catalyst-ui-kit/    Tailwind Plus Catalyst kit（web/src/components/ui/ 的来源）
docs/               架构 / 安全 / 里程碑 / 技术债 / tgbot-legacy-commands
```

## 构建

```sh
make build          # 守护进程 + 安装器 + ctl（不嵌入面板）
make release        # release 二进制；daemon 嵌入 web dist
make test           # 单测 + race 检测
make web-test       # 前端 Vitest 测试
make coverage       # 各包覆盖率
make lint           # golangci-lint + web lint
make size-check     # 第一方非测试 Go/TS 文件 800 行硬闸
make install-hooks  # 把 scripts/pre-commit 链到 .git/hooks/
```

开发循环：

```sh
# 终端 1
cd web && npm run dev

# 终端 2
go run ./cmd/5gpn --config configs/example.yaml --data /tmp/5gpn --insecure --listen 127.0.0.1:8443
```

冷启动时 daemon 会在 stdout 打印一个一次性 **setup token**，POST 到
`/api/v1/bootstrap` 加上 username + password 就能领取面板。

## 安装

主机引导脚本会下载 release 二进制并交给 Go installer：

```sh
curl -fsSL https://raw.githubusercontent.com/Xiuyixx/5GPN-Go/main/deploy/install.sh | sudo bash
```

脚本从同一个 GitHub Release 下载 `SHA256SUMS`、
`5gpn-installer-<os>-<arch>` 和 daemon，用 checksum 文件校验两个
二进制后执行 `install`。这能发现下载损坏或 release 内部不一致，但
不是发布者签名，也不是独立的真实性证明。`cmd/5gpn-installer/` 是 CLI
入口；用户、目录、config、systemd unit 与 service 操作在
`internal/installer/`。

installer 可以落盘并启动 service，但系统配置写入和 `systemctl` 操作所需
的运行时 privileged helper 仍未解决；把它视为生产就绪前，请先阅读
[docs/tech-debt.md](docs/tech-debt.md)。

从旧的 5GPN-X 环境迁移：

```sh
5gpn-installer migrate-from-legacy --dry-run     # 只预演，不写盘
5gpn-installer migrate-from-legacy               # 写 /etc/5gpn/config.yaml
5gpn-installer migrate-from-legacy --allow-partial # 显式接受遗漏
```

迁移可以从旧的 `/opt/proxy-gateway/` 和 `/etc/proxy-gateway/` 目录生成
域名、DNS、Telegram 与 iOS 设置，且不修改老目录。规则、policy map、
出口以及非 direct 的活动出口目前无法由 config-only migrator 无损表达；
检测到这些状态时默认拒绝迁移，只有显式传入 `--allow-partial` 才会遗漏它们。

## 质量闸（M4）

每次 push 到 `main` 都跑：

- **`ci.yml`**：Go 1.25 在 Ubuntu 22.04/24.04 上执行 tidy、vet、build
  和 `go test -race`；Node 20 把 `npm test`、lint、build 作为硬闸。
  另外还执行源码行数检查、e2e 冒烟、依赖边界检查和 9 路
  installer-fixture dry-run 矩阵。
- **`security.yml`**：`govulncheck` 硬 fail、`npm audit
  --audit-level=high` 硬 fail、`go.mod` 或 `package-lock.json` 有
  改动就 on-push 触发，每周一 03:00 UTC 定时跑一次。

本地：

- `scripts/pre-commit`：pre-commit 阶段对 staged 文件跑 800 LOC 硬闸
  （`make install-hooks` 装钩子）。
- 本地主要闸门为 `make test`、`make web-test` 和 `make lint`。

## 文档

- `docs/architecture.md` — 顶层拓扑
- `docs/security.md` — 威胁模型 + 加固姿态
- `docs/tech-debt.md` — 当前限制与解决条件
- `docs/milestones.md` — 实现状态与发布阻塞项
- `docs/tgbot-legacy-commands.md` — 旧 `tgbot.py` 全量清点 + 新面板
  能力对应表（AC8 追溯）
- 本地 `.omc/` 计划属于被忽略的工具状态，不作为项目文档分发。

## 许可

分发本仓库或其构建产物前，应就全部第一方与第三方材料取得相关权利人
或合格法律顾问的确认。本 README 不对许可证兼容性或再分发权作法律结论。
