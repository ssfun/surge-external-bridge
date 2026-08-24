# vless2surge

[![CI](https://github.com/ssfun/vless2surge/actions/workflows/ci.yml/badge.svg)](https://github.com/ssfun/vless2surge/actions/workflows/ci.yml)

vless2surge 是面向 Surge 的单文件、单进程 VLESS 协议网关。它把固定版本的上游 sing-box Core 编译进自身，通过一个 SOCKS5 端口和节点级用户名路由，让 Surge 把订阅中的每个 VLESS 节点作为独立策略选择、测速和故障转移。

当前源码固定的 Core 版本以 `go.mod` 为准；每个 GitHub Release 的精确 sing-box 版本记录在随附的 `BUILDINFO.txt`。

当前 macOS 发行基线为 macOS 13.0 或更高版本；Linux arm64/amd64 发行物采用静态链接。

## 工作方式

```text
Surge SOCKS 节点 A ── 用户 A ─┐
Surge SOCKS 节点 B ── 用户 B ─┼─> vless2surge:1080 ─> auth_user ─> VLESS outbound
Surge SOCKS 节点 C ── 用户 C ─┘
```

- Surge 继续负责系统代理、TUN、规则和策略组。
- vless2surge 不创建 TUN，不修改系统代理。
- 所有节点共用一个 SOCKS5 数据端口，但拥有独立随机用户名和密码。
- 配置台、订阅调度、`/proxies` 与 Embedded Core 位于同一进程。
- `/proxies` 只发布已经成功应用的 revision；未应用草稿不会提前影响 Surge。
- 节点页支持单个或四路并发批量测试，同时验证 Web/TCP 与真实 SOCKS5 UDP DNS 链路；测试目标和单节点超时可配置。

## 下载与自动构建

正式构建由 [GitHub Actions](https://github.com/ssfun/vless2surge/actions) 完成：

- 推送到 `main` 或提交 Pull Request 时，CI 自动执行测试、race、vet 和四平台 QA 构建；
- 推送 `v*` tag 时，Release 工作流验证源码已固定 sing-box 最新正式版，然后自动发布 GitHub Release；
- 也可以在 Actions 中手动运行 Release 工作流并输入版本号。工作流会读取 sing-box `releases/latest`，把最新正式 Core 精确写入 `go.mod/go.sum` 并提交到 `main`，再创建 tag、测试、构建和发布；
- 每个 Release 包含 macOS/Linux 的 arm64、amd64 四个二进制，以及项目 `LICENSE`、`SHA256SUMS`、`BUILDINFO.txt` 和完整第三方许可清单。

发布页：[github.com/ssfun/vless2surge/releases](https://github.com/ssfun/vless2surge/releases)。

## 本地构建

源码最低要求 Go 1.24.7，官方构建固定使用 `.go-version` 中的 Go 1.27.0。前端为内嵌静态资源，不需要 Node.js 才能构建。Reality/uTLS 依赖 `with_utls`，gRPC 使用上游标准实现的 `with_grpc`；后者避免 gRPC-lite 在当前 Core 中的并发问题。Makefile 会强制启用并在发行审计中验证两个标签，不要用缺少标签的裸 `go build` 替代正式构建。

```bash
make build
./vless2surge version
```

构建四个平台的单文件产物：

```bash
make dist VERSION=0.1.0
```

输出：

- `dist/vless2surge-darwin-arm64`
- `dist/vless2surge-darwin-amd64`
- `dist/vless2surge-linux-arm64`
- `dist/vless2surge-linux-amd64`

生成带校验和、构建信息和完整第三方许可清单的本地发行目录：

```bash
make release VERSION=0.1.0
```

额外输出 `dist/SHA256SUMS`、`dist/BUILDINFO.txt` 和 `dist/THIRD_PARTY_NOTICES.txt`。生成器会硬校验产品版本、目标架构、构建标签、Core 版本、macOS 13.0 最低版本标记和 Linux 静态链接，并收集 Go 工具链及全部链接模块的许可声明。

## macOS Developer ID 签名与公证

GitHub Actions 默认发布未签名的命令行二进制。如需提供经过 Apple Developer ID 签名和公证的版本，应在发布前准备 `Developer ID Application` 证书、对应 Team ID 和 App 专用密码。

先构建但暂不生成最终校验和：

```bash
make dist VERSION=0.1.0
```

对两个架构分别签名并验证：

```bash
codesign --force --options runtime --timestamp \
  --sign "Developer ID Application: Your Name (TEAMID)" \
  dist/vless2surge-darwin-arm64
codesign --force --options runtime --timestamp \
  --sign "Developer ID Application: Your Name (TEAMID)" \
  dist/vless2surge-darwin-amd64
codesign --verify --strict --verbose=2 dist/vless2surge-darwin-arm64
codesign --verify --strict --verbose=2 dist/vless2surge-darwin-amd64
```

签名后再生成与最终二进制匹配的构建清单和校验和：

```bash
make release-metadata VERSION=0.1.0
```

首次使用 `notarytool` 时保存公证凭据，然后把每个签名二进制单独打包并提交：

```bash
xcrun notarytool store-credentials vless2surge-notary \
  --apple-id "you@example.com" \
  --team-id "TEAMID" \
  --password "APP-SPECIFIC-PASSWORD"

ditto -c -k --keepParent dist/vless2surge-darwin-arm64 dist/vless2surge-darwin-arm64.zip
ditto -c -k --keepParent dist/vless2surge-darwin-amd64 dist/vless2surge-darwin-amd64.zip
xcrun notarytool submit dist/vless2surge-darwin-arm64.zip --keychain-profile vless2surge-notary --wait
xcrun notarytool submit dist/vless2surge-darwin-amd64.zip --keychain-profile vless2surge-notary --wait
spctl --assess --type execute --verbose=2 dist/vless2surge-darwin-arm64
spctl --assess --type execute --verbose=2 dist/vless2surge-darwin-amd64
```

普通命令行二进制不能像 `.app`、`.pkg` 或 `.dmg` 那样 stapling 公证票据，因此应分发已经通过公证的 ZIP；Gatekeeper 会在线验证公证记录。不要在生成 `SHA256SUMS` 后再次修改或签名二进制。

## macOS 本地自签名

没有 Apple Developer ID 时，可以对自己使用的二进制做本地签名。自签名不能建立 Apple 公证记录，也不适合作为面向公众的可信发布方式。

Release 直接下载的裸二进制可能没有可执行权限。先用 `uname -m` 确认架构（Apple Silicon 为 `arm64`，Intel 为 `x86_64`），然后恢复权限；修改权限不会改变文件的 SHA-256：

```bash
chmod 755 vless2surge-darwin-arm64
```

建议严格按“校验官方 SHA-256 → 本地签名 → 验证签名 → 必要时移除 quarantine → 运行”的顺序操作。签名会原地修改二进制，因此官方校验和只能在签名前核对。

### 方法一：ad-hoc 签名

这是单机使用最快的方法，不需要创建证书。先在签名前核对下载文件；以下命令应在同时包含二进制和 `SHA256SUMS` 的目录执行，并只校验当前架构对应的清单条目：

```bash
grep 'vless2surge-darwin-arm64$' SHA256SUMS | shasum -a 256 -c -
codesign --force --options runtime --timestamp=none --sign - vless2surge-darwin-arm64
codesign --verify --strict --verbose=2 vless2surge-darwin-arm64
codesign --display --verbose=4 vless2surge-darwin-arm64
```

Intel Mac 将文件名替换为 `vless2surge-darwin-amd64`。ad-hoc 签名只表明文件签名后没有再被修改，没有可供其他设备信任的签名身份。

### 方法二：钥匙串自建 Code Signing 证书

1. 打开“钥匙串访问”；
2. 选择“钥匙串访问 → 证书助理 → 创建证书”；
3. 名称可设为 `vless2surge Local`，身份类型选择“自签名根”，证书类型选择“代码签名”；
4. 创建后打开证书的“信任”设置，仅在自己的登录钥匙串中设为信任；
5. 用 `security find-identity -v -p codesigning` 确认签名身份可用。

然后签名并验证：

```bash
codesign --force --options runtime --timestamp=none \
  --sign "vless2surge Local" \
  vless2surge-darwin-arm64
codesign --verify --strict --verbose=2 vless2surge-darwin-arm64
codesign --display --verbose=4 vless2surge-darwin-arm64
```

如需在自己的另一台 Mac 使用，需要安全导出并导入证书及私钥，并在那台 Mac 上单独信任该证书。不要公开分享自签名私钥。

浏览器下载的文件可能带有 quarantine 属性。只有在 SHA-256 已经与 Release 清单一致、且你确认文件来自本项目后，才可以移除该文件的 quarantine 标记：

```bash
xattr -p com.apple.quarantine vless2surge-darwin-arm64
xattr -d com.apple.quarantine vless2surge-darwin-arm64
```

签名会改变二进制内容，因此签名前的 `SHA256SUMS` 在签名后失效。若要保存本地签名版本，应重新计算并单独保存校验和，不得用它覆盖官方 Release 的校验清单。

## 首次运行

```bash
./vless2surge serve
```

默认端点：

- 配置台：`http://127.0.0.1:18080`
- SOCKS5：`127.0.0.1:1080`
- 数据目录：`~/.vless2surge`

打开配置台后：

1. 添加订阅 URL、导入 Clash `proxy-providers`，或粘贴 VLESS/Base64/Clash `proxies` 内容。
2. 刷新订阅并检查保留、丢弃节点及原因。
3. 校验并应用第一份配置版本。
4. 在“节点”页面发起单个或批量实测；测试会经过当前本地网关，分别显示 TCP、UDP 延迟和具体失败阶段。Web 目标、UDP DNS 服务器与超时可在“配置 → 节点测试”中修改。
5. 复制总览中的 Surge `policy-path` URL，在 Surge 中加入策略组并测速。

配置台将“网关”“配置”“诊断”“日志”拆分为独立页面。网关页面负责启动、重启和停止 Embedded Engine；部署地址、访问保护与系统服务在“配置”页面管理。

示例：

```ini
[Proxy Group]
VLESS = select, policy-path=http://127.0.0.1:18080/proxies, update-interval=3600
```

macOS 本地模式还必须避免 vless2surge 自身的 VLESS 出站再次被 Surge 代理，否则会形成代理递归。将下面规则放在 Surge `[Rule]` 中其他代理规则之前：

```ini
PROCESS-NAME,vless2surge,DIRECT
```

如果你重命名了可执行文件，请把规则中的进程名同步替换为实际文件名。Linux 私网网关不运行在 Surge 所在的 Mac 上，不需要这条本机进程规则。

## 系统服务

macOS 使用用户级 LaunchAgent，Linux 默认使用 systemd user service：

```bash
./vless2surge service status
./vless2surge service install
./vless2surge service uninstall
```

也可以在配置台“配置 → 运行与维护”中管理。服务只托管一个 vless2surge 进程，不要求系统安装 sing-box。

安装流程会先把数据目录解析为绝对路径，并以 `0700` 权限创建或收紧。LaunchAgent 和 systemd user service 都使用 `0077` umask；macOS 服务输出保存在私有数据目录，Linux 日志由 systemd journal 承载。

## Linux 私网网关

推荐通过 Tailscale、WireGuard 或可信局域网连接，不要把 SOCKS5 或配置台裸露在公网。

当 HTTP 监听地址不是回环地址时，必须同时设置：

- Management Token：保护配置台 API；
- Policy Token：保护包含 SOCKS 凭据的 `/proxies`。

两类 Token 必须不同且至少 16 字符；配置台可用浏览器密码学随机源生成 32 字符安全值。配置台会生成带 Policy Token 的 URL。`SOCKS advertise` 和 Policy URL 不得使用 `0.0.0.0` 或 `::`，因为通配地址只能用于监听。SOCKS5 端口本身使用每节点独立身份认证，未知用户会被拒绝。

SOCKS、HTTP 和 Policy 终点会拒绝公开 IP 字面量；请使用回环、RFC1918、Tailscale CGNAT、WireGuard 或解析到可信私网的主机名。无 Management Token 的回环配置台只接受回环 Host 与回环 Policy URL，以降低浏览器 DNS rebinding 风险。

## 状态与恢复

- `config.json` 与 `state.json` 以 `0600` 权限原子写入。
- 订阅刷新失败或返回空内容时保留最近成功快照。
- 节点异常骤降会把草稿标记为高风险，必须显式确认。
- 候选 Engine 启动失败时恢复上一 applied revision。
- 连续三次非正常退出后进入安全模式，只启动配置台。
- 节点凭据轮换只生成草稿，应用成功后才发布给 Surge。

## 验证

```bash
make test
make test-race
make vet
node --check internal/webassets/static/app.js
make surge-check
```

`make surge-check` 需要 macOS 已安装 Surge，只校验生成节点的 Surge 配置语法，不修改当前 Surge 配置。

测试包含真实回环链路：

- SOCKS5 用户认证到不同 VLESS outbound；
- TLS+uTLS+ALPN、Reality+Vision+uTLS 的完整握手；
- WebSocket、gRPC、HTTP 和 HTTP Upgrade 传输；
- SOCKS5 UDP Relay 经 VLESS XUDP 到 UDP 目标；
- 未知用户拒绝；
- 150 个 Reality/Vision 身份与 outbound 共用一个 SOCKS5 inbound；
- 候选端口失败后的旧 revision 回滚；
- 订阅缓存、风险应用、重启恢复和 applied-only `/proxies`；
- 混合订阅不静默丢弃、Clash provider 请求头/间隔、来源变更的快照保底；
- 管理 Token、Policy Token、ETag、同源保护和敏感字段脱敏。

## 安全与许可证

订阅 URL、VLESS UUID、Reality 参数、SOCKS 密码和管理 Token 都属于敏感信息。不要公开数据目录、配置预览或 `/proxies` URL。

项目根目录 [`LICENSE`](LICENSE) 采用与 sing-box 一致的 GNU GPL v3 或更高版本条款，并保留额外的名称/关联表述限制。`LICENSES/sing-box.txt` 保存当前 Core 的上游许可原文。公开分发内嵌 Core 的二进制时必须保留上游声明，并提供对应源代码与可追溯构建信息。
