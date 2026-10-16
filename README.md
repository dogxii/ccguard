# ccguard / ccg

用 Go 编写的 macOS 本地工具，管理官方 Claude Code 的登录配置、网络连接和专用 Chrome，并为 Claude Desktop 提供启动入口与可撤销配置。只依赖 Go 标准库，命令名为 `ccg`。当前版本 0.2.1。

## Claude Desktop

桌面端适配已提供，**当前机器未安装官方 Desktop，尚未完成真实应用验收**。安装包应从 [Claude 官方下载页](https://claude.com/download) 获取。Desktop 的 Code 标签页使用它自行更新的 Claude Code，不会自动调用 `ccg`。

```sh
ccg desktop setup           # 私密备份，写入本地 Code 代理设置，创建启动入口
ccg desktop                 # 启动已安装的官方 Desktop；在应用内自行官方登录
ccg desktop doctor          # 配置、签名、运行参数、服务状态；不输出凭据
ccg desktop restore         # 撤销 ccg 拥有的字段，移除启动入口
```

启动入口为 `~/Applications/Claude via ccg.app`，可拖进 Dock。默认查找 `/Applications/Claude.app` 和 `~/Applications/Claude.app`；其他位置用 `ccg config set desktop_app /绝对路径/Claude.app` 指定。已有 Desktop 进程时拒绝启动，需先 Cmd+Q 完全退出。启动前检查应用 bundle ID、代码签名完整性和 macOS Gatekeeper；不会修改应用、关闭系统安全保护或开放调试端口。

**setup 会修改默认 `~/.claude/settings.json`**：写入大小写代理变量、仅回环的旁路、官方 API 地址、官方订阅登录方式、TLS 验证和所选非必要流量选项。此文件也影响直接运行的 `claude`。原有插件和权限配置保留；发现中转认证、其他提供商或 `apiKeyHelper` 时拒绝覆盖，只显示字段名。配置前保存 0600 备份及字段恢复记录；后续 setup 不替换原始备份。其他工具修改 ccg 拥有的字段时停止写入，避免覆盖。restore 只恢复所管理的字段，保留之后新增的设置，不整文件恢复历史中转配置。

设置已匹配时，重复 setup 或启动不会重写设置和恢复记录。

桌面界面的 Chromium 网络通过启动参数指定 ccg，应用进程获得代理环境变量；本地 Code 会话通过官方支持的用户设置获取代理。**参数和配置存在不等于全部连接均受控**：应用自行设置的网络策略、更新器、MCP、外部浏览器和子工具可能使用独立连接。用原始 Claude 图标、自动启动、系统协议链接打开的应用也可能不经过启动器。ccg 没有修改全局系统代理或启用 PopCat TUN。

真实验收需要：在本地 Code 会话 `/status` 检查 Proxy，观察 ccg 中来自应用的连接，并在无重要任务时停止 ccg，确认重新请求失败而非直连；同时检查桌面界面与登录请求。**这些真实 Desktop 检查尚未完成**。云端和 SSH 会话在远端运行，本机代理和时区不能覆盖远端。

## 可选进程时区

```sh
ccg config set timezone Asia/Singapore
ccg desktop setup           # 同步本地 Code 设置，然后 Cmd+Q 并重开 Desktop
ccg config set timezone none # 取消 ccg 的覆盖；再同步 Desktop 配置
```

默认不覆盖。可选项仅通过 `TZ` 配置本地进程，不修改 macOS 系统时区。不同运行时未必遵循它；**当前官方 Claude Code 及 Desktop 的实际时区读取尚未验证**，也不保证 Electron 页面、原生功能、外部工具同步改变。浏览器诊断页仍显示浏览器实际读到的时区，不把配置值冒充检测结果。取消覆盖会恢复 setup 前的 `TZ` 值或移除 ccg 新增的值。

时区、非必要流量选项和客户端路径仅影响客户端，无需重启代理服务；代理地址、出口地区要求、检查间隔和超时改变时，才需结束现有会话后重启服务。

## 开始使用

```sh
./install.sh
ccg doctor
ccg login
ccg
```

`ccg login` 调用已安装的官方 `claude auth login --claudeai`。请在专用 Chrome 中自行完成登录、验证码和官方授权；ccg 不代管密码、不购买订阅。完成后用 `ccg auth status` 检查，再在项目目录运行 `ccg`。

```sh
ccg -- --resume
ccg -- --model sonnet
ccg browser                 # Claude 网页
ccg browser doctor          # 本地浏览器诊断页
ccg status
ccg logs
```

Claude 参数会原样传递，但 `--settings`、`--setting-sources`、`--bare` 与本工具的官方登录隔离冲突，因此拒绝使用。项目的普通设置、CLAUDE.md 和官方权限确认仍由 Claude Code 处理。ccg 不开启跳过权限模式。

## 本地目录

| 用途 | 默认位置 |
| --- | --- |
| 命令 | `~/.local/bin/ccg` |
| 官方浏览器启动器 | `~/.local/bin/ccg-browser`（指向 ccg 的符号链接） |
| 配置 | `~/.config/ccg/config.json` |
| 独立 Claude 设置、会话和登录 | `~/.config/ccg/claude`；凭据的实际存储由官方客户端管理 |
| 专用 Chrome 配置及 Cookie | `~/.local/share/ccg/chrome` |
| 服务控制套接字和日志 | `~/.local/state/ccg` |

CLI 模式不导入默认 `~/.claude/settings.json` 中的中转令牌、API 地址和插件，通过官方支持的 `CLAUDE_CONFIG_DIR` 隔离账号配置。Desktop 模式使用其自身官方登录和默认 Code 设置，具体修改见上方说明。配置采用 JSON。

`CCG_CONFIG=/绝对路径/config.json` 可指定独立测试实例；该实例的状态、Chrome 和 Claude 数据在配置文件所在目录中保存。配置文件、控制套接字及日志创建为 0600，数据目录创建为 0700。

Desktop 例外：无论 `CCG_CONFIG` 指向哪里，`desktop setup` 都修改当前 `HOME` 下的默认 `~/.claude/settings.json`。测试 Desktop 配置时还需隔离 `HOME`；不以 CLI 独立配置冒充 Desktop 配置隔离。

## 网络与服务

```text
官方 Claude / 专用 Chrome
           ↓ HTTP(S) 代理或 CONNECT
127.0.0.1:17897（ccg）
           ↓ 仅上游代理，不回退直连
127.0.0.1:7897（本机 PopCat；可修改）
           ↓ PopCat 的分流与节点选择
目标站点
```

第一次运行 `ccg`、`ccg login` 或 `ccg browser` 时启动后台连接服务。浏览器关闭后服务仍运行，以便持续使用专用配置。默认不会注册开机启动。

```sh
ccg service start
ccg service stop
ccg service restart
ccg service install         # macOS 登录时启动
ccg service uninstall       # 移除登录启动项
```

停止服务会关闭它管理的现有隧道；专用浏览器的外部连接随之失败。不要在重要会话中重启服务。已经由 Claude 执行的本地工具不会被 ccg 撤销。

服务每 60 秒并行请求三个官方域名的 `/cdn-cgi/trace`。连接失败时阻止新连接；上游恢复后恢复放行。既有隧道不会因单次健康检查失败而主动关闭。上游自身中断时连接自然报错。没有备用节点或直连回退逻辑。

健康检查不发送账号 Cookie、令牌、代码或提示词。HTTP 200 且有 trace 才显示出口 IP 和地区；其他 HTTP 状态只证明 TLS/HTTP 可达，不证明账号可用。地区变化写入日志，普通 IP 变化不视为封号风险。

可以要求三个探测结果都符合某个地区，否则阻止新连接：

```sh
ccg config set expected_country SG
ccg service restart
ccg config set expected_country none  # 取消地区匹配要求
```

这是连接一致性检查，不改变实际位置、使用资格或服务端判断。默认不设置地区要求。IP 情报库、节点标签与网站判断可能不同；trace 的结果也只代表该次请求。

## 隐私与安全边界

- 不解密 TLS，不安装根证书，不关闭证书验证。
- 默认不覆盖时区；可选 `TZ` 仅用于本地进程。不修改系统时区、语言、字体、Canvas 或系统提示词。
- 不记录请求/响应正文、URL 查询参数、令牌或密码。事件日志仅含时间、目标主机和结果，超过 1 MiB 轮转一次。
- 关闭非必要流量是默认选项；会影响自动更新以及部分依赖功能标志的功能。可手动 `claude update`。
- 如需默认功能完整性，执行 `ccg config set disable_nonessential_traffic false`，重开 Claude 会话；Desktop 需先重新运行 setup。
- 退出/停止 ccg 不会自动清空 Chrome Cookie 或官方登录数据。
- 控制接口使用 0600 的本地 Unix 套接字，代理仅监听回环地址；不开放远程调试端口。

**不是系统级网络隔离。** ccg 只能约束使用它的代理配置的连接。MCP、项目 hooks、外部命令、独立应用可能自行联网。托管设置拥有更高优先级，遇到已知冲突会拒绝启动；无法保证未来客户端版本或外部工具不会改变行为。

工具不提供反封号保证，也不能把不受支持地区中的实际使用变成受支持使用。地区政策与账户状态仍由服务商决定。

## 专用 Chrome

`ccg browser` 使用独立 user-data-dir 和固定本地代理启动已安装的 Google Chrome。不会使用普通 Chrome 的 Cookie、扩展或同步配置。不会安装自动化扩展。建议不要在此配置中开启同步或导入普通浏览器的扩展，否则会重新引入环境差异。

本地诊断页显示时区、语言、User-Agent、位置权限状态，不请求精确位置。点击“开始检查”时，由浏览器直接访问三个官方 trace 地址，不携带 Cookie；结果只显示在本页。跨域读取被阻止时会明确显示无法读取，不伪造“通过”。

Chromium 的本机地址旁路保留，用于官方 OAuth 回调。如 Chrome 已在运行，启动参数不能覆盖该实例的初始参数；修改监听地址或配置后，先手动关闭全部 ccg 专用 Chrome 窗口，再重启。

当前本机 Claude Code 2.1.87 的 `BROWSER` 启动器行为已做静态检查。未来版本若不再使用该变量，官方流程仍会显示登录 URL，可手动复制到 `ccg browser 'URL'` 打开的专用浏览器中。不要把带 OAuth 参数的 URL 发给其他人。

## PopCat

```sh
ccg popcat doctor
ccg popcat export
```

只读检查 PopCat 已保存配置和本地端口；不把保存的节点名称当成实际出口。ccg 不读取订阅令牌、不修改全局选项，不自动更换节点。

导出的 `.module` 是可选分流模板，格式参考 PopCat 自己发布的 Telegram 模块。需要时在 PopCat 设置 → 模块配置中导入，再在 CCGuard 分组中手动选择一条线路。模板不设置自动测速或 fallback，不含 HTTPS 解密和重写配置。未导入前不会生效。当前网络测试已经能访问三个官方域名时，不必为了使用 ccg 导入它。

模板唯一来源为 `popcat/ccguard.module`，通过 Go embed 内置到命令，导出与仓库文件不会各自维护。

## 构建、验证与卸载

```sh
go test -race ./...
go vet ./...
go build -trimpath -o bin/ccg ./cmd/ccg
ccg uninstall
```

卸载先撤销 ccg 拥有的 Desktop 设置和启动入口，再移除安装命令、辅助符号链接和启动项、停止服务。恢复遇到外部改动时拒绝继续，保留命令供排查。保留备份、配置和登录数据，不删除源代码。

测试覆盖 CONNECT 的缓冲字节、长流式响应、上游失败、TLS 证书校验、认证/代理环境隔离、浏览器参数以及服务启停。不会自动发起模型对话或付费 API 调用。

官方参考：[认证与独立配置](https://code.claude.com/docs/en/authentication)、[代理配置](https://code.claude.com/docs/en/network-config)、[数据使用](https://code.claude.com/docs/en/data-usage)、[支持地区](https://www.anthropic.com/legal/supported-regions)。
