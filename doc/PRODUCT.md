# vless2surge 产品需求文档

> PRD 版本：1.0<br>
> 更新日期：2026-08-25<br>
> 状态：目标架构已实现，发布候选验证中<br>
> 当前已发布基线：v0.1.6，内嵌 sing-box<br>
> 目标架构：内嵌固定版本 Mihomo，Provider 为订阅与节点权威，vless2surge 为 Surge 身份投影与安全管理门面<br>
> 实现固定版本：Mihomo v1.19.30

---

## 1. 文档目的

本文定义 vless2surge 下一代架构的完整产品需求、系统边界、用户流程、功能要求、安全约束、迁移方式和验收标准。

本文描述的是目标产品，不代表当前 v0.1.6 已经实现。当前版本中围绕 sing-box 建立的订阅解析、Snapshot、Draft/Applied revision、风险更新确认、配置编译和 Box 重建逻辑，均属于迁移前实现，不再作为目标架构的长期约束。

目标产品遵循一个核心判断：

> 上游订阅是节点集合的唯一事实，Mihomo Provider 负责获取、解析、缓存、刷新和运行这些节点；vless2surge 不再维护第二套代理节点事实，只把 Mihomo 当前节点投影为 Surge 可独立使用的 SOCKS5 策略。

---

## 2. 一句话产品定义

vless2surge 是面向 Surge 的单文件、单进程协议网关：它内嵌 Mihomo，通过一个 SOCKS5 端口和节点级身份映射，将 Mihomo Provider 中的 VLESS 节点投影为 Surge 可独立选择、测速和故障转移的普通 SOCKS5 节点。

Surge 继续负责系统代理、TUN、DNS、规则和策略组；Mihomo 只负责订阅节点、协议出站、健康检查与运行观测；vless2surge 只负责 Surge 身份投影、安全 API 门面、配置台和服务生命周期。

---

## 3. 背景与用户问题

Surge 无法直接使用 VLESS，尤其无法直接承载常见的 Reality、Vision、uTLS、gRPC、HTTP Upgrade 和 xHTTP 等组合。格式转换工具只能改变表示形式，不能把 VLESS 握手伪装成其他协议。

用户可以让 Surge 连接另一个代理内核的单一 SOCKS/Mixed 端口，但 Surge 随后只能看到一个出口，无法对订阅中的每个 VLESS 节点独立执行：

- `select`；
- `url-test`；
- `fallback`；
- 单节点延迟观察；
- 单节点故障隔离。

为每个节点开放一个本地端口虽然能解决独立选择问题，但会让端口、监听器、配置和防火墙复杂度随节点数量线性增长。

用户真正需要的是：

> Provider 中有多少个当前有效、应由网关承载的 VLESS 节点，Surge 中就能看到多少个独立 SOCKS5 策略；这些策略共用一个地址和端口，通过不同凭据选择不同上游节点。

---

## 4. 产品目标

### 4.1 核心目标

1. 直接使用 Mihomo Provider 接收 HTTP 订阅、URI 列表、Base64 URI 和 Clash YAML。
2. 以上游最近一次成功订阅结果为唯一节点事实，不再建立第二套 Draft/Applied 节点状态机。
3. 将每个目标节点投影为 Surge 可独立使用的 SOCKS5 节点。
4. Provider 内容更新时由 Mihomo 原地替换节点；Provider 定义等产品配置变化时使用进程内 `ApplyConfig`，两类更新均不得通过重启进程生效。
5. 使用 Mihomo API 提供节点状态、延迟、Provider 刷新、连接、流量、内存和日志管理能力。
6. 保持单文件、单进程、无需外部 Mihomo 安装的交付体验。
7. 明确禁止 Mihomo 接管系统代理、开启 TUN 或建立透明代理入口。
8. 保持 macOS 本地模式和 Linux 私网网关模式。

### 4.2 体验目标

- 用户只配置订阅，不需要理解 Mihomo 顶层配置。
- 订阅刷新成功后，Surge 节点集合自动更新，无“生成配置—确认应用—重启内核”的日常流程。
- 订阅失败时继续使用 Mihomo 最近成功缓存。
- 配置台展示的节点、SOCKS 认证可用节点和 `/proxies` 发布节点来自同一原子投影快照。
- 节点管理能力不弱于常见 Mihomo Dashboard，但只暴露与本产品有关的安全子集。

### 4.3 工程目标

- 删除自有协议字段编译器和大部分协议兼容测试，依赖固定版本 Mihomo 的解析与出站实现。
- 删除重复的订阅 Fetcher、Parser、Snapshot、Draft/Applied revision 和风险应用流程。
- 将项目核心收敛为 Provider 配置、身份投影、动态路由、安全门面和 Surge 输出。
- 不维护 Mihomo fork；只在 vless2surge 内实现薄适配层。

---

## 5. 非目标

目标版本不做以下事情：

- 替代 Surge；
- 修改 macOS/Linux 系统代理；
- 创建或启用 TUN；
- 创建 Redir、TProxy、Mixed、HTTP 代理或透明代理监听器；
- 管理 Surge 的规则、DNS、MITM 或策略选择；
- 允许用户上传并运行任意 Mihomo 顶层配置；
- 向浏览器裸露完整 Mihomo Controller；
- 在运行时自动升级 Mihomo Core；
- 自研或 fork VLESS、Reality、Vision、uTLS 等协议实现；
- 提供机场多租户、订阅销售或计费面板；
- 将订阅、节点凭据、访问日志上传到外部服务；
- 首个目标版本支持 Windows、iOS、Android 或菜单栏 GUI；
- 默认把 Surge 已原生支持的所有协议再次代理一层。

---

## 6. 目标用户与场景

### 6.1 目标用户

- 已使用 Surge 负责系统代理和规则分流；
- 订阅中包含大量 VLESS Reality/Vision 节点；
- 希望保留 Surge 的策略组、测速和故障转移能力；
- 不想单独安装、配置和管理 Mihomo；
- 希望在 Mac 本机运行，或部署到 Linux 私网主机供 Surge 访问。

### 6.2 主要场景

1. 添加一个机场订阅，自动看到其中所有 VLESS 节点。
2. 在 Surge 中通过 `policy-path` 获取独立节点列表。
3. Provider 自动刷新后，新增、删除和修改的节点自动反映到 Surge。
4. 在配置台查看节点存活状态、延迟历史、协议能力和 Provider 更新时间。
5. 手动刷新某个 Provider 或发起整组健康检查。
6. 查看实时连接、节点流量、代理链和结构化日志。
7. 在 Linux 私网主机运行网关，由 Mac 上的 Surge 通过私网地址访问。

---

## 7. 产品硬约束

以下决策是目标架构的不可变约束：

1. **上游权威。** Mihomo Provider 最近一次成功结果就是当前节点事实。
2. **不做风险审批。** 节点骤降只要是 Mihomo 认可的成功更新，就立即成为当前事实。
3. **失败保留。** 拉取、解密或解析失败时，由 Mihomo 保留最近成功缓存。
4. **单端口身份选路。** 所有 Surge 映射节点共用一个 SOCKS5 监听端口。
5. **未知身份拒绝。** 未命中当前投影的用户名或密码必须失败，绝不回落到直连。
6. **单文件、单进程。** Web、管理 API、身份投影和 Mihomo 数据面位于同一可执行文件和进程。
7. **固定 Core。** 每个产品版本固定精确 Mihomo 版本，不浮动依赖。
8. **无协议子进程。** 不通过 `exec` 启动外部 Mihomo。
9. **Surge 管系统。** 系统代理、TUN、规则、DNS 和策略组始终由 Surge 管理。
10. **Mihomo 不接管系统。** 顶层配置只能由产品生成，TUN 和所有透明入口永久禁用。
11. **API 不裸露。** 浏览器只访问 vless2surge 的权限受控门面。
12. **Provider 内容不等于 Core 配置。** 订阅只能提供 Provider 节点，不能改变监听器、TUN、DNS、Controller 或运行模式。
13. **认证先于监听。** 在有效 Projection Snapshot、自定义 Authenticator 和 Router 全部就绪前，SOCKS listener 不得接受连接。
14. **UDP 身份不丢失。** SOCKS5 UDP 必须绑定到已经通过用户名密码认证的 `UDP ASSOCIATE` 控制连接；没有明确身份关联的 UDP 包一律丢弃。

---

## 8. 权威数据模型

### 8.1 唯一事实

目标产品只保留三类事实：

| 事实 | 权威来源 | 持久化责任 |
|---|---|---|
| Provider 配置 | vless2surge 用户配置 | vless2surge |
| Provider 当前成功节点 | Mihomo Provider | Mihomo 缓存 |
| Surge 身份投影规则 | 当前 Provider 节点 + 本地主密钥 | 可重建，不保存节点副本 |

### 8.2 不再存在的状态

目标架构不再保存：

- 最近成功 Snapshot 的产品副本；
- Draft revision；
- Applied revision；
- 风险更新待确认状态；
- 节点字段标准化副本；
- 每节点随机身份注册表；
- 编译后的 sing-box JSON；
- Box 回滚配置。

### 8.3 投影不是第二份节点事实

投影快照只保存运行所需的短生命周期引用和派生字段：

- Provider ID；
- Mihomo 当前 Proxy 名称与对象引用；
- 稳定公开节点 ID；
- Surge 展示名；
- 派生用户名与密码；
- 节点的静态能力；
- 用于 ETag 的投影哈希。

投影快照不得修改 Provider 节点，不得成为订阅恢复来源。进程重启后必须能从 Provider 当前缓存和本地主密钥完整重建。

节点存活、延迟、连接和流量属于 Mihomo 的动态观测数据，不进入身份投影快照，也不参与 `/proxies` ETag。管理 API 在响应时以公开节点 ID 将这些动态数据与当前投影视图关联。

---

## 9. 总体架构

### 9.1 单进程结构

```text
┌─────────────────────────────────────────────────────────────┐
│ vless2surge                                                  │
│                                                             │
│  Web 配置台                                                   │
│  └─ 总览 / Providers / 节点 / 连接 / 日志 / 设置              │
│                                                             │
│  安全管理门面                                                 │
│  ├─ Management API                                          │
│  ├─ Mihomo API allowlist 适配                                │
│  ├─ WebSocket 转发与脱敏                                     │
│  └─ /proxies /health                                        │
│                                                             │
│  Surge 身份投影                                               │
│  ├─ Projection Snapshot                                     │
│  ├─ Deterministic Authenticator                             │
│  └─ auth_user Router ProxyAdapter                           │
│                                                             │
│  Embedded Mihomo                                            │
│  ├─ Proxy Providers                                         │
│  ├─ 一个 SOCKS5 Listener                                    │
│  ├─ 协议 Outbounds                                          │
│  ├─ Health Check / Connections / Traffic / Logs             │
│  └─ 私有 Controller                                         │
└─────────────────────────────────────────────────────────────┘
```

### 9.2 数据流

```text
订阅 URL
   ↓
Mihomo HTTP Provider
   ↓  YAML / URI / Base64 解析、缓存、定时刷新
当前 Provider Proxies
   ↓
Projection Snapshot
   ├── /proxies → Surge
   ├── SOCKS Authenticator
   └── auth_user Router → 当前 Mihomo Proxy
```

### 9.3 管理流

```text
Web 配置台
   ↓ Management Token
vless2surge API
   ↓ allowlist + 参数验证 + 脱敏
Mihomo Controller（Unix Socket 或私有回环）
```

---

## 10. Mihomo Provider 需求

### 10.1 支持的来源

每个 Provider 支持：

- `http`；
- `file`；
- `inline`，仅用于手动粘贴或迁移；
- Clash `proxies:` YAML；
- URI 列表；
- Base64 编码 URI 列表。

URI 与 YAML 的实际协议支持范围以固定 Mihomo 版本为准，vless2surge 不复制协议解析规则。

### 10.2 Provider 配置

配置台至少支持：

- 名称；
- URL；
- 启用状态；
- 刷新间隔；
- 受控请求 Header：`Authorization`、`Cookie`、`User-Agent`、`Accept`、`Accept-Language`；
- 下载所用代理；
- 响应大小上限；
- 健康检查开关、URL、间隔、超时、期望状态码和 lazy；
- 手动刷新；
- 手动健康检查。

Header 中的 Authorization 和 Cookie 必须按敏感数据存储与脱敏，只能随 HTTPS Provider 使用；任意 Header 名、Host、代理认证 Header 和不会被 HTTP 客户端跨域剥离的自定义 Token Header 不进入首版允许列表。

虽然 Mihomo Provider 原生支持 `filter`、`exclude-filter`、`exclude-type` 和 `override`，目标产品不使用这些字段改写 Provider 当前节点。名称和协议筛选只属于 Surge 投影设置；首版不提供 Provider override，以保持上游内容的可观测性和权威性。

### 10.3 更新语义

- Provider 的定时与手动触发由 vless2surge 在同一 Manager 串行化；实际获取、解析、缓存和内容替换仍调用 Mihomo Provider 原生能力。固定的 Mihomo v1.19.30 自动 pull loop 与公开 `Update()` 对内部时间字段存在未同步访问，因此首版不同时启用两条上游触发路径，也不为此维护 Mihomo fork。
- 拉取或解析失败时不替换当前 Proxies。
- 内容成功且发生变化时立即替换 Provider 当前 Proxies。
- 节点数量骤降不触发产品级确认。
- Provider 当前版本变化后，Projection Snapshot 自动重建。
- Snapshot 重建不得要求重启进程、重建 SOCKS listener 或重载完整 Mihomo 配置。
- 已建立连接可以继续持有旧 Proxy 对象；新连接必须使用最新投影。

目标产品明确区分两种热更新路径：

1. **Provider 内容更新**：定时刷新、手动刷新和远端内容变化直接调用 Mihomo Provider 的更新能力；成功后由 Provider 原地替换 Proxies，不调用完整 `ApplyConfig`。
2. **产品配置更新**：新增、编辑、停用或删除 Provider 时，由产品重新生成受控配置并执行进程内受控 `ApplyConfig`；实现只初始化并原子替换允许变化的 Mihomo Provider/Proxy 拓扑，不重建固定的私有 Controller，也不重复应用不可变 General、TUN、DNS 或 listener 设置；不得退出或拉起 vless2surge 进程。

受控 `ApplyConfig` 前必须完成解析与系统接管不变量校验，应用过程必须与 Provider `Update()` 串行化；应用后重新注入固定 `v2s-router` 并重建原子投影。产品自管的已认证 SOCKS listener 不属于 Mihomo 顶层端口或任意用户配置，监听地址未变化时不得因配置应用重建。若监听地址确实变化，允许受控地关闭旧 listener、装好认证与 Router 后再打开新 listener，但不得出现无认证窗口。

Provider 定义变化可能使相关既有连接结束，界面保存前必须明确提示；普通 Provider 内容刷新不得由 vless2surge 主动关闭既有连接。

### 10.4 Provider 缓存

- 缓存文件由 Mihomo 管理。
- 路径必须位于 vless2surge 私有数据目录。
- 不允许 Provider 自定义任意绝对缓存路径。
- 启动时优先使用有效本地缓存，再按间隔更新远端。
- 缓存文件权限不得放宽数据目录边界。

### 10.5 投影协议范围

- 首个目标版本默认只发布 VLESS 节点，保持产品定位和 Surge 原生协议边界。
- Mihomo Provider 可以保留订阅中的其他节点，但它们不默认进入 Surge 投影。
- 类型、Provider 和名称包含/排除筛选都只是投影视图，不删除或改写 Provider 节点。
- 每个 Provider 可以保存独立的投影筛选规则；这些规则由 vless2surge 在构建 Projection Snapshot 时执行，不写入 Mihomo Provider 的过滤或 override 字段。
- 后续可以增加明确的协议 allowlist，复用同一投影架构；不得为新协议重新建立自有字段解析器。

---

## 11. Surge 身份投影

### 11.1 原子快照

Projection Snapshot 必须一次性构建并原子发布。以下消费者必须读取同一个不可变快照：

- SOCKS 身份认证；
- auth_user 动态路由；
- `/proxies`；
- 节点列表 API；
- ETag 与 revision 标识；
- 端到端诊断。

不得分别更新用户名、路由和 `/proxies`，避免短暂不一致。

### 11.2 节点键

默认节点键为：

```text
provider_stable_id + NUL + mihomo_proxy_name
```

`provider_stable_id` 是创建 Provider 时由产品生成并持久化的随机 UUID，不使用可编辑名称或 URL；修改名称、URL、Header 或刷新设置不会改变该 ID。

语义：

- 同一 Provider 内名称不变，即视为同一 Surge 身份；
- 服务器、UUID、Reality 参数或传输参数变化时，凭据保持稳定；
- 上游改名视为删除旧节点并新增节点；
- 不同 Provider 的同名节点互不冲突。

### 11.3 凭据生成

首次启动生成至少 32 字节随机 Projection Master Key，以 `0600` 存储。

用户名与密码确定性派生：

```text
username = "v2s_" + Base64URL(HMAC-SHA256(master, "user:" + node_key)) 的安全截断
password = Base64URL(HMAC-SHA256(master, "pass:" + node_key))
public_id = "n_" + Base64URL(SHA256(node_key)) 的安全截断
```

要求：

- 同一主密钥和节点键始终生成同一凭据；
- 用户名取摘要前 22 个 Base64URL 字符，加前缀后共 26 个 ASCII 字符；密码使用完整 43 个 Base64URL 字符；
- `public_id` 取摘要前 22 个 Base64URL 字符，只用于 Management API 路径和关联，不作为认证凭据；它不随 Master Key 轮换变化；
- 密码不得写入日志或公开 API；
- 比较密码时使用常量时间比较；
- Snapshot 构建必须检测用户名和公开 ID 冲突，发现任何冲突即拒绝发布；
- 未知、过期或密码错误的身份必须拒绝；
- 不提供无认证模式。

全量凭据轮换通过生成新 Master Key 完成。目标首版不提供每节点单独轮换，以避免重新引入身份注册表。

### 11.4 自定义 Authenticator

vless2surge 实现 Mihomo `Authenticator`：

- `Verify(user, pass)` 只查询当前 Projection Snapshot；
- 节点从 Provider 删除后，对应身份立即失效；
- `Users()` 只在 Mihomo 管理逻辑需要时返回当前用户名列表；
- Snapshot 替换不重建 SOCKS listener。

### 11.5 动态 Router ProxyAdapter

vless2surge 注册一个固定的 `v2s-router`：

- 从 `Metadata.InUser` 读取已认证用户名；
- 产品自管 SOCKS 入口为 TCP 与 UDP 元数据都注入 `SpecialProxy=v2s-router`，不经过可变规则匹配；
- 在当前 Projection Snapshot 中查找目标 Proxy；
- TCP 调用目标的 `DialContext`；
- UDP 调用目标的 `ListenPacketContext`；
- 继承目标 Proxy 的 UDP 能力和错误；
- 未命中、节点已删除或能力不支持时明确失败；
- 永远不回落到 DIRECT；
- 连接链与 Provider 链应保留真实目标节点信息，供 Mihomo API 展示。

### 11.6 SOCKS5 UDP 身份绑定

Mihomo v1.19.30 默认 SOCKS UDP listener 不会把 TCP 握手中认证的用户名自动带入 UDP 数据包，因此目标产品不得直接把默认 UDP listener 当作节点级身份路由入口。vless2surge 必须在薄适配层中维护 SOCKS5 `UDP ASSOCIATE` 绑定：

- TCP 握手只接受 SOCKS5 RFC 1929 用户名密码认证，拒绝无认证方法和 SOCKS4；
- `UDP ASSOCIATE` 成功后记录认证用户名、控制连接、客户端源 IP、声明的 UDP 源端点和生命周期；
- UDP 数据包只有在精确命中仍存活的 association 后，才能被注入对应 `Metadata.InUser` 并交给 `v2s-router`；
- 不得只按客户端 IP 推断身份；同一 Surge 主机的并发 association 必须通过不同 UDP 源端点隔离；
- 客户端声明端口为零时，只有在该源 IP 恰好存在一个未绑定 association 的情况下，才允许首包绑定实际源端点；存在歧义时必须拒绝；
- 控制 TCP 连接关闭、超时或被取消后立即释放 association；表容量、单 IP 数量和空闲时间必须受限；
- 已删除节点的旧 association 即使仍存在，也会在 Router 查询当前 Snapshot 时失败；
- 未关联、过期、源端点不匹配或重放的 UDP 包静默丢弃并只记录限速后的脱敏统计。

TCP listener 与 UDP relay 使用同一发布地址和端口，构成一个逻辑 SOCKS5 入口。该绑定逻辑只解决身份投影，不复制 Mihomo 的代理协议、DNS 或出站实现。

### 11.7 展示名称

- 默认使用 Mihomo Proxy 名称。
- 多 Provider 时可以加 Provider 前缀，例如 `机场 A · 香港 01`。
- 同一输出中名称必须唯一。
- 若前缀后仍冲突，使用稳定、可读的序号消歧。
- 展示名变化可以改变 Surge 策略名称，但不得改变同一节点键的凭据。

---

## 12. Surge 输出

### 12.1 `/proxies`

每个投影节点输出：

```ini
节点名 = socks5, host, port, username=..., password=..., udp-relay=true
```

实际语法必须通过当前 Surge 配置检查工具验证。

### 12.2 Policy Path

用户在 Surge 中配置：

```ini
[Proxy Group]
VLESS = select, policy-path=http://127.0.0.1:18080/proxies, update-interval=3600
```

Linux 私网模式使用私网可达地址和 Policy Token。

### 12.3 一致性

- `/proxies` 只读取当前 Projection Snapshot。
- ETag 由规范化后的实际发布内容计算，包括节点展示名、SOCKS 发布地址、端口和凭据；不加入刷新时间、健康状态或其他不影响输出的 Provider 元数据。
- Provider 未变化时重复请求必须返回稳定 ETag。
- 实际发布内容变化后 ETag 必须变化；仅 Provider 版本或动态健康状态变化而发布内容相同时 ETag 保持不变。
- 若尚无有效投影节点，返回明确错误或空列表状态，不得发布 DIRECT 替代项。

### 12.4 递归保护

macOS 本地模式必须保留：

```ini
PROCESS-NAME,vless2surge,DIRECT
```

该规则防止 vless2surge 的上游连接再次被 Surge 捕获。重命名可执行文件时，文档和配置台提示必须使用实际进程名。

---

## 13. Mihomo 管理 API

### 13.1 Controller 部署

优先顺序：

1. macOS/Linux 使用私有 Unix Socket；
2. 无法使用 Unix Socket 时使用随机回环端口和独立高强度 Secret；
3. 禁止 Controller 监听非回环 TCP 地址。

Controller 路径和凭据不得出现在公开配置、日志或 `/proxies` 中。

### 13.2 允许复用的能力

| 产品能力 | Mihomo API |
|---|---|
| Core 版本 | `GET /version` |
| 当前基本配置只读投影 | `GET /configs` |
| 所有 Provider | `GET /providers/proxies` |
| 单个 Provider | `GET /providers/proxies/:provider` |
| 手动刷新 Provider | `PUT /providers/proxies/:provider` |
| Provider 健康检查 | `GET /providers/proxies/:provider/healthcheck` |
| 单节点信息 | `GET /providers/proxies/:provider/:proxy` |
| 单节点测试 | `GET /providers/proxies/:provider/:proxy/healthcheck` |
| 实时连接 | `GET/WS /connections` |
| 关闭单个连接 | `DELETE /connections/:id` |
| 实时流量 | `GET/WS /traffic` |
| 内存 | `GET/WS /memory` |
| 结构化日志 | `GET/WS /logs?format=structured` |

实际路由必须适配固定 Mihomo 版本，并通过集成测试验证。

### 13.3 禁止暴露的能力

配置台和公开 Management API 不得透传：

- `PUT /configs`；
- `PATCH /configs`；
- `/restart`；
- `/upgrade`；
- `/upgrade/ui`；
- `/debug/*`；
- 任意路径形式的配置加载；
- 未列入 allowlist 的未来 Mihomo API。

### 13.4 API 门面要求

- 浏览器只访问 vless2surge 路由。
- 每个写操作执行 Management Token、同源、方法和请求大小检查。
- Provider 与 Proxy 路径参数必须从当前公开 ID 解析，不能直接拼接上游路径。
- 响应必须删除订阅 URL Secret、Header、SOCKS 密码、Controller Secret 和本地路径。
- Mihomo 错误转换为稳定的产品错误码和中文可读消息。
- WebSocket 必须在 vless2surge 层验证权限、限制连接数并在客户端断开时释放上游连接。
- 不因 Mihomo API 暂时不可用而停止现有 SOCKS 数据面。

---

## 14. 配置台信息架构

### 14.1 总览

展示：

- vless2surge 与 Mihomo 版本；
- 网关运行状态；
- SOCKS 监听与发布地址；
- Provider 数量、当前投影节点数和存活数；
- 当前连接数；
- 实时上下行和累计流量；
- 内存使用；
- Surge Policy URL；
- `PROCESS-NAME,...,DIRECT` 提示；
- 最近 Provider 错误摘要。

### 14.2 Providers

支持：

- 新增、编辑、启用、停用和删除 Provider；
- URL、Header、刷新间隔和健康检查配置；
- 独立标注为“Surge 投影视图”的名称与协议筛选，不得伪装成订阅内容修改；
- 最近更新时间、节点数、订阅流量信息和最近错误；
- 单个手动刷新；
- 单个 Provider 全量健康检查；
- 展开查看 Provider 当前节点；
- 敏感 Header 查看与复制确认。

### 14.3 节点

每个节点展示：

- 展示名与 Provider；
- 协议类型；
- 存活状态；
- 当前及历史延迟；
- UDP、UOT、XUDP、TFO、MPTCP、SMUX 能力；
- 当前连接数和上下行；
- 单节点测试；
- 复制 Surge 单节点行；
- 端到端 SOCKS TCP/UDP 验证；
- 最近错误与代理链。

支持按 Provider、协议、存活状态和名称过滤。

### 14.4 连接

基于 Mihomo `/connections`：

- 实时列表；
- 源/目标、网络类型、节点、Provider、代理链、规则、开始时间和流量；
- 按节点、Provider、域名或 IP 过滤；
- 关闭单个连接；
- 关闭全部连接属于高风险操作，必须二次确认；
- 敏感目标信息默认只对具有 Management 权限的本地用户展示。

### 14.5 日志

基于结构化 `/logs`：

- 实时显示 info、warning、error、debug；
- 默认不启用 debug；
- 支持级别、Provider、节点和关键词过滤；
- 对 URL 查询、Header、UUID、密码、Token 和本地路径二次脱敏；
- 提供复制诊断摘要，不提供未经处理的全量敏感导出。

### 14.6 设置

分组：

- 部署模式与监听地址；
- HTTP 管理端口；
- SOCKS 监听、发布地址与端口；
- Management Token 与 Policy Token；
- Projection Master Key 全量轮换；
- 默认节点测试 URL、超时和 UDP DNS 目标；
- 节点投影协议范围；
- 系统服务安装、状态和卸载；
- 数据目录、版本和诊断；
- 危险操作确认。

配置台不得提供 TUN、Mixed、Redir、TProxy、系统代理或任意 Mihomo YAML 编辑入口。

---

## 15. 数据与持久化

### 15.1 vless2surge 配置

产品配置只保存：

- 部署模式；
- HTTP/SOCKS bind、advertise 和端口；
- Provider 定义；
- 每个 Provider 不随可编辑字段变化的 stable ID；
- 投影协议 allowlist；
- 每个 Provider 的投影名称包含与排除规则；
- Provider 前缀显示选项；
- Management/Policy Token；
- 节点测试设置；
- 服务与自动启动设置；
- 产品 schema 版本。

### 15.2 敏感状态

单独保护：

- Projection Master Key；
- Management Token；
- Policy Token；
- Provider 敏感 Header；
- Mihomo Controller Secret；
- Provider 缓存中的节点凭据。

数据目录权限为 `0700`，配置、Secret 和状态文件为 `0600`，服务使用 `0077` umask。

### 15.3 Mihomo 私有目录

Mihomo HomeDir 必须位于产品数据目录中，例如：

```text
~/.vless2surge/mihomo/
```

包含 Provider 缓存、Controller Socket 和 Mihomo 必需的内部缓存。不得使用用户已有 Clash/Mihomo 客户端目录，也不得读取或覆盖其他客户端配置。

### 15.4 可重建状态

以下内容不得作为必须备份的权威数据：

- Projection Snapshot；
- 节点健康状态；
- 实时连接；
- 流量速率；
- 内存信息；
- 临时日志流；
- ETag 缓存。

---

## 16. 系统接管防护

### 16.1 顶层配置不变量

产品生成的 Mihomo 配置必须显式满足：

```yaml
port: 0
socks-port: 0
mixed-port: 0
redir-port: 0
tproxy-port: 0

tun:
  enable: false

dns:
  enable: false
  listen: ""
```

Mihomo 顶层和 named listeners 中不创建任何代理入口。唯一 SOCKS5 listener 由 vless2surge 在认证和 Router 就绪后，通过 Mihomo listener 能力显式创建；此外只允许私有 Controller。

### 16.2 应用前校验

每次构造或加载 Mihomo 配置后、应用前必须检查：

- TUN 关闭；
- HTTP、Mixed、Redir、TProxy 端口为零；
- 不存在额外 named listener；
- 不存在 iptables 或路由表自动配置；
- DNS listener 未启用；如未来出站必须使用 Mihomo 内部解析器，仍须保持 `listen` 为空且不得对外提供 DNS 服务；
- Controller 为 Unix Socket 或回环地址；
- Provider 缓存路径在私有 HomeDir；
- 用户输入未进入顶层未知字段。

任何一项不满足时拒绝启动 Core。

### 16.3 运行后断言

启动后读取运行配置并检查同一组不变量。发现 TUN、额外监听器或公开 Controller 时立即停止 Embedded Mihomo，记录脱敏错误，并让配置台保持可用以供修复。

### 16.4 系统代理

vless2surge 不调用：

- macOS `networksetup`；
- `scutil`；
- Linux 路由表、iptables 或 nftables 修改；
- 桌面代理开关；
- 任何等价系统代理 API。

系统代理和 TUN 是否启用只由 Surge 决定。

---

## 17. 网络与安全边界

### 17.1 本地模式

- HTTP 和 SOCKS 默认监听回环地址。
- 无 Management Token 的配置台只允许回环 Host。
- `/proxies` 可在纯回环模式下免 Policy Token，但界面应建议启用。
- 防止 DNS rebinding 和跨站写请求。

### 17.2 Linux 私网模式

- 非回环 HTTP 必须配置 Management Token。
- 非回环 `/proxies` 必须配置独立 Policy Token。
- 两个 Token 必须不同且至少 16 字符，默认生成 32 字符以上随机值。
- SOCKS 每个节点都必须认证。
- advertise 和 Policy URL 不能使用 `0.0.0.0` 或 `::`。
- 只允许回环、RFC1918、Tailscale CGNAT、WireGuard 地址，以及单标签私有 DNS 名、`.local`、`.lan`、`.internal`、`.home.arpa` 或 Tailscale `.ts.net` 主机名；任意公网域名不因使用域名形式而自动受信。
- 不建议且不支持裸露公网部署。

### 17.3 Provider 输入

- URL 只允许 HTTP/HTTPS。
- 设置合理响应大小上限和超时。
- 自定义 Header 不跨源泄露到重定向目标。
- 禁止 Provider 指定任意本地写入路径。
- 手动内容限制请求体大小。
- Provider 返回的顶层 `tun`、`dns`、`listeners` 等字段不得进入运行配置。

### 17.4 凭据

- `/proxies` 包含 SOCKS 凭据，必须设置 `Cache-Control: no-store`。
- 浏览器复制完整 Policy URL 或单节点行需要敏感信息确认。
- Management API 默认不返回 SOCKS 密码。
- Token、主密钥、订阅 Header、UUID 和密码不得进入日志。

---

## 18. 生命周期与服务

### 18.1 启动顺序

1. 加载并迁移 vless2surge 配置。
2. 验证数据目录和文件权限。
3. 确保 Projection Master Key 存在。
4. 生成不含任何代理 listener 的最小 Mihomo 初始化配置，并把固定 `v2s-router` 注入受控 Proxy 映射。
5. 执行系统接管防护校验。
6. 启动 Embedded Mihomo、私有 Controller 和 Provider；此阶段不得接受代理连接。
7. Provider 从有效缓存或远端初始化。
8. 构建并原子发布 Projection Snapshot。
9. 为产品自管 listener 创建持有当前原子 Snapshot 的自定义 Authenticator，并确认 Router 已可用。
10. 使用该专用 AuthStore 启动唯一允许的 SOCKS5 TCP listener 和同端口 UDP relay，并启用 association 身份绑定；不得依赖会被 `ApplyConfig` 临时清空的全局静态用户列表。
11. 启动 Web/API 与 `/proxies`。
12. 执行运行后不变量断言和本地 SOCKS 认证探测。

配置台应尽量可在 Provider 或 Core 启动失败时继续访问。

实现可以采用分阶段配置或 Mihomo 内部 listener API，但不得先以无认证或临时认证方式开放 SOCKS 端口。

### 18.2 Provider 更新

1. Mihomo 拉取并解析新内容。
2. 失败则保留旧 Provider Proxies。
3. 成功则 Provider 原地替换 Proxies 并增加版本。
4. vless2surge 观察版本变化。
5. 构建新 Projection Snapshot。
6. 原子替换认证、路由和 `/proxies` 视图。
7. 记录不含敏感信息的节点增删摘要。

### 18.3 配置应用

1. 接收并校验产品配置，不接收任意 Mihomo YAML。
2. 在内存中生成完整受控 Mihomo 配置并执行应用前不变量检查。
3. 将固定 `v2s-router` 注入 Proxy 映射。
4. 串行执行进程内受控 `ApplyConfig`，只替换 Provider/Proxy 拓扑，不重建 Controller 或不可变系统设置，也不退出进程。
5. 等待 Provider 初始化，从其当前缓存或远端成功内容构建新 Projection Snapshot。
6. 原子发布 Snapshot 并执行运行后不变量检查。
7. SOCKS bind/port 未变化时保持原 listener；发生变化时按“先认证与路由、后监听”的顺序受控切换。
8. 应用失败时保持管理面可访问并返回稳定错误；不得以 DIRECT、无认证 listener 或任意旧产品节点副本兜底。

### 18.4 停止

- 停止接受新的 Web 写请求；
- 关闭 WebSocket；
- 停止 Provider 后台任务；
- 关闭 SOCKS listener 和 Embedded Mihomo；
- 删除 Unix Socket；
- 保持 Provider 最近成功缓存；
- 优雅退出并释放端口。

### 18.5 系统服务

- macOS 使用用户级 LaunchAgent；
- Linux 使用 systemd user service；
- 服务只运行一个 vless2surge 进程；
- 不安装或管理外部 Mihomo；
- 安装、卸载和状态查询保持幂等；
- 不执行系统代理或网络栈修改。

---

## 19. 节点测试与诊断

### 19.1 Mihomo 原生测试

普通节点延迟、Provider 健康检查和历史状态使用 Mihomo API，不再自建重复实现。

### 19.2 端到端投影测试

仍保留产品特有测试：

```text
SOCKS 凭据
  → 自定义 Authenticator
  → TCP InUser / UDP ASSOCIATE 身份绑定
  → Metadata.InUser + SpecialProxy=v2s-router
  → v2s-router
  → 当前 Provider Proxy
  → TCP Web / UDP DNS 目标
```

该测试用于发现 Mihomo outbound 健康但身份投影错误的情况。
默认 UDP DNS 目标为 `8.8.8.8:53`。TCP/UDP 端到端诊断由 vless2surge 自身连接产品 SOCKS listener，只验证项目内核链路，不经过 Surge。真实 Surge 验收只覆盖 Policy Path、策略组与 TCP 互通，不重复执行 UDP 节点诊断。

### 19.3 分层诊断

诊断至少包括：

- 产品配置；
- Mihomo 版本；
- 系统接管不变量；
- Provider 缓存与最近刷新；
- Projection Snapshot 数量与哈希；
- SOCKS 正确凭据、错误密码和未知用户；
- TCP 端到端；
- UDP 端到端；
- UDP association 隔离、过期、错误源端点和无绑定包拒绝；
- `/proxies` 与当前 Snapshot 一致性；
- Controller 私有性；
- Surge 递归保护提示；
- 系统服务状态。

---

## 20. API 与功能需求

### 20.1 Provider

| 编号 | 需求 |
|---|---|
| FR-P01 | 可以创建、编辑、停用和删除 HTTP/File/Inline Provider。 |
| FR-P02 | HTTP Provider 支持自定义 Header、间隔、大小上限和健康检查。 |
| FR-P03 | URI、Base64 URI 和 Clash YAML 由 Mihomo 原生解析。 |
| FR-P04 | 更新失败时继续使用最近成功 Provider 内容。 |
| FR-P05 | 成功更新立即成为运行事实，不提供节点骤降确认。 |
| FR-P06 | 可以通过安全门面手动刷新和健康检查。 |
| FR-P07 | Provider 定时与手动刷新串行调用原生 Update；Provider 定义变化使用进程内受控 ApplyConfig 替换 Provider/Proxy 拓扑，均不重启产品进程。 |

### 20.2 投影

| 编号 | 需求 |
|---|---|
| FR-I01 | 每个目标节点生成稳定、唯一的 SOCKS 身份。 |
| FR-I02 | 认证、路由、节点 API 和 `/proxies` 使用同一原子 Snapshot。 |
| FR-I03 | 未知、过期和错误凭据明确拒绝。 |
| FR-I04 | Provider 节点参数变化但名称不变时凭据保持稳定。 |
| FR-I05 | Provider 节点删除后身份和 `/proxies` 条目自动消失。 |
| FR-I06 | TCP 和 UDP 均按 `Metadata.InUser` 路由到对应 Proxy。 |
| FR-I07 | 名称和协议筛选只影响 Projection Snapshot，不改变 Mihomo Provider 当前节点。 |
| FR-I08 | UDP 包必须来自有效、无歧义的已认证 UDP ASSOCIATE，并携带绑定用户进入 Router。 |

### 20.3 管理

| 编号 | 需求 |
|---|---|
| FR-M01 | 节点列表展示 Mihomo 健康、延迟历史和协议能力。 |
| FR-M02 | 可以查看实时连接、流量、内存和结构化日志。 |
| FR-M03 | 可以关闭单个连接；关闭全部连接需要二次确认。 |
| FR-M04 | Mihomo API 只通过 allowlist 门面访问。 |
| FR-M05 | WebSocket 受 Management Token 和并发限制保护。 |
| FR-M06 | Mihomo API 故障不应停止已有 SOCKS 数据面。 |

### 20.4 Surge

| 编号 | 需求 |
|---|---|
| FR-S01 | `/proxies` 输出当前所有投影节点。 |
| FR-S02 | 输出支持 ETag、`no-store` 和可选 Policy Token。 |
| FR-S03 | 同一节点凭据在重启和普通 Provider 更新后稳定。 |
| FR-S04 | 配置台生成可复制的 Policy URL 和 Surge 示例。 |
| FR-S05 | 本地模式明确提示 `PROCESS-NAME,vless2surge,DIRECT`。 |

### 20.5 系统安全

| 编号 | 需求 |
|---|---|
| FR-X01 | TUN 永久禁用。 |
| FR-X02 | 不修改系统代理、路由表、iptables 或 nftables。 |
| FR-X03 | 除一个 SOCKS listener 外不启动其他代理入口。 |
| FR-X04 | 不接受任意 Mihomo 顶层配置。 |
| FR-X05 | Controller 只使用 Unix Socket 或回环地址。 |
| FR-X06 | 运行配置违反不变量时立即停止 Embedded Mihomo。 |
| FR-X07 | Projection、Authenticator 和 Router 就绪前 SOCKS 端口不得监听。 |

---

## 21. 非功能需求

### 21.1 可用性

- Provider 临时失败不影响最近成功节点。
- 普通 Provider 刷新不重启进程和 SOCKS listener。
- Provider 定义与产品拓扑配置通过进程内受控 `ApplyConfig` 生效；私有 Controller 和 SOCKS 地址不变时对应 listener 均保持不变。
- Provider 更新期间现有 TCP 连接不应被产品主动关闭。
- 新请求在 Snapshot 原子切换前后只看到完整旧版本或完整新版本。

### 21.2 性能

- 150 个节点的投影构建应在普通桌面设备上快速完成，不阻塞数据面。
- `/proxies` 与节点列表读取不得持有长时间全局写锁。
- 实时日志、连接和流量 WebSocket 应实施背压与客户端上限。
- Snapshot 使用不可变结构与原子指针替换，避免请求路径复制全量节点。

### 21.3 兼容性

- macOS 13.0+ arm64/amd64；
- Linux arm64/amd64；
- `CGO_ENABLED=0` 静态 Linux 构建目标；
- Surge 当前稳定版本的 SOCKS5、UDP Relay 与 Policy Path。

### 21.4 可维护性

- Mihomo 固定精确版本。
- 适配层集中在独立包中，不让 Mihomo 类型扩散到整个产品。
- 对 Mihomo API 响应建立产品 DTO，避免前端直接依赖上游 JSON。
- Core 升级必须重新运行 Provider、API、身份路由、TCP/UDP 和无 TUN 回归。

### 21.5 可访问性与响应式

- 配置台支持桌面和 390px 移动宽度；
- 不产生页面级横向溢出；
- 状态不只依赖颜色；
- 实时列表提供暂停和减少动态更新选项；
- 键盘可访问主要写操作和确认对话框。

---

## 22. 失败与边界场景

| 场景 | 预期行为 |
|---|---|
| Provider 网络失败 | 保留最近成功节点，显示错误与重试时间 |
| Provider 内容无法解析 | 保留最近成功节点，不替换 Snapshot |
| Provider 成功返回少量节点 | 立即采用，符合上游权威原则 |
| Provider 删除节点 | 新 Snapshot 删除身份与 `/proxies` 条目；旧连接自然结束 |
| Provider 修改同名节点 | 身份不变，新连接使用新 Proxy |
| Provider 增加同名节点 | 以 Mihomo 的名称去重结果为准 |
| 无有效 VLESS 节点 | 数据面保持运行但 `/proxies` 明确为空；不回落 DIRECT |
| Controller API 暂时故障 | 管理功能降级，SOCKS 数据面继续运行 |
| Projection 构建失败 | 不继续向新连接发布旧节点；关闭新认证与 `/proxies` 投影，保留既有连接自然结束，记录错误并重试 |
| Master Key 损坏 | 拒绝启动身份数据面，配置台提供恢复或全量轮换 |
| SOCKS 端口被占用 | Core 启动失败，配置台继续可用并显示端口错误 |
| 检测到 TUN 或额外入口 | 立即停止 Embedded Mihomo，保持管理面可诊断 |
| Surge 捕获自身上游 | 诊断提示 PROCESS-NAME 直连规则 |
| WebSocket 客户端过慢 | 丢弃旧帧或断开该客户端，不阻塞 Core |
| UDP 包没有有效 association | 丢弃，不进入 Mihomo 出站，不回落 DIRECT |
| 同一客户端存在歧义的零端口 association | 拒绝首包绑定并提示兼容性诊断，不猜测用户身份 |

---

## 23. 从 v0.1.x 迁移

### 23.1 可迁移配置

- 订阅名称、URL、受控 Header、刷新间隔和过滤；
- HTTP/SOCKS bind、advertise 和端口；
- Management Token 与 Policy Token；
- 节点测试目标与超时；
- 服务自动启动设置；
- Provider 前缀展示偏好。

### 23.2 不迁移的旧状态

- sing-box 编译配置；
- Snapshot 节点副本；
- Draft/Applied revision；
- 风险更新待确认状态；
- 旧 Identity Registry；
- 每节点随机凭据；
- Engine 回滚状态。

首次迁移会生成 Projection Master Key，因此 Surge 节点凭据会整体变化一次。迁移完成后，同名节点凭据保持稳定。

### 23.3 迁移安全

- 在新 Mihomo 数据面通过无 TUN 校验、Provider 初始化和端到端 SOCKS 测试前，不删除旧数据。
- 旧状态以只读备份保留一个迁移周期。
- 不同时运行 sing-box 和 Mihomo 两个数据面。
- 迁移失败时不得自动覆盖旧配置。
- 旧配置含首版白名单外的 Header 时，迁移必须明确报错并保留只读备份，不得静默删除或提交一份失去认证信息的新配置。
- UI 明确提示这是 Core 架构迁移和凭据变更。

---

## 24. 实施里程碑

### M0：可行性原型

- 固定一个 Mihomo 版本并嵌入单文件；
- 启动一个 SOCKS listener 和一个 HTTP Provider；
- 验证 URI、Base64 URI 和 Clash YAML；
- 验证失败保留最近成功缓存；
- 实现自定义 Authenticator；
- 实现 TCP/UDP `v2s-router`；
- 通过项目内核端到端测试验证 UDP ASSOCIATE 的源端点行为和每身份并发隔离；
- 验证 Provider 原地更新不重启 listener；
- 验证 TUN、系统代理和透明入口均未启用。

### M1：核心投影

- Projection Snapshot；
- 确定性凭据；
- `/proxies`、ETag 和 Policy Token；
- Provider 增删改同步；
- 多 Provider 名称与身份隔离；
- 端到端 TCP/UDP 诊断。

### M2：管理 API 与配置台

- Provider 管理；
- 节点健康与延迟；
- 实时连接、流量、内存和日志；
- 安全 API 门面；
- 响应式与敏感信息体验。

### M3：迁移、安全与服务

- v0.1.x 配置迁移；
- 私有 Mihomo HomeDir；
- Unix Socket Controller；
- 无 TUN 双重校验；
- LaunchAgent/systemd user 生命周期；
- 崩溃恢复和权限审计。

### M4：发布验证

- 全量测试、race、vet；
- 四平台构建；
- Linux 静态链接验证；
- 许可与 BUILDINFO；
- Surge 配置检查；
- 真实 Provider、Reality/Vision、TCP/UDP、更新与缓存恢复；
- GitHub Actions 和 Release 制品验证。

---

## 25. 发布验收标准

目标版本必须同时满足：

1. 发行物是单一 vless2surge 可执行文件，不要求外部 Mihomo。
2. 运行时只有一个 vless2surge 进程，没有协议子进程。
3. HTTP Provider 能直接加载 URI、Base64 URI 和 Clash YAML。
4. Provider 更新失败时继续使用最近成功节点。
5. Provider 成功增删改节点后，Projection 与 `/proxies` 自动更新。
6. 同名节点参数更新后 SOCKS 凭据保持稳定。
7. 未知用户、错误密码和已删除节点身份均被拒绝。
8. 150 个节点共用一个 SOCKS5 端口并能正确选路。
9. TCP 和 UDP 均能通过指定身份到达正确 Provider Proxy。
10. 同一 Surge 主机的多个 UDP ASSOCIATE 能按源端点隔离身份；无绑定、过期和歧义 UDP 包被拒绝。
11. 普通 Provider 更新不重启进程和 SOCKS listener。
12. 更新期间现有连接不被 vless2surge 主动关闭。
13. Provider 定义变化由进程内受控 `ApplyConfig` 生效，不重建私有 Controller，且不会产生无认证、DIRECT 回落或额外 listener。
14. Mihomo API 能提供 Provider、节点延迟、连接、流量、内存和结构化日志。
15. 浏览器无法直接访问完整 Mihomo Controller。
16. `/configs` 写入、restart、upgrade 和 debug API 未被产品门面暴露。
17. TUN 关闭，Mixed/Redir/TProxy/HTTP 代理入口均未监听。
18. 产品未修改系统代理、路由表、iptables 或 nftables。
19. 启动和 ApplyConfig 过程中不存在无认证或认证未就绪的 SOCKS 暴露窗口。
20. 本地模式与 Linux 私网模式的 Token、Host 和地址边界通过测试。
21. `/proxies` 通过 Surge 配置语法检查并能参与 select/url-test/fallback。
22. macOS 13+ 构建和基础运行通过；Linux arm64/amd64 仅要求构建、静态链接和发行元数据验证，不要求在本机或容器内运行制品。
23. 固定 Mihomo 版本、产品版本、构建标签、许可和校验和写入发行元数据。

---

## 26. 成功指标

### 26.1 产品指标

- 首次添加有效订阅到获得可复制 Policy URL，不超过五个明确步骤。
- 正常订阅刷新不需要用户执行“应用”或“重启”。
- 配置台能解释 Provider、节点、投影和 Surge 四层状态。
- 节点管理主要数据来自 Mihomo，不维护重复健康状态。

### 26.2 技术指标

- Provider 更新失败不造成当前节点清空。
- Snapshot 不一致事件为零。
- 未认证或错误认证流量直连事件为零。
- TUN 或透明监听意外启用事件为零。
- Core 升级适配集中在 Mihomo adapter/API DTO 层。
- 相比 v0.1.x，删除自有订阅解析、revision 和协议编译的主要生产代码路径。

---

## 27. 风险与控制

### 27.1 Mihomo 嵌入接口稳定性

风险：Mihomo 是完整应用内核，Go 包与 API 未必承诺长期稳定。

控制：

- 固定精确版本；
- 将所有 Mihomo 类型封装在适配包；
- 前端只依赖产品 DTO；
- 每次 Core 升级运行完整集成测试；
- 不自动升级 Core。

### 27.2 自定义 Router 正确性

风险：动态代理选择处于 TCP/UDP 主链路。

控制：

- 实现保持极薄；
- Snapshot 不可变；
- 未命中 fail closed；
- 覆盖 TCP、UDP、并发更新和节点删除测试；
- 保留真实 chain/provider chain。

### 27.3 Provider 与投影观察延迟

风险：Provider 已更新但投影尚未重建。

控制：

- 观察 Provider 版本；
- 事件驱动优先，短周期轮询兜底；
- Snapshot 原子替换；
- 管理界面显示 Provider 版本与投影哈希；
- 重建失败时投影 fail closed，不把上一 Snapshot 继续声明为当前上游事实，并快速重试。

### 27.4 SOCKS5 UDP association 正确性

风险：SOCKS5 UDP 数据报本身不携带用户名密码，若没有把它与已认证控制连接可靠绑定，多个 Surge 节点可能串线或形成未认证 UDP 入口。

控制：

- 不直接使用缺少用户传播的默认 UDP listener；
- association 必须以已认证控制连接为根，并绑定精确 UDP 源端点；
- 无绑定和有歧义的数据包 fail closed；
- 控制连接关闭即撤销，容量和超时有界；
- 覆盖同 IP 多用户并发、端口复用、伪造源端点、过期和节点删除测试；
- 必须通过项目自身的真实 SOCKS5 UDP 端到端链路以及 association 并发、伪造、过期和歧义回归；不得用 Mihomo 节点健康结果替代身份投影验证。

### 27.5 上游成功但节点骤降

这是已接受的产品语义，不视为异常回滚条件。配置台可以展示变化摘要和 Provider 错误，但不得阻止已经成功的 Mihomo 更新。

### 27.6 Controller 权限

风险：完整 Mihomo API 能重载配置、重启或升级 Core。

控制：

- Unix Socket/回环；
- 独立 Secret；
- 浏览器不直连；
- 严格 allowlist；
- 禁止配置、restart、upgrade 和 debug 写接口。

---

## 28. 许可证与发布

- vless2surge 当前继续使用仓库既有许可证。
- Mihomo 及全部链接依赖的许可证必须进入 THIRD_PARTY_NOTICES。
- Release 必须记录精确 Mihomo 版本和提交来源。
- 自动化必须验证构建中确实链接预期 Core，而不是仅相信 `go.mod`。
- 发布制品包含版本、平台、架构、Go 工具链、Core 版本、校验和和许可清单。
- macOS 签名、公证与本地自签名流程继续由 README 说明。

---

## 29. Definition of Done

目标架构只有在以下条件全部完成后才算落地：

- sing-box 数据面和目标架构不再需要的旧状态机已从生产路径移除；
- Mihomo Provider 成为唯一订阅与节点权威；
- Projection Snapshot、Authenticator、Router 和 `/proxies` 共用同一事实；
- Mihomo API 管理门面完成且不存在危险透传；
- 无 TUN、无系统代理、无透明入口经过代码、配置、运行时和集成测试四层验证；
- Provider 失败缓存、成功热更新、TCP/UDP、连接保持和节点删除行为通过测试；
- 配置迁移可恢复且不会静默覆盖旧数据；
- 配置台完成桌面与移动端验证；
- 四平台构建、许可、制品和 GitHub Actions 全部通过；
- 真实 Surge Policy Path、策略组测速和实际 VLESS 服务互通完成验收。
