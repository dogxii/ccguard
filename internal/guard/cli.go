package guard

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ccguard/popcat"
)

const help = `ccg — Claude Code 启动与网络诊断工具

  ccg                         检查网络后启动官方 Claude Code
  ccg -- <Claude 参数>         传递参数（保留当前工作目录和终端）
  ccg login                   用专用 Chrome 执行官方订阅账号登录
  ccg auth status             查看独立配置的官方登录状态
  ccg browser [URL]           打开专用 Chrome，默认 claude.ai
  ccg browser doctor          打开本地浏览器诊断页
  ccg desktop                 通过 ccg 启动已安装的官方 Claude Desktop
  ccg desktop setup           备份并配置本地 Code 会话的代理，创建桌面入口
  ccg desktop doctor          检查桌面端配置和运行参数
  ccg desktop restore         撤销 ccg 写入的桌面端设置
  ccg doctor                  检查代理、官方域名出口及认证配置
  ccg init [--proxy URL]       创建配置，不覆盖已有配置
  ccg status                  查看本地连接服务
  ccg config show|path        查看配置或配置路径
  ccg config set KEY VALUE    修改代理、地区检查或隐私选项
  ccg service start|stop|restart|status|install|uninstall
  ccg popcat doctor          只读检查 PopCat 端口和已保存的路由配置
  ccg popcat export [FILE]    导出可选的 PopCat 模块（不会自动导入）
  ccg logs [--service]        查看最近的脱敏日志
  ccg version                查看 ccg 版本
  ccg uninstall              移除命令与开机启动，保留配置和登录数据

服务按需启动；service install 才启用登录系统时启动。
不解密 TLS、不修改设备指纹、不保证账号可用性；不提供系统级网络隔离。
`

func Main(args []string) int {
	if filepath.Base(os.Args[0]) == "ccg-browser" {
		c, e := loadConfig()
		if e == nil && len(args) == 1 {
			e = openBrowser(c, args[0])
		} else if e == nil {
			e = errors.New("浏览器启动器需要一个 URL")
		}
		return result(e)
	}
	if len(args) > 0 {
		switch args[0] {
		case "help", "--help", "-h":
			fmt.Print(help)
			return 0
		case "version":
			fmt.Println("ccg", Version)
			return 0
		case "init":
			return result(initConfig(args[1:]))
		}
	}
	c, e := loadConfig()
	if e != nil {
		return result(e)
	}
	if len(args) == 0 {
		return run(c, nil)
	}
	switch args[0] {
	case "_serve":
		return result(serve(c))
	case "run":
		return run(c, stripDash(args[1:]))
	case "--":
		return run(c, args[1:])
	case "login":
		if len(args) != 1 {
			return result(errors.New("用法: ccg login"))
		}
		return run(c, []string{"auth", "login", "--claudeai"})
	case "auth":
		if len(args) == 2 && args[1] == "status" {
			if e = setup(c); e != nil {
				return result(e)
			}
			fmt.Println(authSummary(c))
			return 0
		}
		return result(errors.New("用法: ccg auth status；登录使用 ccg login"))
	case "browser":
		target := "https://claude.ai/"
		if len(args) == 2 {
			target = args[1]
			if target == "doctor" {
				target = "http://" + c.Listen + "/__ccg/browser"
			}
		} else if len(args) > 2 {
			return result(errors.New("用法: ccg browser [URL|doctor]"))
		}
		if e = openBrowser(c, target); e != nil {
			return result(e)
		}
		fmt.Println("已打开 ccg 专用 Chrome。连接服务保持运行；ccg service stop 可停止。")
		return 0
	case "doctor":
		return doctor(c)
	case "desktop":
		return result(desktopCommand(c, args[1:]))
	case "status":
		return showStatus()
	case "config":
		return result(configCommand(c, args[1:]))
	case "service":
		return result(serviceCommand(c, args[1:]))
	case "logs":
		return result(logs(args[1:]))
	case "popcat":
		return result(popcatCommand(args[1:]))
	case "uninstall":
		return result(uninstall(c))
	default:
		return run(c, args)
	}
}
func result(e error) int {
	if e != nil {
		fmt.Fprintln(os.Stderr, "ccg:", e)
		return 1
	}
	return 0
}
func stripDash(a []string) []string {
	if len(a) > 0 && a[0] == "--" {
		return a[1:]
	}
	return a
}

func initConfig(args []string) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	proxy := f.String("proxy", "", "HTTP(S) 上游代理")
	country := f.String("expected-country", "", "可选的出口地区检查")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("init 不接受位置参数")
	}
	if _, e := os.Stat(paths().Config); e == nil {
		fmt.Println("配置已存在:", paths().Config)
		return nil
	}
	c := defaultConfig()
	if *proxy != "" {
		c.Upstream = *proxy
	}
	c.ExpectedCountry = *country
	if e := c.validate(); e != nil {
		return e
	}
	if e := setup(c); e != nil {
		return e
	}
	if e := writeJSON(paths().Config, c); e != nil {
		return e
	}
	fmt.Println("已创建配置:", paths().Config)
	fmt.Println("官方登录配置独立保存:", c.ClaudeDir)
	if w := legacyWarning(); w != "" {
		fmt.Println(w)
	}
	return nil
}

func run(c Config, args []string) int {
	args = stripDash(args)
	if e := validateClaudeArgs(args); e != nil {
		return result(e)
	}
	if e := checkSettings(c); e != nil {
		return result(e)
	}
	// Informational official CLI commands need no external connectivity.
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-v" || args[0] == "--help" || args[0] == "-h") {
		return child(c, args)
	}
	if e := requireReadyService(c); e != nil {
		return result(e)
	}
	fmt.Fprintln(os.Stderr, "ccg: 官方登录配置 · 本地代理已就绪 · 非必要流量关闭:", c.DisableNonessential)
	event("claude", "", "started")
	code := child(c, args)
	event("claude", "", "exited")
	return code
}

func printProbes(ps []Probe) {
	for _, p := range ps {
		if !p.Reachable {
			fmt.Printf("[失败] %s：%s\n", p.Host, p.Error)
			continue
		}
		if p.IP == "" {
			fmt.Printf("[连通] %s：HTTP %d，未取得 trace 出口信息\n", p.Host, p.Status)
			continue
		}
		fmt.Printf("[出口] %s → %s / %s\n", p.Host, p.Country, p.IP)
		if p.Country == "CN" || p.Country == "HK" || p.Country == "MO" {
			fmt.Println("       此地区不在当前 Claude 支持名单内；更改出口不改变实际所在地的使用资格。")
		}
	}
}
func doctor(c Config) int {
	fmt.Println("ccg", Version, "· 诊断")
	fmt.Println("官方客户端:", c.Claude)
	fmt.Println("独立配置:", c.ClaudeDir)
	fmt.Println("上游代理:", c.Upstream)
	fmt.Println("专用 Chrome:", c.BrowserDir)
	bad := false
	if _, e := os.Stat(c.Claude); e != nil {
		fmt.Println("[失败] Claude 可执行文件不存在")
		bad = true
	}
	if _, e := os.Stat(c.Chrome); e != nil {
		fmt.Println("[失败] Chrome 可执行文件不存在")
		bad = true
	}
	if w := legacyWarning(); w != "" {
		fmt.Println("[说明]", w)
	}
	if e := checkSettings(c); e != nil {
		fmt.Println("[失败]", e)
		bad = true
	}
	if e := setup(c); e != nil {
		return result(e)
	}
	fmt.Println("[认证]", authSummary(c))
	ps := probes(c)
	printProbes(ps)
	for _, p := range ps {
		if !p.Reachable {
			bad = true
		}
		if c.ExpectedCountry != "" && p.Country != c.ExpectedCountry {
			bad = true
			fmt.Println("[失败] 出口未通过 expected_country 检查")
		}
	}
	if c.DisableNonessential {
		fmt.Println("[隐私] 已关闭非必要流量；自动更新及部分依赖功能标志的功能可能受影响。手动更新：claude update")
	}
	if st, e := control("GET", "/status"); e == nil {
		fmt.Printf("[服务] PID %d，活动隧道 %d，就绪 %t\n", st.PID, st.Active, st.Ready)
		if st.Signature != serviceSignature(c) {
			fmt.Println("[提示] 服务仍使用旧配置；结束会话后运行 ccg service restart")
		}
	} else {
		fmt.Println("[服务] 未运行；ccg / ccg browser 会按需启动")
	}
	fmt.Println("[边界] trace 仅反映本次请求；未检测账号封禁概率。浏览器环境可用 ccg browser doctor 检查。")
	if bad {
		return 1
	}
	return 0
}
func showStatus() int {
	st, e := control("GET", "/status")
	if e != nil {
		fmt.Println("ccg 服务未运行")
		return 0
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	fmt.Println(string(b))
	return 0
}

func configCommand(c Config, args []string) error {
	if len(args) == 1 && args[0] == "path" {
		fmt.Println(paths().Config)
		return nil
	}
	if len(args) == 1 && args[0] == "show" {
		b, _ := json.MarshalIndent(c, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(args) == 3 && args[0] == "set" {
		previousService := serviceSignature(c)
		switch args[1] {
		case "upstream_proxy":
			c.Upstream = args[2]
		case "expected_country":
			c.ExpectedCountry = strings.ToUpper(args[2])
			if c.ExpectedCountry == "NONE" {
				c.ExpectedCountry = ""
			}
		case "disable_nonessential_traffic":
			v, e := strconv.ParseBool(args[2])
			if e != nil {
				return e
			}
			c.DisableNonessential = v
		case "desktop_app":
			c.Desktop = args[2]
		case "timezone":
			c.Timezone = args[2]
			if strings.EqualFold(c.Timezone, "none") {
				c.Timezone = ""
			}
		default:
			return errors.New("可修改的键: upstream_proxy、expected_country、disable_nonessential_traffic、desktop_app、timezone")
		}
		if e := c.validate(); e != nil {
			return e
		}
		if e := writeJSON(paths().Config, c); e != nil {
			return e
		}
		fmt.Println("配置已保存；新启动的客户端使用新配置。Desktop 设置需重新运行 ccg desktop setup 并完全退出后重开应用。")
		if previousService != serviceSignature(c) {
			fmt.Println("网络配置已改变；结束现有会话后运行 ccg service restart。")
		}
		return nil
	}
	return errors.New("用法: ccg config show|path|set KEY VALUE")
}
func stopService() error {
	if _, e := control("GET", "/status"); e != nil {
		return nil
	}
	_, e := control("POST", "/stop")
	if e != nil {
		return e
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := control("GET", "/status"); e != nil {
			time.Sleep(100 * time.Millisecond)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("服务没有按时停止")
}
func serviceCommand(c Config, args []string) error {
	if len(args) != 1 {
		return errors.New("用法: ccg service start|stop|restart|status|install|uninstall")
	}
	switch args[0] {
	case "start":
		st, e := ensureService(c)
		if e != nil {
			return e
		}
		fmt.Printf("服务已启动，PID %d，代理 %s\n", st.PID, st.Proxy)
		return nil
	case "stop":
		e := stopService()
		if e == nil {
			fmt.Println("连接服务已停止；专用浏览器的外部连接会失败。")
		}
		return e
	case "restart":
		if e := stopService(); e != nil {
			return e
		}
		return serviceCommand(c, []string{"start"})
	case "status":
		showStatus()
		return nil
	case "install":
		if e := launchAgent(c, "install"); e != nil {
			return e
		}
		fmt.Println("已启用 macOS 登录时启动。停止服务不会自动重启；ccg 会按需启动。")
		return nil
	case "uninstall":
		if e := launchAgent(c, "uninstall"); e != nil {
			return e
		}
		fmt.Println("已移除登录时启动项。")
		return nil
	default:
		return errors.New("未知服务操作")
	}
}
func logs(args []string) error {
	p := filepath.Join(paths().State, "events.jsonl")
	if len(args) == 1 && args[0] == "--service" {
		p = filepath.Join(paths().State, "service.log")
	} else if len(args) != 0 {
		return errors.New("用法: ccg logs [--service]")
	}
	b, e := os.ReadFile(p)
	if os.IsNotExist(e) {
		fmt.Println("暂无日志")
		return nil
	}
	if e != nil {
		return e
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}
func uninstall(c Config) error {
	h, _ := os.UserHomeDir()
	bin := filepath.Join(h, ".local/bin/ccg")
	exe, _ := os.Executable()
	if exe != bin {
		return errors.New("请运行 ~/.local/bin/ccg uninstall；源码中的构建产物不会被删除")
	}
	if e := restoreDesktop(); e != nil {
		return e
	}
	if e := removeDesktopShortcut(); e != nil {
		return e
	}
	if e := launchAgent(c, "uninstall"); e != nil {
		return e
	}
	if e := stopService(); e != nil {
		return e
	}
	helper := filepath.Join(filepath.Dir(bin), "ccg-browser")
	if dest, e := os.Readlink(helper); e == nil && (dest == bin || dest == "ccg") {
		_ = os.Remove(helper)
	}
	if e := os.Remove(bin); e != nil {
		return e
	}
	fmt.Println("命令已移除。保留配置、Claude 登录和 Chrome Cookie，可重新安装复用。")
	return nil
}

func popcatCommand(args []string) error {
	if len(args) == 1 && args[0] == "doctor" {
		h, _ := os.UserHomeDir()
		root := filepath.Join(h, "Library/Group Containers/group.com.popcat.app")
		b, e := os.ReadFile(filepath.Join(root, "configuration.json"))
		if e != nil {
			return errors.New("未找到 PopCat 已保存的内核配置")
		}
		var v struct {
			Inbounds []struct {
				Type   string `json:"type"`
				Listen string `json:"listen"`
				Port   int    `json:"listen_port"`
			} `json:"inbounds"`
			Route struct {
				Final string            `json:"final"`
				Rules []json.RawMessage `json:"rules"`
			} `json:"route"`
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return e
		}
		fmt.Println("PopCat 已保存的配置（不等于当前每条连接的实际出口）：")
		for _, i := range v.Inbounds {
			if i.Type == "mixed" || i.Type == "http" {
				address := net.JoinHostPort(i.Listen, strconv.Itoa(i.Port))
				fmt.Println("  代理入口:", address)
				conn, e := net.DialTimeout("tcp", address, time.Second)
				if e == nil {
					conn.Close()
					fmt.Println("  端口正在监听")
				} else {
					fmt.Println("  端口未响应")
				}
			}
		}
		fmt.Printf("  默认分组: %s；保存的规则数: %d\n", v.Route.Final, len(v.Route.Rules))
		pref := filepath.Join(root, "Library/Preferences/group.com.popcat.app.plist")
		if b, e := exec.Command("/usr/bin/plutil", "-extract", "selected_outbounds", "json", "-o", "-", pref).Output(); e == nil {
			var sel map[string]string
			if json.Unmarshal(b, &sel) == nil {
				for k, v := range sel {
					fmt.Printf("  保存的选择: %s → %s\n", k, v)
				}
			}
		}
		fmt.Println("实际域名出口请运行 ccg doctor。无需安装 HTTPS 解密证书；模块只负责分流。")
		return nil
	}
	if len(args) >= 1 && args[0] == "export" && len(args) <= 2 {
		p := filepath.Join(paths().Data, "ccguard.module")
		if len(args) == 2 {
			p = args[1]
		}
		if _, e := os.Stat(p); e == nil {
			return errors.New("模块文件已存在；请选择其他导出路径")
		}
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		if e := os.WriteFile(p, []byte(popcat.Template), 0600); e != nil {
			return e
		}
		fmt.Println("已导出:", p)
		fmt.Println("尚未修改 PopCat；导入后在 CCGuard 分组中选择线路。不要为此模块开启 HTTPS 解密。")
		return nil
	}
	return errors.New("用法: ccg popcat doctor|export [FILE]")
}
