package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const Version = "0.2.1"

type Config struct {
	Schema              int    `json:"schema"`
	Upstream            string `json:"upstream_proxy"`
	Listen              string `json:"listen"`
	Claude              string `json:"claude_binary"`
	ClaudeDir           string `json:"claude_config_dir"`
	Chrome              string `json:"chrome_binary"`
	BrowserDir          string `json:"browser_profile_dir"`
	Desktop             string `json:"desktop_app,omitempty"`
	Timezone            string `json:"timezone,omitempty"`
	DisableNonessential bool   `json:"disable_nonessential_traffic"`
	ExpectedCountry     string `json:"expected_country"`
	Interval            int    `json:"check_interval_seconds"`
	Timeout             int    `json:"connect_timeout_seconds"`
}

type Paths struct{ Config, State, Data string }

func paths() Paths {
	h, _ := os.UserHomeDir()
	p := Paths{filepath.Join(h, ".config/ccg/config.json"), filepath.Join(h, ".local/state/ccg"), filepath.Join(h, ".local/share/ccg")}
	if v := os.Getenv("CCG_CONFIG"); v != "" {
		absolute, _ := filepath.Abs(v)
		if absolute != p.Config {
			p.Config = absolute
			p.State = filepath.Join(filepath.Dir(absolute), "state")
			p.Data = filepath.Join(filepath.Dir(absolute), "data")
		}
	}
	p.Config, _ = filepath.Abs(p.Config)
	return p
}

func defaultConfig() Config {
	p := paths()
	claude, _ := exec.LookPath("claude")
	c := Config{Schema: 1, Upstream: "http://127.0.0.1:7897", Listen: "127.0.0.1:17897", Claude: claude, ClaudeDir: filepath.Join(filepath.Dir(p.Config), "claude"), Chrome: "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", BrowserDir: filepath.Join(p.Data, "chrome"), DisableNonessential: true, Interval: 60, Timeout: 10}
	if b, e := exec.Command("/usr/sbin/scutil", "--proxy").Output(); e == nil {
		m := map[string]string{}
		for _, s := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(s, " : "); ok {
				m[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if m["HTTPSEnable"] == "1" && m["HTTPSProxy"] != "" {
			c.Upstream = "http://" + net.JoinHostPort(m["HTTPSProxy"], m["HTTPSPort"])
		}
	}
	return c
}

func (c Config) validate() error {
	if c.Schema != 1 {
		return errors.New("不支持的配置版本")
	}
	u, e := url.Parse(c.Upstream)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("upstream_proxy 必须是 HTTP(S) 代理地址；不支持 SOCKS 或带路径的地址")
	}
	if u.User != nil {
		return errors.New("此版本不保存代理密码；请使用 PopCat 的本地无密码代理端口")
	}
	h, port, e := net.SplitHostPort(c.Listen)
	if e != nil || net.ParseIP(h) == nil || !net.ParseIP(h).IsLoopback() || port == "0" {
		return errors.New("listen 必须是带固定端口的回环 IP，例如 127.0.0.1:17897")
	}
	upstreamLocal := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
		upstreamLocal = true
	}
	if u.Host == c.Listen || (upstreamLocal && u.Port() == port) {
		return errors.New("上游代理不能指向 ccg 自己")
	}
	if c.Timeout < 1 || c.Timeout > 120 || c.Interval < 10 || c.Interval > 3600 {
		return errors.New("检查间隔应为 10–3600 秒，连接超时应为 1–120 秒")
	}
	if c.ExpectedCountry != "" && (len(c.ExpectedCountry) != 2 || strings.ToUpper(c.ExpectedCountry) != c.ExpectedCountry) {
		return errors.New("expected_country 应为空或两个大写字母的国家/地区代码")
	}
	if c.Desktop != "" && (!filepath.IsAbs(c.Desktop) || filepath.Ext(c.Desktop) != ".app") {
		return errors.New("desktop_app 必须是官方 Claude.app 的绝对路径")
	}
	if c.Timezone != "" {
		if c.Timezone == "Local" || strings.ContainsAny(c.Timezone, "\\\x00\r\n") || strings.HasPrefix(c.Timezone, "/") || strings.Contains(c.Timezone, "..") {
			return errors.New("timezone 应为 IANA 时区，例如 Asia/Singapore；none 表示不覆盖")
		}
		if _, e := time.LoadLocation(c.Timezone); e != nil {
			return errors.New("timezone 不是有效的 IANA 时区")
		}
	}
	for _, s := range []string{c.Claude, c.ClaudeDir, c.Chrome, c.BrowserDir} {
		if !filepath.IsAbs(s) {
			return errors.New("二进制与数据目录必须使用绝对路径")
		}
	}
	return nil
}

func loadConfig() (Config, error) {
	var c Config
	b, e := os.ReadFile(paths().Config)
	if e != nil {
		return c, fmt.Errorf("读取配置失败；先运行 ccg init: %w", e)
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, fmt.Errorf("配置 JSON 无效: %w", e)
	}
	return c, c.validate()
}

func writeJSON(path string, v any) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	b = append(b, '\n')
	f, e := os.CreateTemp(filepath.Dir(path), ".ccg-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if e = f.Chmod(0600); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}

func setup(c Config) error {
	for _, p := range []string{paths().State, paths().Data, c.ClaudeDir, c.BrowserDir} {
		if e := os.MkdirAll(p, 0700); e != nil {
			return e
		}
	}
	return nil
}

var logMu sync.Mutex

func event(kind, host, result string) {
	logMu.Lock()
	defer logMu.Unlock()
	p := filepath.Join(paths().State, "events.jsonl")
	if st, e := os.Stat(p); e == nil && st.Size() > 1024*1024 {
		_ = os.Rename(p, p+".1")
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return
	}
	defer f.Close()
	// Only structured metadata: never URLs, request headers/bodies, or arbitrary errors.
	_ = json.NewEncoder(f).Encode(map[string]any{"time": time.Now().Format(time.RFC3339), "event": kind, "host": host, "result": result})
}
