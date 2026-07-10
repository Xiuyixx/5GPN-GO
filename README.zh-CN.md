# 5GPN-Go

[English](README.md) · [中文](README.zh-CN.md)

个人透明代理网关。单个 Go 二进制 + 内嵌 React + Tailwind + Catalyst UI
面板。前身是 [5GPN-X](https://github.com/Xiuyixx/5GPN-X)（Go + Python
+ 3372 行 install.sh 三合一），这一版重构成一个干净的守护进程 + 一个面板。

不是 SaaS。单用户、单机、单二进制。目标用户是"自己在 VPS 上跑个网关，
希望这套东西自己看得懂、改得动"的人。

## 能做什么

- **规则可视化路由**：DOMAIN / DOMAIN-SUFFIX / DOMAIN-KEYWORD /
  GEOSITE / GEOIP / IP-CIDR / RULE-SET / MATCH，每条规则可设优先级、
  出口动作、启用开关。面板或 TG Bot 都能编辑。
- **Dry-run + 自动回滚**：每次改规则先跑静态校验（mihomo config-check
  + dnsdist config-check + fixture matcher）。真正 apply 后如果健康
  检查失败，自动回滚到上一个 snapshot。这条是相对 clash-verge /
  mihomo-party 的差异化能力。
- **不可变快照**：每次 apply 都在 `/var/lib/5gpn/snapshots/{id}.tar.gz`
  留档，SQLite 有对应记录。任何历史版本都能一键回滚。
- **面板**：React 19 + Tailwind v4 + Catalyst UI Kit。共 7 屏：
  Login / Dashboard / Rules / Exits / Snapshots / Backup / Logs
  （SSE 实时流）。
- **TG Bot**：文本命令平移面板能力，chat_id 白名单强制启用（白名单
  为空 = daemon 拒绝启动 Bot）。
- **iOS DoT 描述文件**：签名 `.mobileconfig` 直接下载 + 二维码。
- **wa-shim**：1:1 复刻旧 Python 版（保留 WA_PREFIXES、KNOWN 握手样本、
  allow-CIDR、14 个环境变量的默认值）。
- **蓝绿自升级**：`.prev` 备份 + sha256 校验 + 3 秒健康检查失败自动
  回滚。
- **面板级备份 / 恢复**：tar.gz 含规则、snapshot manifest、通过
  `VACUUM INTO` 生成的 WAL 安全 SQLite 热复制。
- **带 `--dry-run` 的安装器**：`5gpn-installer install / upgrade /
  uninstall / status / doctor / migrate-from-legacy`，加上
  `--os-fixture` 可以在不起 VM 的前提下预演在 Ubuntu 22.04/24.04、
  Debian 12/13、CentOS 9、AlmaLinux 9、Rocky 9、RHEL 9、Fedora 40
  上的行为。

## 数据面保留外部组件

dnsdist + mihomo (1.19.28) + sniproxy 仍然作为独立 systemd 单元跑，
��重造轮子。Go daemon 只做 **配置渲染 + 生命周期编排**：单一 YAML
真源渲染出三个组件的配置，热切换动作分别是：

- dnsdist：`systemctl reload dnsdist`（SIGHUP 重载配置）
- mihomo：REST API `PUT /configs?reload=true`
- sniproxy：`systemctl restart sniproxy`（数据面短暂 ≤ 1 秒中断）

总切换窗口 ≤ 1.5 秒。

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
  orchestrator/     systemd apply 流水线 + 健康检查失败回滚
  proxy/washim.go   wa-shim.py 的 Go 版
  rules/            model / parse / dry-run / hash / import-legacy
  tgbot/            文本命令 Bot + chat_id 白名单
  updater/          蓝绿替换 + sha256 校验 + 自动回滚
  web/              面板 dist 的 go:embed
web/                React 19 面板（Vite + Tailwind v4 + Catalyst UI）
deploy/             精简的引导 install.sh（下载 + sha256 校验 + exec）
configs/            example.yaml（schema 权威示例）
tests/e2e/          真实 daemon 冒烟套件（build-tag `e2e` 门禁）
scripts/            check-file-size.sh、pre-commit
5GPN-X/             旧代码库 —— 只读参考（子树 + 独立 go.mod shim 隔离）
catalyst-ui-kit/    Tailwind Plus Catalyst kit（web/src/components/ui/ 的来源）
docs/               架构 / 安全 / 里程碑 / 技术债 / tgbot-legacy-commands
```

## 构建

```sh
make build          # 守护进程 + 安装器 + ctl（不嵌入面板）
make release        # 单二进制（嵌入 web dist，目标 ≤ 40 MB）
make test           # 单测 + race 检测
make coverage       # 各包覆盖率
make lint           # golangci-lint + web lint
make size-check     # internal/**/*.go 超过 800 行硬 fail
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

生产环境安装是下载已签名的二进制然后交棒：

```sh
curl -fsSL https://raw.githubusercontent.com/Xiuyixx/5GPN-Go/main/deploy/install.sh | sudo bash
```

脚本本体只有 83 行：从当前 GitHub Release 下载
`5gpn-installer-<os>-<arch>`，校验 sha256，`exec` 到 `install` 子命令。
后面的逻辑（用户 + 目录 + config + systemd unit + enable + 启动）全在
`cmd/5gpn-installer/`。

从旧的 5GPN-X 环境迁移：

```sh
5gpn-installer migrate-from-legacy --dry-run     # 只预演，不写盘
5gpn-installer migrate-from-legacy               # 写 /etc/5gpn/config.yaml
```

迁移会从旧的 `/opt/proxy-gateway/` 和 `/etc/proxy-gateway/` 目录里
抽出域名、DNS 上游、当前出口、规则、policy map、出口列表、TG token +
admin ids、iOS profile UUID —— **从不动老目录**。

## 质量闸（M4）

每次 push 到 `main` 都跑：

- **`ci.yml`**：`go build` + `go test -race`（Go 1.22/1.23 × Ubuntu
  22.04/24.04 矩阵）、800 LOC guard、web lint + build、e2e 冒烟
  （daemon boot → bootstrap → login → protected endpoint）、9 路
  installer-fixture 矩阵（对每种发行版 dry-run install）。
- **`security.yml`**：`govulncheck` 硬 fail、`npm audit
  --audit-level=high` 硬 fail、`go.mod` 或 `package-lock.json` 有
  改动就 on-push 触发，每周一 03:00 UTC 定时跑一次。

本地：

- `scripts/pre-commit`：pre-commit 阶段对 staged 文件跑 800 LOC 硬闸
  （`make install-hooks` 装钩子）。
- push 前 `make test` + `make lint` 都得绿。

M4 收尾时的覆盖率基线：

| 包 | 覆盖率 | 备注 |
|---|---|---|
| `internal/rules` | 75.8% | |
| `internal/exit` | 79.0% | |
| `internal/config/render` | 78.4% | |
| `internal/installer` | 77.9% | |
| `internal/ios` | 70.6% | |
| `internal/api` | 65.8% | SSE + `ListenAndServe` 走 `tests/e2e/` 补齐 |

## 文档

- `docs/architecture.md` — 顶层拓扑
- `docs/security.md` — 威胁模型 + 加固姿态
- `docs/tech-debt.md` — 已接受的取舍及到期时间
- `docs/milestones.md` — M0-M4 日志
- `docs/tgbot-legacy-commands.md` — 旧 `tgbot.py` 全量清点 + 新面板
  能力对应表（AC8 追溯）
- `.omc/plans/5gpn-refactor-consensus-plan.md` — 完整 RALPLAN-DR
  共识计划，含 17 条风险 + 15 条验收标准（内部工作稿）

## 许可

见 `LICENSE`。`web/src/components/ui/` 和 `catalyst-ui-kit/` 下的
Catalyst 文件保留 Tailwind Plus 原始许可证声明。
