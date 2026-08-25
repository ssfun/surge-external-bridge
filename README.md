# vless2surge

[![CI](https://github.com/ssfun/vless2surge/actions/workflows/ci.yml/badge.svg)](https://github.com/ssfun/vless2surge/actions/workflows/ci.yml)

vless2surge 是面向 Surge 的单文件、单进程 Mihomo Provider 网关。它把固定版本的 Mihomo Core 嵌入可执行文件，让 Mihomo 直接负责订阅获取、协议解析、最近成功缓存、健康状态和节点热更新；产品只在其上提供一层确定性的 Surge SOCKS5 身份投影与安全管理门面。

```text
HTTP / File / Inline Provider
             │
             ▼
      Embedded Mihomo ── 私有 Unix Controller
             │                  │
       Provider Proxies         └─ vless2surge allowlist API / UI
             │
       原子 Projection Snapshot
             │
Surge 节点凭据 ──> 单一认证 SOCKS5 TCP/UDP ──> v2s-router ──> 指定 Provider Proxy
```

核心原则：

- Mihomo Provider 是订阅与节点的唯一事实来源；vless2surge 不保存第二份节点快照。
- Provider 内容刷新调用 Mihomo 原生 `Update`，失败时继续使用最近成功内容。
- Provider 定时与手动刷新串行调用 Mihomo 原生 `Update`；Provider 定义变化通过进程内受控 `ApplyConfig` 只替换 Provider/Proxy 拓扑，不重建私有 Controller 或产品进程。
- 没有 Draft/Applied revision、节点骤降审批、订阅转换器或随机身份注册表。
- Authenticator、Router、节点 API 和 `/proxies` 共用同一个不可变原子 Snapshot。
- 未知、错误或已过期身份严格拒绝，绝不回落到 DIRECT。
- Surge 继续独占系统代理、TUN、规则和策略组；vless2surge 永不接管系统网络。

## Surge 工作方式

每个节点获得由以下输入计算的稳定凭据：

```text
provider_stable_id + NUL + mihomo_proxy_name
  └─ HMAC-SHA256(持久化 32-byte Projection Master Key)
```

节点参数变化但名称不变时凭据保持稳定；Provider 或节点删除后，对应身份立即失效。所有节点共享一个 SOCKS5 端口，TCP 和 UDP 都通过认证用户路由到 `v2s-router`。UDP 使用产品自管的 RFC 1929 SOCKS5 listener，把已认证 `UDP ASSOCIATE` 控制连接与 UDP 源端点绑定；无绑定、过期或歧义的数据包直接丢弃。

Surge 示例：

```ini
[Proxy Group]
VLESS = select, policy-path=http://127.0.0.1:18080/proxies, update-interval=3600

[Rule]
PROCESS-NAME,vless2surge,DIRECT
```

`PROCESS-NAME` 规则必须放在其他代理规则之前，避免本机网关出站再次进入 Surge 形成递归。如果重命名二进制，请同步修改进程名。Linux 私网网关不在 Surge 所在 Mac 上运行时不需要此规则。

## 首次运行

```bash
./vless2surge serve
```

默认端点：

- 配置台：`http://127.0.0.1:18080`
- 认证 SOCKS5 TCP/UDP：`127.0.0.1:1080`
- 数据目录：`~/.vless2surge`

首次使用只需：

1. 在 Providers 页添加 HTTP 订阅、私有 File Provider 或 Inline payload。
2. 等待 Mihomo 原生初始化；有效缓存或远端成功内容会立即进入 Projection。
3. 在节点页检查 Mihomo 延迟，按需运行真实 SOCKS TCP/UDP 端到端诊断；UDP 默认测试 `8.8.8.8:53`。该诊断由 vless2surge 自身发起，只验证项目内核链路，不经过 Surge。
4. 从总览复制 Policy Path 配置到 Surge。

后续成功刷新自动生效，不需要“应用”或重启。上游请求或解析失败时，Mihomo 保留最近成功 Provider 内容。

## 配置台

配置台提供：

- 总览：产品/Core 版本、Provider/节点/连接数、实时流量、累计流量、内存和最近错误。
- Providers：增删改、启停、手动刷新、全量健康检查、最近状态、订阅流量与投影视图筛选。
- 节点：协议能力、Mihomo 存活与延迟历史、Provider 过滤、单节点健康检查、TCP/UDP 诊断和 Surge 行复制。
- 连接：实时目标、网络、节点/Provider 链、规则、流量、关闭单个或二次确认关闭全部。
- 日志：Mihomo 结构化实时日志与产品事件；URL 查询、Header、UUID、密码、Token 和本地路径二次脱敏。
- 设置：本地/Linux 私网模式、HTTP/SOCKS 地址、Token、诊断目标、Projection Key 全量轮换和用户级系统服务。

浏览器只访问产品 allowlist。允许的 Mihomo 能力包括版本、只读配置、Provider/节点健康、连接、流量、内存和结构化日志。`PUT/PATCH /configs`、restart、upgrade、debug 和未来未列入清单的 Controller 路由均不暴露。

## 不接管系统的硬约束

产品生成配置固定满足：

```yaml
port: 0
socks-port: 0
mixed-port: 0
redir-port: 0
tproxy-port: 0
tun:
  enable: false
  auto-route: false
  auto-redirect: false
dns:
  enable: false
  listen: ""
listeners: []
```

同时禁用 iptables、NTP 系统时间写入、named listeners、tunnels、HTTP/Mixed/Redir/TProxy、DNS listener 和系统代理操作。配置在 `ApplyConfig` 前与运行后各验证一次；私有 Controller 使用数据目录内权限为 `0600` 的 Unix Socket。唯一代理入口是 Projection 与 Router 就绪后才开放的产品自管认证 SOCKS5 listener。

## 本地与 Linux 私网边界

本地模式默认只允许回环 HTTP、SOCKS、发布地址和 Policy URL。无 Management Token 时，配置台拒绝非回环 Host，降低 DNS rebinding 风险。

Linux 私网模式要求：

- Management Token 与 Policy Token 不同且都至少 16 字符；
- HTTP 写操作执行 Token、同源、方法和请求体大小检查；
- SOCKS 每个节点强制认证；
- SOCKS advertise 和 Policy URL 不能使用 `0.0.0.0` 或 `::`，且必须是回环、私网、Tailscale CGNAT、链路本地地址，或单标签私有 DNS、`.local`、`.lan`、`.internal`、`.home.arpa`、Tailscale `.ts.net` 主机名；任意公网域名不会被自动信任；
- 不支持把管理台、Policy 或 SOCKS 裸露到公网。

`/proxies` 含 SOCKS 凭据，固定返回 `Cache-Control: no-store`，支持 ETag 和独立 Policy Token。配置台不会在普通 API 中回显 Token、Header 值、SOCKS 密码、Controller Secret 或本地 Provider 路径；敏感复制必须显式确认。

## 持久化与迁移

数据目录中的权威状态只有产品部署设置、Token、Provider 定义、投影规则和两个持久密钥：

```text
~/.vless2surge/
├── gateway.json          # 0600，schema 2
├── projection.key        # 0600，确定性身份主密钥
├── controller.key        # 0600，内部 Controller Secret 来源
├── mihomo/               # 0700，Provider 最近成功缓存与 Unix Socket
└── migration-v1-readonly/
    ├── config.json       # v0.1.x 只读备份（如存在）
    └── state.json
```

首次读取 v0.1.x 数据时，URL 订阅会迁移为 HTTP Providers；旧节点 Snapshot、Draft/Applied revision、随机 SOCKS 身份和手工解析结果不会进入新状态。旧文件先复制到 `migration-v1-readonly`，再原子写入 `gateway.json`。

Projection、健康状态、实时连接、流量、内存、日志和 ETag 都可从 Mihomo Provider 与主密钥重建，不是备份权威数据。

## 系统服务

```bash
./vless2surge service status
./vless2surge service install
./vless2surge service uninstall
```

macOS 使用用户级 LaunchAgent，Linux 使用 systemd user service。配置台注册服务时不会立即启动第二个进程，避免与当前 HTTP/SOCKS listener 争抢端口；服务在下次登录时接管。CLI `service install` 会立即启用。两者都使用 `0077` umask，不安装或管理外部 Mihomo，也不修改系统代理或网络栈。

## 本地构建与验证

源码最低要求 Go 1.24.7；官方构建使用 [`.go-version`](.go-version) 固定的工具链。Mihomo 精确版本以 [`go.mod`](go.mod) 为准，前端是内嵌静态资源。

```bash
make build
./vless2surge version
make check
make test-race
make surge-check
```

`make surge-check` 使用 `/Applications/Surge.app/Contents/Applications/surge-cli` 校验与 `/proxies` 相同的关键字认证语法，以及节点参与 `select`、`url-test` 和 `fallback` 的配置矩阵；它不修改当前 Surge 配置。

四平台静态发行物：

```bash
make release VERSION=0.2.0
```

生成：

- `dist/vless2surge-darwin-arm64`
- `dist/vless2surge-darwin-amd64`
- `dist/vless2surge-linux-arm64`
- `dist/vless2surge-linux-amd64`
- `dist/SHA256SUMS`
- `dist/BUILDINFO.txt`
- `dist/THIRD_PARTY_NOTICES.txt`

发行生成器硬校验产品版本标记、目标架构、`CGO_ENABLED=0`、未被 replace 的 Mihomo 精确模块版本、上游 Git tag 与完整提交哈希、macOS 13.0 最低版本和 Linux 静态链接，并收集所有实际链接模块的许可证。`BUILDINFO.txt` 同时记录 Mihomo 上游 URL、tag、提交、模块校验和与 `go.mod` 校验和。

CI 在 `main` 和 Pull Request 上执行测试、race、vet、前端语法检查与四平台发行审计。Release 工作流只读取并验证仓库 `go.mod` 已固定的 Mihomo 版本，然后构建和发布；发布流程不会修改或提交依赖。Core 升级必须作为独立、可评审的源码变更完成，并重新执行 Provider、API、身份路由、TCP/UDP 和无 TUN 回归。

## macOS 签名与公证

GitHub Actions 默认发布未签名命令行二进制。持有 Developer ID 时，应先 `make dist`，再签名两个 macOS 架构，最后生成与签名后文件匹配的元数据：

```bash
codesign --force --options runtime --timestamp \
  --sign "Developer ID Application: Your Name (TEAMID)" \
  dist/vless2surge-darwin-arm64
codesign --force --options runtime --timestamp \
  --sign "Developer ID Application: Your Name (TEAMID)" \
  dist/vless2surge-darwin-amd64
codesign --verify --strict --verbose=2 dist/vless2surge-darwin-arm64
codesign --verify --strict --verbose=2 dist/vless2surge-darwin-amd64
make release-metadata VERSION=0.2.0
```

随后用 `xcrun notarytool submit <signed-zip> --keychain-profile <profile> --wait` 分别提交签名后的 ZIP。普通裸命令行文件不能 stapling，应分发已通过公证的 ZIP；不要在生成 `SHA256SUMS` 后再次签名或修改二进制。

仅供自己使用时可在核对官方 SHA-256 后做 ad-hoc 签名：

```bash
codesign --force --options runtime --timestamp=none --sign - vless2surge-darwin-arm64
codesign --verify --strict --verbose=2 vless2surge-darwin-arm64
```

ad-hoc 签名不建立发布者身份或 Apple 公证记录。签名会改变文件内容，因此官方校验和只适用于签名前下载的原始发行物。

## 许可

项目代码按根目录 [`LICENSE`](LICENSE) 发布。Embedded Mihomo 为 GPL-3.0，固定版本的上游许可原文保存在 [`LICENSES/mihomo.txt`](LICENSES/mihomo.txt)。[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) 说明源码与发行物的许可追踪边界。
