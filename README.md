# Surge External Bridge

[![CI](https://github.com/ssfun/surge-external-bridge/actions/workflows/ci.yml/badge.svg)](https://github.com/ssfun/surge-external-bridge/actions/workflows/ci.yml)

Surge External Bridge 是面向 Surge 的单文件、单进程、单端口 Mihomo Provider 协议桥。它把 Mihomo Core 嵌入 `SurgeEB`，让 Mihomo 直接负责订阅获取、协议解析；产品只在其上提供一层确定性的 Surge SOCKS5 身份投影与安全管理门面，让 Surge 可以使用 VLESS 等 Surge 不原生支持的协议。

```text
HTTP / File / Inline Provider
             │
             ▼
      Embedded Mihomo ── 私有 Unix Controller
             │                  │
       Provider Proxies         └─ SurgeEB allowlist API / UI
             │
       原子 Projection Snapshot
             │
Surge 节点凭据 ──> 单一认证 SOCKS5 TCP/UDP ──> surgeeb-router ──> 指定 Provider Proxy
```

核心原则：

- Mihomo Provider 管理 Surge 不支持的节点订阅。
- Surge External Bridge 通过多身份，单端口将节点映射成 SOCKS5 代理，提供 policy-path 订阅链。
- Surge 继续独占系统代理、TUN、规则和策略组；Surge External Bridge 不接管系统网络和规则。

## Surge 工作方式

每个节点的稳定凭据由 Provider 稳定 ID、Mihomo 节点名和持久化 Projection Master Key 派生。节点参数变化但名称不变时凭据保持稳定；Provider 或节点删除后，对应身份立即失效。

所有节点共享一个认证 SOCKS5 端口。TCP 和 UDP 都通过认证用户路由到 `surgeeb-router`；UDP listener 会把已认证的 `UDP ASSOCIATE` 控制连接与 UDP 源端点绑定，无绑定、过期或歧义的数据包直接丢弃。

Surge 示例：

```ini
[Proxy Group]
External = select, policy-path=http://127.0.0.1:18080/proxies, update-interval=3600

[Rule]
PROCESS-NAME,SurgeEB,DIRECT
```

`PROCESS-NAME` 规则必须位于其他代理规则之前，避免桥接进程的出站再次进入 Surge 形成递归。SurgeEB 与 Surge 不在同一台 Mac 上运行时不需要此规则。

## 首次运行

```bash
./SurgeEB serve
```

默认端点：

- 配置台：`http://127.0.0.1:18080`
- 认证 SOCKS5 TCP/UDP：`127.0.0.1:1080`
- 数据目录：`~/.surge-external-bridge`
- 数据目录环境变量：`SURGEEB_DATA_DIR`

首次使用：

1. 在订阅页添加 HTTP、私有 File Provider 或 Inline payload。
2. 等待 Mihomo 原生初始化；有效缓存或远端成功内容会立即进入 Projection。
3. 在节点页查看 Mihomo 延迟，按需运行真实 SOCKS TCP/UDP 诊断。UDP 默认测试 `8.8.8.8:53`，诊断仅验证项目内核链路，不经过 Surge。
4. 从总览复制 Policy Path 配置到 Surge。

后续成功刷新自动生效，不需要再次应用或重启。上游请求或解析失败时，Mihomo 保留最近成功的 Provider 内容。

## 配置台

配置台提供：

- 总览：产品/Core 版本、Provider/节点/连接数、实时流量、内存和最近错误。
- 订阅：增删改、启停、手动刷新、健康检查、订阅状态和名称投影筛选。
- 节点：协议、能力、Mihomo 延迟历史、TCP/UDP 诊断和 Surge 行复制。
- 连接：实时目标、节点链、规则、流量，以及关闭单个或全部连接。
- 日志：Mihomo 结构化实时日志与产品事件，敏感字段二次脱敏。
- 设置：仅本机/局域网网关模式、HTTP/SOCKS 监听、统一发布主机、Token、诊断目标、Projection Key 和用户级系统服务。

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

## 仅本机与局域网网关边界

`virtual_host` 是投影节点和 Policy Path 的唯一发布主机。Policy 基础 URL 固定根据它与 HTTP 监听端口生成，避免 SOCKS 发布地址、Policy URL 和 HTTP Host 白名单相互漂移。它可以是 `surge.eb` 这样的显式虚拟域名，但不能包含协议、端口、路径或通配符。

仅本机模式要求 HTTP 与 SOCKS 真实监听地址都是回环地址，同时允许回环 Host 和精确配置的 `virtual_host`。因此服务仍只对本机开放，但 `http://surge.eb:18080` 可以在 `surge.eb` 解析到本机时使用。

局域网网关模式同时适用于 macOS 与 Linux，并要求：

- Management Token 与 Policy Token 不同且都至少 16 字符；
- HTTP 写操作执行 Token、同源、方法和请求体大小检查；
- SOCKS 每个节点强制认证；
- `virtual_host` 会写入所有投影节点并作为 HTTP Host 精确放行；不会因为配置了 `surge.eb` 而信任整个 `.eb` 后缀；
- 直接用 IP 作为 `virtual_host` 时，只允许回环、私网、Tailscale CGNAT 或链路本地地址；
- 不支持把管理台、Policy 或 SOCKS 裸露到公网。

`/proxies` 含 SOCKS 凭据，固定返回 `Cache-Control: no-store`，支持 ETag 和独立 Policy Token。敏感复制必须显式确认。

## 多设备统一 Policy Path

所有 SurgeEB 实例配置相同的 `virtual_host`，例如 `surge.eb`，即可发布相同格式的节点和 Policy Path：

```ini
[Proxy Group]
External = select, policy-path=http://surge.eb:18080/proxies?token=POLICY_TOKEN, update-interval=3600
```

实际入口由 DNS 决定：MacBook 本机可解析到 `127.0.0.1`；家庭网络解析到 Mac mini 的局域网地址；iPhone 离开家庭网络后解析到 Linux 网关的可达地址。iPhone 可以通过 Surge 的 `[SSID Setting]` 在家庭 Wi-Fi 下切换 DNS：

```ini
[SSID Setting]
SSID:家庭WiFi dns-server=家庭DNS地址, encrypted-dns-server=off
```

家庭 DNS 将 `surge.eb` 返回为 Mac mini 地址，默认 DNS 返回 Linux 网关地址。若使用未公开注册的 `surge.eb`，两侧 DNS 都必须由自己控制；使用自有域名并配置分流 DNS 通常更简单。

Surge 的 `[Host]` Local DNS Mapping 不作用于代理服务器自身的域名。由于投影节点中的 `server = surge.eb` 正是代理服务器地址，不能只依靠 `[Host] surge.eb = ...` 完成入口切换。

## 持久化

Surge External Bridge 采用全新数据目录和配置，不读取或迁移旧项目配置：

```text
~/.surge-external-bridge/
├── gateway.json          # 0600，schema 1
├── projection.key        # 0600，确定性身份主密钥
├── controller.key        # 0600，内部 Controller Secret 来源
└── mihomo/               # 0700，Provider 最近成功缓存与 Unix Socket
```

Projection、健康状态、实时连接、流量、内存、日志和 ETag 都可从 Mihomo Provider 与主密钥重建，不是备份权威数据。

## 系统服务

```bash
./SurgeEB service status
./SurgeEB service install
./SurgeEB service uninstall
```

macOS 使用 `fun.ssfun.surgeeb` LaunchAgent，Linux 使用 `surgeeb.service` systemd user service。配置台注册服务时不会立即启动第二个进程；CLI `service install` 会立即启用。两者都使用 `0077` umask，不安装或管理外部 Mihomo，也不修改系统代理或网络栈。

## 本地构建与验证

源码工具链由 [`.go-version`](.go-version) 固定，Mihomo 精确版本以 [`go.mod`](go.mod) 为准，前端是内嵌静态资源。

```bash
make build
./SurgeEB version
make check
make test-race
make surge-check
```

`make surge-check` 使用 `/Applications/Surge.app/Contents/Applications/surge-cli` 校验配置矩阵，不修改当前 Surge 配置。

四平台静态发行物：

```bash
make release VERSION=0.2.0
```

生成：

- `dist/SurgeEB-darwin-arm64`
- `dist/SurgeEB-darwin-amd64`
- `dist/SurgeEB-linux-arm64`
- `dist/SurgeEB-linux-amd64`
- `dist/SHA256SUMS`
- `dist/BUILDINFO.txt`
- `dist/THIRD_PARTY_NOTICES.txt`

发行生成器会校验产品版本标记、目标架构、`CGO_ENABLED=0`、Mihomo 精确模块版本与来源、macOS 最低版本和 Linux 静态链接，并收集实际链接模块的许可证。

## macOS 签名与公证

GitHub Actions 默认发布未签名命令行二进制。持有 Developer ID 时，应先构建，再签名两个 macOS 架构，最后重新生成与签名后文件匹配的元数据：

```bash
make dist VERSION=0.2.0
codesign --force --options runtime --timestamp --sign "Developer ID Application: Your Name (TEAMID)" dist/SurgeEB-darwin-arm64
codesign --force --options runtime --timestamp --sign "Developer ID Application: Your Name (TEAMID)" dist/SurgeEB-darwin-amd64
codesign --verify --strict --verbose=2 dist/SurgeEB-darwin-arm64
codesign --verify --strict --verbose=2 dist/SurgeEB-darwin-amd64
make release-metadata VERSION=0.2.0
```

签名会改变文件内容，因此不要在生成 `SHA256SUMS` 后再次修改二进制。

## 许可

项目代码按根目录 [`LICENSE`](LICENSE) 发布。Embedded Mihomo 为 GPL-3.0，固定版本的上游许可原文保存在 [`LICENSES/mihomo.txt`](LICENSES/mihomo.txt)。[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) 说明源码与发行物的许可追踪边界。
