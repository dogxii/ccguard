package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var authVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "CLAUDE_CODE_OAUTH_SCOPES", "ANTHROPIC_PROFILE", "ANTHROPIC_CONFIG_DIR", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_FEDERATION_ROLE", "ANTHROPIC_FEDERATION_PROVIDER", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"}

func environment(c Config, base []string, claudeDir string) []string {
	m := map[string]string{}
	for _, s := range base {
		if k, v, ok := strings.Cut(s, "="); ok {
			m[k] = v
		}
	}
	delete(m, "CLAUDE_CONFIG_DIR")
	// Protected values replace inherited credentials, proxy bypasses and TLS
	// overrides directly; deleting them before the overwrite adds no protection.
	for k, v := range protectedEnv(c) {
		m[k] = v
	}
	if claudeDir != "" {
		m["CLAUDE_CONFIG_DIR"] = claudeDir
	}
	m["CCG_CONFIG"] = paths().Config
	exe, _ := os.Executable()
	m["BROWSER"] = filepath.Join(filepath.Dir(exe), "ccg-browser")
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func protectedEnv(c Config) map[string]string {
	m := map[string]string{}
	for _, k := range authVars {
		m[k] = ""
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy"} {
		m[k] = "http://" + c.Listen
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		m[k] = "localhost,127.0.0.1,::1"
	}
	m["ANTHROPIC_BASE_URL"] = "https://api.anthropic.com"
	m["NODE_TLS_REJECT_UNAUTHORIZED"] = "1"
	if c.Timezone != "" {
		m["TZ"] = c.Timezone
	}
	if c.DisableNonessential {
		m["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
	} else {
		m["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = ""
	}
	return m
}

func guardSettings(c Config) string {
	b, _ := json.Marshal(map[string]any{"env": protectedEnv(c), "forceLoginMethod": "claudeai"})
	return string(b)
}

func settingsFiles(c Config) []string {
	out := []string{filepath.Join(c.ClaudeDir, "settings.json"), "/Library/Application Support/ClaudeCode/managed-settings.json"}
	d, _ := os.Getwd()
	for {
		out = append(out, filepath.Join(d, ".claude/settings.json"), filepath.Join(d, ".claude/settings.local.json"))
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return out
}

func checkSettings(c Config) error {
	for _, p := range settingsFiles(c) {
		b, e := os.ReadFile(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return fmt.Errorf("无法检查设置文件 %s", p)
		}
		var v map[string]any
		if json.Unmarshal(b, &v) != nil {
			return fmt.Errorf("设置文件不是有效 JSON: %s", p)
		}
		if s, ok := v["apiKeyHelper"]; ok && s != "" && s != nil {
			return fmt.Errorf("官方登录配置检测到 apiKeyHelper: %s；请在该文件中移除或改用原始 claude", p)
		}
		if strings.Contains(p, "/ClaudeCode/managed-settings") {
			if env, ok := v["env"].(map[string]any); ok {
				for k, want := range protectedEnv(c) {
					if got, exists := env[k]; exists && got != want {
						return fmt.Errorf("受管理设置覆盖了 %s；ccg 无法覆盖组织策略", k)
					}
				}
			}
			if s, ok := v["forceLoginMethod"].(string); ok && s != "" && s != "claudeai" {
				return errors.New("组织策略要求其他登录方式")
			}
		}
	}
	return nil
}

func validateClaudeArgs(args []string) error {
	for _, a := range args {
		for _, f := range []string{"--settings", "--setting-sources", "--bare"} {
			if a == f || strings.HasPrefix(a, f+"=") {
				return fmt.Errorf("%s 与 ccg 的官方登录配置隔离冲突；其他 Claude 参数可原样传入", f)
			}
		}
	}
	return nil
}

func child(c Config, args []string) int {
	// run validates settings and arguments once, before any network activity.
	full := append([]string{"--settings", guardSettings(c)}, args...)
	cmd := exec.Command(c.Claude, full...)
	cmd.Env = environment(c, os.Environ(), c.ClaudeDir)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Share the foreground process group and inherit the actual terminal. Terminal
	// Ctrl+C reaches both processes; do not forward it twice. Non-terminal TERM/HUP
	// received by the supervisor are forwarded explicitly.
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)
	if e := cmd.Start(); e != nil {
		fmt.Fprintln(os.Stderr, "ccg: 无法启动 Claude Code")
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case s := <-sigs:
			if s != syscall.SIGINT {
				_ = cmd.Process.Signal(s)
			}
		case e := <-done:
			if e == nil {
				return 0
			}
			var x *exec.ExitError
			if errors.As(e, &x) {
				if st, ok := x.Sys().(syscall.WaitStatus); ok && st.Signaled() {
					return 128 + int(st.Signal())
				}
				return x.ExitCode()
			}
			return 1
		}
	}
}

func legacyWarning() string {
	h, _ := os.UserHomeDir()
	b, e := os.ReadFile(filepath.Join(h, ".claude/settings.json"))
	if e != nil {
		return ""
	}
	var v struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		if v.Env[k] != "" {
			return "原 ~/.claude 中存在认证/端点配置；ccg 使用独立目录，不导入这些值"
		}
	}
	if base := v.Env["ANTHROPIC_BASE_URL"]; base != "" && strings.TrimRight(base, "/") != "https://api.anthropic.com" {
		return "原 ~/.claude 中存在自定义端点；ccg 使用独立目录，不导入这些值"
	}
	return ""
}

func authSummary(c Config) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Claude, "--settings", guardSettings(c), "auth", "status", "--json")
	cmd.Env = environment(c, os.Environ(), c.ClaudeDir)
	b, _ := cmd.Output()
	var v struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "未能读取官方认证状态（不代表账号异常）"
	}
	if !v.LoggedIn {
		return "独立配置尚未登录；运行 ccg login"
	}
	return "已登录（官方客户端报告；不显示邮箱或组织信息）"
}
