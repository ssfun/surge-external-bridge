# 自动更新

更新源固定为 `ssfun/surge-external-bridge` 的 GitHub Releases，更新整个 SurgeEB（二进制、前端、内嵌 Mihomo）。不调用 Mihomo upgrade API。

## 使用

设置页的“软件更新”提供检查、Release 说明、更新并重启、自动检查和自动安装时段。

- 默认自动检查；启动约 30 秒后首次检查，之后每 24 小时检查。失败不会影响代理运行。
- 默认不自动安装。开启后，在运行主机本地时间指定小时内安装；只对当前实例已安装的系统服务开放。
- 手动更新同样会中断现有代理连接；只有目标版本、运行实例和网关就绪均匹配才标记成功。
- 选择更高的稳定 SemVer，排除 draft/prerelease 和带预发布后缀的 tag；不自动降级，不覆盖开发构建。GitHub 元数据检查范围为最近 100 个 Release。
- 更新请求接受后，页面轮询真实状态并在断线后重连；接受请求不代表完成。
- 失败 Release 不会形成每分钟重启循环；可检查原因后显式重试。

```bash
./SurgeEB update check --data-dir /path/to/data
./SurgeEB update status --data-dir /path/to/data
./SurgeEB update install --data-dir /path/to/data
./SurgeEB update recover --data-dir /path/to/data
```

CLI `install` 对已安装服务提交独立后台任务，使用 `status` 查看最终结果。无服务时需要先停止实例，再由 CLI 原子安装；随后手动启动，状态为 `installed`，不声称已经通过运行就绪验证。

实例运行时，CLI 校验数据目录中的进程身份与 `/health` 一致，再通过该实例的管理接口检查并提交更新，复用配置写入冻结和手动进程接管。实例身份或监听地址改变时拒绝提交，不回退为离线安装。离线安装全程持有实例锁，防止下载期间启动的进程与更新并发。

`update status` 在实例运行时查询管理接口，显示实时下载进度、校验失败原因和运行版本；实例停止时读取本地事务记录。运行实例无法连接或身份不匹配时返回错误，不用旧事务记录冒充当前状态。

## 平台与安装目标

| 场景 | 行为 |
| --- | --- |
| macOS 用户 LaunchAgent | 更新 `~/Library/Application Support/SurgeEB/bin/SurgeEB` 服务副本，使用独立 launchd 任务停止、替换、启动和验证 |
| macOS 已注册但仍运行下载目录副本 | 根据服务数据目录确认同一实例；停止旧手动进程后由 LaunchAgent 接管，原下载副本不更新 |
| Linux systemd 用户服务 | 更新用户 unit 引用的程序，独立 `systemd-run --user` 任务执行 |
| Linux systemd 系统服务 | 使用原 root 上下文更新 `/usr/local/bin/SurgeEB`，独立 systemd transient unit 执行 |
| 手动运行、未注册服务 | 自动检查可用；停止后 CLI 安装并手动重启 |
| 服务属于其他数据目录 | 不选择该服务；当前实例按手动模式处理 |
| 多个实例共享程序 | 运行进程的共享文件锁阻止替换仍被其他实例使用的程序 |
| 路径不可写、符号链接目标、服务定义不匹配 | 返回明确错误，不自动提权或覆盖其他实例 |
| systemd drop-in、自定义启动参数、未加载的新 unit | 停止内置更新，要求核对服务并手动安装；不猜测实际运行路径 |
| 不支持的平台/架构、开发版本 | 检查或安装给出明确限制 |

macOS 不使用 sudo。Linux 用户服务要求用户 systemd manager 可用。后台安装 worker 与被更新服务分属独立 job/unit，不会随主服务的进程组/cgroup 一起停止。

安装副本和候选版本都必须支持 `__update-protocol` 版本 1。首次从没有自动更新能力的版本升级时，请先手动安装新 Release，并重新安装自启服务；此后才能保证自动回滚目标理解更新事务。

## 下载与恢复

1. 固定 GitHub HTTPS 项目来源；仅允许 GitHub 的 Release 资产重定向，不接受用户提供的下载 URL。更新请求使用显式配置的更新代理，留空直连，不跟随环境代理；应避免依赖正在更新的内嵌代理。
2. 临时文件位于目标目录，检查下载大小、唯一精确 SHA256 条目、Go 产品路径、平台/架构、静态构建、更新协议和产品版本。
3. 等待已进入的管理写请求完成，持久化事务，再冻结后续写入。服务定义、实例 PID/随机标识和安装目标在替换前重新核对。
4. 停止进程，备份 `gateway.json` 与旧二进制，原子替换。新进程试运行期间保持管理写入冻结，并暂停上传文件垃圾回收。
5. 60 秒内检查目标版本、随机实例标识、本次事务 ID 和网关 running 状态。失败则停新服务、恢复旧二进制和配置、重新启动并验证旧版本。
6. worker 中断后保留事务。`update recover` 可恢复未完成事务；如果二进制/服务定义后来被其他操作修改，则拒绝覆盖并保留错误。不会把恢复失败标成成功。

更新偏好存放在数据目录的 `update-settings.json`，事务在 `update-transaction.json`；均为 0600。旧程序保留在目标旁的 `.previous`，配置备份保留在私有 `update-backup/gateway.json`。这些文件可能包含配置秘密，应按原数据目录保护。

`update-instance.json` 保存本次启动的 PID 与随机实例标识（0600），仅在实例运行锁被占用时用于 CLI 身份核对。回滚和中断恢复也会获取目标程序的独占锁；其他实例仍在使用该程序时保留恢复记录，停止这些实例后可再次执行 `update recover`。

macOS worker 日志位于 `update.log`；Linux 使用对应 `surgeeb-update-<id>` 的 journal。若目标程序无法启动，可使用保留的 `update-worker update recover --data-dir ...` 执行恢复。

当前 SHA256SUMS 与二进制同由 GitHub Release 提供，信任项目 GitHub 发布权限和 HTTPS；尚未建立独立发布者签名密钥体系。未来需先配置实际签名密钥、公钥和发布流程后启用强制验签，不能把 SHA256 等同于签名。

回滚覆盖本次受控窗口内的程序与 gateway.json；不是通用数据目录快照或任意未来迁移撤销工具。未来版本若新增不可逆的数据迁移，必须同时扩展协议和恢复范围。

## 实现状态与证据（2026-09-08）

- 完成：Release 检查与校验、服务归属解析、安装事务、CLI、后台检查、设置页和自动安装偏好。
- 完成：管理认证/同源/确认、配置写入冻结、共享程序锁、旧配置恢复及中断恢复回归测试。
- 完成：`make check`、Go race、四平台交叉构建、GitHub Actions actionlint。
- 完成：macOS arm64 本地两个构建版本的真实进程升级与强制验收失败回滚；配置保持一致。
- 完成：隔离 Linux arm64 systemd 容器中系统/用户服务真实 worker 更新，均读回目标版本；worker 与主服务在独立 unit。用户服务另验“已注册但从下载副本手动运行”的接管：旧进程退出，新服务 active。
- 完成：macOS 独立 launchd worker 存活探针；浏览器从设置页检查实际 GitHub Release、修改自动检查开关并确认 0600 文件持久化。
- 未运行：用户真实 macOS LaunchAgent 的替换与下载目录接管（避免触碰现有安装）；Intel 实机更新；公开 Release 到下一公开 Release 的完整端到端安装。平台路径/参数已有自动化测试，不能替代这些实机范围。

可复现的真实进程测试在 `internal/update/runtime_integration_test.go`；设置 `SURGEEB_UPDATE_TEST_OLD`/`SURGEEB_UPDATE_TEST_NEW` 指向本源码构建的 1.0.0/1.0.1 后运行。CI 新增 macOS/Linux 作业持续执行该测试，远端作业尚未触发。

## 更新代理

“软件更新 → 更新代理”可填写 `http://127.0.0.1:7890`、`https://proxy.example:443`、`socks5://127.0.0.1:1080` 或 `socks5h://127.0.0.1:1080`，必须包含主机和端口。支持 `scheme://username:password@host:port` 认证；特殊字符需 URL 编码。SOCKS5 和 SOCKS5H 均由代理解析目标域名。

留空保持直连，不读取系统或环境代理。保存后，版本检查、SHA256SUMS、程序下载及允许的 HTTPS 跳转使用同一代理；代理无效或连接失败会报错，不回退直连。HTTPS 来源限制和校验流程保持不变。更新期间不能修改设置，请选择独立于正在更新的 SurgeEB 实例的可用代理。

设置以 `proxy_url` 保存到数据目录 `update-settings.json`（文件权限 0600）；认证信息按原值保存，配置台输入框遮蔽显示，代理连接错误不回显凭据。停止实例后执行 CLI 更新也读取此设置。

回归验证：`go test ./internal/update -run TestUpdateProxyIntegration -v -count=1`，可将输出保存为验收记录。测试使用本地 HTTP/HTTPS CONNECT、SOCKS5/SOCKS5H 认证代理和 TLS Release 服务，覆盖检查、校验和、下载与允许跳转、认证、设置持久化、忙时拒绝、无效地址拒绝、失败不回退及凭据保护，不访问 GitHub。
