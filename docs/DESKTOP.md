# Claude Desktop（实验性）

ccg 可以为已安装的官方 Claude Desktop 配置本地 Code 会话代理，并创建启动入口。**尚未完成真实 Desktop 应用的端到端验收；配置与参数检测通过不代表全部流量均受控。**

## 使用

从 [Claude 官方下载页](https://claude.com/download) 安装应用，并先完成 ccg 的 CLI 安装与配置：

```sh
ccg desktop setup           # 备份并配置默认 Code 设置，创建启动入口
ccg desktop                 # 启动 Desktop，在应用内自行官方登录
ccg desktop doctor          # 检查配置、应用签名、运行参数和代理服务
ccg desktop restore         # 撤销 ccg 管理的字段并移除启动入口
```

启动入口为 `~/Applications/Claude via ccg.app`，可拖进 Dock。默认查找 `/Applications/Claude.app` 和 `~/Applications/Claude.app`，其他位置可指定：

```sh
ccg config set desktop_app /绝对路径/Claude.app
```

启动前检查应用的 bundle ID、代码签名完整性和 Gatekeeper，不修改应用或关闭系统保护，不开放调试端口。已存在 Desktop 主进程时拒绝启动；请先用 Cmd+Q 完全退出，仅关闭窗口不够。`ccg desktop` 会同步本地 Code 设置并要求代理检查通过，随后直接启动应用。

## 会修改哪些设置

**setup 和启动都会配置当前 `HOME` 下的 `~/.claude/settings.json`，因此也影响直接运行的原始 `claude`。** Desktop 的 Code 标签页使用应用自行管理的 Claude Code，不会自动调用 `ccg`，也不复用 CLI 的独立账号目录。

ccg 管理的字段包括：

- 大小写 HTTP(S)/ALL 代理变量，以及仅包含回环地址的 `NO_PROXY`。
- 官方 `ANTHROPIC_BASE_URL`、订阅登录方式 `forceLoginMethod` 和 TLS 验证选项。
- 非必要流量选项，以及用户明确配置时的 `TZ`。

原有插件、权限和其他设置保留。默认设置里发现认证令牌、其他提供商、自定义端点、`CLAUDE_CONFIG_DIR` 或 `apiKeyHelper` 时拒绝覆盖；错误只显示字段名。Desktop 中后来打开的项目、托管策略或宿主设置仍可能覆盖用户级代理配置，需在实际会话中检查。

首次配置保存 0600 完整备份和按字段恢复记录，后续 setup 保留原始备份；设置已匹配时不重写设置和记录。其他工具修改 ccg 管理的字段后，setup 和 restore 都会停止，避免覆盖外部改动。

restore 只恢复所管理字段的原值，保留后来新增的其他设置，不整文件回滚。备份和恢复记录位于 `~/.local/state/ccg`。**`CCG_CONFIG` 不隔离 Desktop 的目标设置文件**；测试需同时隔离 `HOME`。多个配置实例也共享同一个 Desktop 启动入口。

## 时区与非必要流量

```sh
ccg config set timezone Asia/Singapore
ccg config set disable_nonessential_traffic false
ccg desktop setup
# Cmd+Q 完全退出 Desktop，再通过 ccg 重开
ccg config set timezone none
ccg desktop setup
```

默认不覆盖时区。`TZ` 只传给本地进程，不修改 macOS 系统时区；各运行时未必采用它，当前官方客户端的实际时区读取仍未验证。取消覆盖时恢复 setup 前的 `TZ`，或移除 ccg 新增的值。浏览器诊断页始终显示浏览器实际读到的时区。

## 网络范围与验收

桌面 Chromium 网络通过启动参数指定 ccg，应用进程获得代理环境变量；本地 Code 会话通过用户设置获取代理。以下连接仍需单独检查：应用更新器、MCP、外部浏览器、子工具，以及应用自定网络策略。原始 Claude 图标、自动启动、协议链接也可能启动未经过 ccg 的实例。ccg 不修改系统代理或启用 PopCat TUN。

真实验收至少需要：

1. 在 Desktop 的本地 Code 会话中运行 `/status`，确认 Proxy 和设置来源。
2. 检查桌面界面、登录请求及本地 Code 请求，并结合 `ccg status`、`ccg logs` 观察连接。
3. 在没有重要任务时停止 ccg，确认新请求失败而非转为直连；重新启动服务后确认恢复。
4. 单独验证有需要的更新、MCP、外部工具与时区行为。

这些真实应用检查尚未完成。云端和 SSH 会话在远端运行，本机代理和 `TZ` 不覆盖远端。

参考：[官方 Desktop 文档](https://code.claude.com/docs/en/desktop)、[设置优先级](https://code.claude.com/docs/en/settings)。
