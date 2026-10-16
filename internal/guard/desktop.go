package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Only fields owned by ccg are restored. A whole-file rollback could revive a
// removed credential or erase unrelated settings added after setup.
type desktopPatch struct {
	BeforePresent bool            `json:"before_present"`
	Before        json.RawMessage `json:"before"`
	Applied       json.RawMessage `json:"applied"`
}

func (p desktopPatch) original() json.RawMessage {
	if !p.BeforePresent {
		return nil
	}
	return p.Before
}

type desktopState struct {
	Settings string                  `json:"settings"`
	Backup   string                  `json:"backup"`
	Fields   map[string]desktopPatch `json:"fields"`
}

const desktopShortcutPlist = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>io.local.ccguard.desktop</string><key>CFBundleName</key><string>Claude via ccg</string><key>CFBundleExecutable</key><string>ccg-launch</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`

type desktopSettings struct {
	Root map[string]json.RawMessage
	Env  map[string]json.RawMessage
	Raw  []byte
}

func sharedSettingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude/settings.json")
}

func desktopStatePath() string { return filepath.Join(paths().State, "desktop-settings.json") }

func readDesktopSettings(path string) (desktopSettings, error) {
	s := desktopSettings{Root: map[string]json.RawMessage{}, Env: map[string]json.RawMessage{}}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	s.Raw = b
	if json.Unmarshal(b, &s.Root) != nil || s.Root == nil {
		return s, errors.New("默认 settings.json 不是 JSON 对象；未修改")
	}
	if env, ok := s.Root["env"]; ok {
		if json.Unmarshal(env, &s.Env) != nil || s.Env == nil {
			return s, errors.New("settings.json 的 env 不是 JSON 对象；未修改")
		}
	}
	return s, nil
}

func (s desktopSettings) get(key string) json.RawMessage {
	if strings.HasPrefix(key, "env.") {
		return s.Env[strings.TrimPrefix(key, "env.")]
	}
	return s.Root[key]
}

func (s desktopSettings) set(key string, value json.RawMessage) {
	m := s.Root
	if strings.HasPrefix(key, "env.") {
		m, key = s.Env, strings.TrimPrefix(key, "env.")
	}
	if value == nil {
		delete(m, key)
	} else {
		m[key] = value
	}
}

func sameJSON(a, b json.RawMessage) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func rawString(s string) json.RawMessage { b, _ := json.Marshal(s); return b }

func rawNonempty(v json.RawMessage) bool {
	return v != nil && !sameJSON(v, rawString("")) && !sameJSON(v, json.RawMessage("null"))
}

func desktopConflicts(s desktopSettings) []string {
	var out []string
	for _, k := range authVars {
		if rawNonempty(s.Env[k]) {
			out = append(out, "env."+k)
		}
	}
	if v := s.Env["ANTHROPIC_BASE_URL"]; rawNonempty(v) && !sameJSON(v, rawString("https://api.anthropic.com")) {
		out = append(out, "env.ANTHROPIC_BASE_URL")
	}
	if rawNonempty(s.Root["apiKeyHelper"]) {
		out = append(out, "apiKeyHelper")
	}
	if rawNonempty(s.Env["CLAUDE_CONFIG_DIR"]) {
		out = append(out, "env.CLAUDE_CONFIG_DIR")
	}
	return out
}

func desktopDesired(c Config) map[string]json.RawMessage {
	out := map[string]json.RawMessage{"forceLoginMethod": rawString("claudeai")}
	env := protectedEnv(c)
	for _, key := range authVars {
		delete(env, key)
	}
	for key, value := range env {
		out["env."+key] = rawString(value)
	}
	return out
}

func loadDesktopState() (desktopState, bool, error) {
	var state desktopState
	b, e := os.ReadFile(desktopStatePath())
	if os.IsNotExist(e) {
		return state, false, nil
	}
	if e != nil {
		return state, false, e
	}
	if json.Unmarshal(b, &state) != nil || state.Settings != sharedSettingsPath() || len(state.Fields) == 0 {
		return state, true, errors.New("Desktop 配置记录无效；未修改 settings.json")
	}
	return state, true, nil
}

func backupDesktopSettings(s desktopSettings) (string, error) {
	dir := filepath.Join(paths().State, "backups")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return "", e
	}
	f, e := os.CreateTemp(dir, "settings-before-desktop-*.json")
	if e != nil {
		return "", e
	}
	defer f.Close()
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(s.Raw)
	}
	return f.Name(), e
}

func saveDesktopSettings(path string, s desktopSettings) error {
	current, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if !bytes.Equal(current, s.Raw) {
		return errors.New("settings.json 在检查后发生变化；为保留其他修改，写入已取消")
	}
	if len(s.Env) == 0 {
		delete(s.Root, "env")
	} else {
		b, _ := json.Marshal(s.Env)
		s.Root["env"] = b
	}
	return writeJSON(path, s.Root)
}

func configureDesktop(c Config) error {
	s, e := readDesktopSettings(sharedSettingsPath())
	if e != nil {
		return e
	}
	if conflicts := desktopConflicts(s); len(conflicts) != 0 {
		return fmt.Errorf("默认配置含认证或端点覆盖项：%s；未覆盖这些值", strings.Join(conflicts, ", "))
	}
	check := c
	check.ClaudeDir = filepath.Dir(sharedSettingsPath())
	if e = checkSettings(check); e != nil {
		return e
	}
	state, exists, e := loadDesktopState()
	if e != nil {
		return e
	}
	oldState := state
	desired := desktopDesired(c)
	if exists {
		unchanged := len(state.Fields) == len(desired)
		for k, p := range state.Fields {
			if !sameJSON(s.get(k), p.Applied) {
				return fmt.Errorf("%s 已被其他工具修改；未覆盖。请运行 ccg desktop doctor", k)
			}
			unchanged = unchanged && sameJSON(p.Applied, desired[k])
		}
		if unchanged {
			fmt.Println("Desktop 设置已匹配，无需写入。")
			return nil
		}
	} else {
		backup, e := backupDesktopSettings(s)
		if e != nil {
			return e
		}
		state = desktopState{Settings: sharedSettingsPath(), Backup: backup, Fields: map[string]desktopPatch{}}
	}
	// Copy the map so rollback metadata remains intact when config changes.
	state.Fields = maps.Clone(state.Fields)
	for k, p := range state.Fields {
		if _, ok := desired[k]; !ok {
			s.set(k, p.original())
			delete(state.Fields, k)
		}
	}
	for k, v := range desired {
		p, ok := state.Fields[k]
		if !ok {
			p.BeforePresent = s.get(k) != nil
			p.Before = s.get(k)
		}
		p.Applied = v
		state.Fields[k] = p
		s.set(k, v)
	}
	// Save recovery metadata before settings. A crash leaves a visible conflict,
	// rather than allowing the next invocation to overwrite an unknown value.
	if e = writeJSON(desktopStatePath(), state); e != nil {
		return e
	}
	if e = saveDesktopSettings(state.Settings, s); e != nil {
		if exists {
			_ = writeJSON(desktopStatePath(), oldState)
		} else {
			_ = os.Remove(desktopStatePath())
		}
		return e
	}
	fmt.Println("已配置默认 Claude Code 设置；其他 CLI 也会使用此代理。备份:", state.Backup)
	return nil
}

func restoreDesktop() error {
	state, exists, e := loadDesktopState()
	if e != nil || !exists {
		return e
	}
	s, e := readDesktopSettings(state.Settings)
	if e != nil {
		return e
	}
	for k, p := range state.Fields {
		if !sameJSON(s.get(k), p.Applied) {
			return fmt.Errorf("%s 已被其他工具修改；自动撤销已取消，以免覆盖改动", k)
		}
	}
	for k, p := range state.Fields {
		s.set(k, p.original())
	}
	if e = saveDesktopSettings(state.Settings, s); e != nil {
		return e
	}
	if e = os.Remove(desktopStatePath()); e != nil {
		return e
	}
	fmt.Println("已撤销 ccg 的 Desktop 设置，保留其他修改；没有恢复旧中转凭据。")
	return nil
}

func desktopApp(c Config) string {
	if c.Desktop != "" {
		return c.Desktop
	}
	h, _ := os.UserHomeDir()
	for _, p := range []string{"/Applications/Claude.app", filepath.Join(h, "Applications/Claude.app")} {
		if _, e := os.Stat(filepath.Join(p, "Contents/MacOS/Claude")); e == nil {
			return p
		}
	}
	return ""
}

func desktopBinary(app string) string { return filepath.Join(app, "Contents/MacOS/Claude") }

func desktopArgs(c Config) []string {
	return []string{"--proxy-server=http://" + c.Listen, "--proxy-bypass-list=localhost;127.0.0.1;[::1]", "--disable-quic"}
}

type desktopProcess struct {
	PID  int
	Args string
}

func desktopProcesses(app string) ([]desktopProcess, error) {
	b, e := exec.Command("/bin/ps", "-axo", "pid=,comm=").Output()
	if e != nil {
		return nil, errors.New("无法检查 Desktop 进程；未启动第二个实例")
	}
	var out []desktopProcess
	for _, row := range strings.Split(string(b), "\n") {
		row = strings.TrimSpace(row)
		i := strings.IndexAny(row, " \t")
		if i < 1 || strings.TrimSpace(row[i:]) != desktopBinary(app) {
			continue
		}
		pid, e := strconv.Atoi(row[:i])
		if e != nil {
			continue
		}
		args, e := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
		if e != nil {
			return nil, errors.New("Desktop 在检查时退出；请重试")
		}
		out = append(out, desktopProcess{pid, strings.TrimSpace(string(args))})
	}
	return out, nil
}

func checkOfficialDesktop(app string) error {
	b, e := exec.Command("/usr/bin/plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", filepath.Join(app, "Contents/Info.plist")).Output()
	if e != nil || strings.TrimSpace(string(b)) != "com.anthropic.claudefordesktop" {
		return errors.New("未识别为官方 Claude Desktop 应用；请从 https://claude.com/download 安装")
	}
	if exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", app).Run() != nil {
		return errors.New("Claude Desktop 签名校验失败；未启动")
	}
	if exec.Command("/usr/sbin/spctl", "--assess", "--type", "execute", app).Run() != nil {
		return errors.New("Claude Desktop 未通过 macOS Gatekeeper 检查；未绕过系统保护")
	}
	return nil
}

func startDesktop(c Config) error {
	app := desktopApp(c)
	if app == "" {
		return errors.New("未找到官方 Claude Desktop；从 https://claude.com/download 安装后运行 ccg desktop")
	}
	if e := checkOfficialDesktop(app); e != nil {
		return e
	}
	ps, e := desktopProcesses(app)
	if e != nil {
		return e
	}
	if len(ps) != 0 {
		return errors.New("Claude Desktop 已在运行；请用 Cmd+Q 完全退出，再运行 ccg desktop（仅关闭窗口不够）")
	}
	if e = configureDesktop(c); e != nil {
		return e
	}
	if e = requireReadyService(c); e != nil {
		return e
	}
	cmd := exec.Command(desktopBinary(app), desktopArgs(c)...)
	cmd.Env = environment(c, os.Environ(), "")
	h, _ := os.UserHomeDir()
	cmd.Dir = h
	pid, e := startDetached(cmd)
	if e != nil {
		return errors.New("Desktop 启动失败；运行 ccg desktop doctor 检查")
	}
	event("desktop", "", "launched")
	fmt.Printf("已启动官方 Claude Desktop（PID %d）；请在应用内自行登录。\n", pid)
	fmt.Println("已传入本地代理参数；实际覆盖情况需结合 ccg desktop doctor 和应用行为验证。")
	return nil
}

func desktopDoctor(c Config) error {
	fmt.Println("ccg", Version, "· Desktop 诊断")
	bad := false
	app := desktopApp(c)
	if app == "" {
		fmt.Println("[待安装] 未检测到官方 Claude Desktop：https://claude.com/download")
		bad = true
	} else {
		fmt.Println("[应用]", app)
		if e := checkOfficialDesktop(app); e != nil {
			fmt.Println("[失败]", e)
			bad = true
		}
		ps, e := desktopProcesses(app)
		if e != nil {
			return e
		}
		if len(ps) == 0 {
			fmt.Println("[进程] 未运行；使用 ccg desktop 或 Claude via ccg 启动")
		}
		for _, p := range ps {
			ok := true
			for _, a := range desktopArgs(c) {
				ok = ok && slices.Contains(strings.Fields(p.Args), a)
			}
			fmt.Printf("[进程] PID %d，启动参数包含预期代理选项：%t（不证明所有请求均经过代理）\n", p.PID, ok)
			bad = bad || !ok
		}
	}
	s, e := readDesktopSettings(sharedSettingsPath())
	if e != nil {
		return e
	}
	for _, k := range desktopConflicts(s) {
		fmt.Println("[冲突]", k, "存在覆盖值（内容不显示）")
		bad = true
	}
	state, exists, e := loadDesktopState()
	if e != nil {
		return e
	}
	if !exists {
		fmt.Println("[待配置] 运行 ccg desktop setup")
		bad = true
	} else {
		desired := desktopDesired(c)
		for k, v := range desired {
			p, owned := state.Fields[k]
			if !owned || !sameJSON(p.Applied, v) {
				fmt.Println("[待更新]", k, "未匹配 ccg 配置；重新运行 setup")
				bad = true
			}
		}
		for k, p := range state.Fields {
			if !sameJSON(s.get(k), p.Applied) {
				fmt.Println("[外部修改]", k)
				bad = true
			}
			if _, ok := desired[k]; !ok {
				fmt.Println("[待更新]", k, "已不在期望配置中；重新运行 setup")
				bad = true
			}
		}
		fmt.Println("[设置]", sharedSettingsPath())
		fmt.Println("[备份]", state.Backup)
	}
	check := c
	check.ClaudeDir = filepath.Dir(sharedSettingsPath())
	if e = checkSettings(check); e != nil {
		fmt.Println("[冲突]", e)
		bad = true
	}
	if st, e := control("GET", "/status"); e == nil {
		fmt.Printf("[代理] %s，就绪 %t，版本 %s\n", st.Proxy, st.Ready, st.Version)
		bad = bad || st.Version != Version || st.Signature != serviceSignature(c) || !st.Ready
	} else {
		fmt.Println("[代理] 未运行；ccg desktop 会按需启动")
	}
	if c.Timezone == "" {
		fmt.Println("[时区] ccg 未覆盖；应用可能使用系统或其他配置中的时区")
	} else {
		fmt.Println("[时区] 本地会话配置 TZ=" + c.Timezone + "；Desktop 页面时区尚未验证，不修改系统时区")
	}
	fmt.Println("[验证] 登录后在本地 Code 会话运行 /status 检查 Proxy；云端及 SSH 会话使用远端网络和时区。")
	fmt.Println("[边界] 不覆盖整个系统、外部工具或全部桌面请求；配置检测不是账号安全评分。")
	if bad {
		return errors.New("Desktop 仍有待安装、配置或验证的项目（见上方）")
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func desktopShortcut() error {
	h, _ := os.UserHomeDir()
	app := filepath.Join(h, "Applications/Claude via ccg.app")
	info := filepath.Join(app, "Contents/Info.plist")
	if _, e := os.Stat(app); e == nil {
		b, e := os.ReadFile(info)
		if e != nil || string(b) != desktopShortcutPlist {
			return errors.New("Claude via ccg.app 已存在且不属于此工具；未覆盖")
		}
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	dir := filepath.Join(app, "Contents/MacOS")
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e = os.WriteFile(info, []byte(desktopShortcutPlist), 0600); e != nil {
		return e
	}
	script := "#!/bin/sh\nif ! CCG_CONFIG=" + shellQuote(paths().Config) + " " + shellQuote(exe) + " desktop; then\n/usr/bin/osascript -e 'display alert \"ccg\" message \"启动失败。请在终端运行 ccg desktop doctor 查看原因。\" as warning'\nfi\n"
	if e = os.WriteFile(filepath.Join(dir, "ccg-launch"), []byte(script), 0700); e != nil {
		return e
	}
	fmt.Println("已创建启动入口:", app)
	return nil
}

func removeDesktopShortcut() error {
	h, _ := os.UserHomeDir()
	app := filepath.Join(h, "Applications/Claude via ccg.app")
	b, e := os.ReadFile(filepath.Join(app, "Contents/Info.plist"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil || string(b) != desktopShortcutPlist {
		return errors.New("桌面入口已被修改；保留该应用，请手动检查")
	}
	return os.RemoveAll(app)
}

func desktopCommand(c Config, args []string) error {
	if len(args) == 0 {
		return startDesktop(c)
	}
	if len(args) == 1 {
		switch args[0] {
		case "setup":
			if e := configureDesktop(c); e != nil {
				return e
			}
			return desktopShortcut()
		case "doctor":
			return desktopDoctor(c)
		case "restore":
			if e := restoreDesktop(); e != nil {
				return e
			}
			return removeDesktopShortcut()
		}
	}
	return errors.New("用法: ccg desktop [setup|doctor|restore]")
}
