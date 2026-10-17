package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var probeHosts = []string{"api.anthropic.com", "claude.ai", "platform.claude.com"}

type Probe struct {
	Host      string `json:"host"`
	Reachable bool   `json:"reachable"`
	Status    int    `json:"http_status,omitempty"`
	IP        string `json:"ip,omitempty"`
	Country   string `json:"country,omitempty"`
	Error     string `json:"error,omitempty"`
}
type Status struct {
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Signature string    `json:"signature"`
	Proxy     string    `json:"proxy"`
	Upstream  string    `json:"upstream"`
	Ready     bool      `json:"ready"`
	Reason    string    `json:"reason,omitempty"`
	Checked   time.Time `json:"checked"`
	Probes    []Probe   `json:"probes"`
	Active    int64     `json:"active_tunnels"`
	Failures  int64     `json:"failed_connections"`
}

func serviceSignature(c Config) string {
	// Client paths, privacy options and TZ do not change the running proxy.
	network := struct {
		Upstream, Listen, ExpectedCountry string
		Interval, Timeout                 int
	}{c.Upstream, c.Listen, c.ExpectedCountry, c.Interval, c.Timeout}
	b, _ := json.Marshal(network)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func socketPath() string { return filepath.Join(paths().State, "control.sock") }
func control(method, path string) (Status, error) {
	return controlWithTimeout(method, path, 5*time.Second)
}

func controlWithTimeout(method, path string, timeout time.Duration) (Status, error) {
	var st Status
	cl := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath())
	}}, Timeout: timeout}
	defer cl.CloseIdleConnections()
	req, _ := http.NewRequest(method, "http://ccg"+path, nil)
	r, e := cl.Do(req)
	if e != nil {
		return st, errors.New("本地服务未运行或控制连接不可用")
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return st, fmt.Errorf("本地服务返回 %d", r.StatusCode)
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&st)
	return st, err
}

func probes(c Config) []Probe {
	u, _ := url.Parse(c.Upstream)
	tr := &http.Transport{Proxy: http.ProxyURL(u), DialContext: (&net.Dialer{Timeout: time.Duration(c.Timeout) * time.Second}).DialContext, TLSHandshakeTimeout: time.Duration(c.Timeout) * time.Second}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: time.Duration(c.Timeout) * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	out := make([]Probe, len(probeHosts))
	var wg sync.WaitGroup
	for i, h := range probeHosts {
		wg.Add(1)
		go func(i int, h string) {
			defer wg.Done()
			p := Probe{Host: h}
			r, e := cl.Get("https://" + h + "/cdn-cgi/trace")
			if e != nil {
				p.Error = "TLS/网络探测失败"
				out[i] = p
				return
			}
			defer r.Body.Close()
			p.Reachable = true
			p.Status = r.StatusCode
			if r.StatusCode == 200 {
				b, e := io.ReadAll(io.LimitReader(r.Body, 8192))
				if e == nil {
					for _, line := range strings.Split(string(b), "\n") {
						k, v, ok := strings.Cut(line, "=")
						if !ok {
							continue
						}
						if k == "ip" && net.ParseIP(v) != nil {
							p.IP = v
						}
						if k == "loc" && validCountry(v) {
							p.Country = v
						}
					}
				}
			}
			out[i] = p
		}(i, h)
	}
	wg.Wait()
	return out
}

func ensureService(c Config) (Status, error) {
	if e := setup(c); e != nil {
		return Status{}, e
	}
	if st, e := control("GET", "/status"); e == nil {
		if st.Signature != serviceSignature(c) || st.Version != Version {
			return st, errors.New("运行中的服务与当前配置/版本不同；结束现有会话后运行 ccg service restart")
		}
		return st, nil
	}
	exe, e := os.Executable()
	if e != nil {
		return Status{}, e
	}
	f, e := os.OpenFile(filepath.Join(paths().State, "service.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return Status{}, e
	}
	defer f.Close()
	cmd := exec.Command(exe, "_serve")
	cmd.Env = append(os.Environ(), "CCG_CONFIG="+paths().Config)
	cmd.Stdout = f
	cmd.Stderr = f
	if _, e = startDetached(cmd); e != nil {
		return Status{}, e
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if st, e := control("GET", "/status"); e == nil {
			if st.Signature != serviceSignature(c) || st.Version != Version {
				return st, errors.New("服务配置不一致")
			}
			return st, nil
		}
		time.Sleep(75 * time.Millisecond)
	}
	return Status{}, errors.New("服务启动失败；检查 ccg logs --service（可能是监听端口被占用）")
}

func requireReadyService(c Config) error {
	if _, e := ensureService(c); e != nil {
		return e
	}
	st, e := controlWithTimeout("POST", "/check", time.Duration(c.Timeout+5)*time.Second)
	if e != nil {
		return e
	}
	if !st.Ready {
		return fmt.Errorf("网络检查未通过：%s；运行 ccg doctor 查看详情", st.Reason)
	}
	return nil
}

func serve(c Config) error {
	if e := setup(c); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(paths().State, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("另一个 ccg 服务已经运行")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ln, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return errors.New("本地代理端口被占用")
	}
	defer ln.Close()
	_ = os.Remove(socketPath())
	ul, e := net.Listen("unix", socketPath())
	if e != nil {
		return e
	}
	defer ul.Close()
	defer os.Remove(socketPath())
	_ = os.Chmod(socketPath(), 0600)
	p := newProxy(c)
	p.ready.Store(false)
	st := Status{Version: Version, PID: os.Getpid(), Started: time.Now(), Signature: serviceSignature(c), Proxy: "http://" + c.Listen, Upstream: c.Upstream, Reason: "正在检查上游网络"}
	var mu, checkMu sync.Mutex
	check := func() {
		requested := time.Now()
		checkMu.Lock()
		defer checkMu.Unlock()
		mu.Lock()
		alreadyChecked := st.Checked.After(requested)
		mu.Unlock()
		// Requests that arrived during a check share its result instead of
		// queuing another full round of probes (including the startup check).
		if alreadyChecked {
			return
		}
		ps := probes(c)
		ready := true
		reason := ""
		for _, q := range ps {
			if !q.Reachable {
				ready = false
				reason = "必要域名探测失败"
			}
			if c.ExpectedCountry != "" && q.Country != c.ExpectedCountry {
				ready = false
				reason = "出口地区未通过 expected_country 检查"
			}
		}
		mu.Lock()
		old := st.Ready
		previous := st.Probes
		st.Probes = ps
		st.Checked = time.Now()
		st.Ready = ready
		st.Reason = reason
		mu.Unlock()
		p.ready.Store(ready)
		for _, q := range ps {
			for _, before := range previous {
				if q.Host == before.Host && q.Country != "" && before.Country != "" && q.Country != before.Country {
					event("country_changed", q.Host, before.Country+"->"+q.Country)
				}
			}
		}
		if old != ready {
			result := "blocked"
			if ready {
				result = "ready"
			}
			event("health", "", result)
		}
	}
	snapshot := func() Status {
		mu.Lock()
		defer mu.Unlock()
		s := st
		s.Active = p.active.Load()
		s.Failures = p.failures.Load()
		return s
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(snapshot())
	})
	mux.HandleFunc("POST /check", func(w http.ResponseWriter, r *http.Request) {
		check()
		_ = json.NewEncoder(w).Encode(snapshot())
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(snapshot())
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		go stop()
	})
	quiet := log.New(io.Discard, "", 0)
	ps := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32768, ErrorLog: quiet}
	cs := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 4096, ErrorLog: quiet}
	go func() { _ = ps.Serve(ln); stop() }()
	go func() { _ = cs.Serve(ul); stop() }()
	go func() {
		check()
		t := time.NewTicker(time.Duration(c.Interval) * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				check()
			}
		}
	}()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)
	event("service", "", "started")
	select {
	case <-done:
	case <-sigs:
		stop()
	}
	p.close()
	_ = ps.Close()
	_ = cs.Close()
	event("service", "", "stopped")
	return nil
}

func launchAgentPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library/LaunchAgents/io.local.ccguard.plist")
}
func xml(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}
func launchAgent(c Config, action string) error {
	p := launchAgentPath()
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	label := domain + "/io.local.ccguard"
	if action == "uninstall" {
		_ = exec.Command("launchctl", "bootout", label).Run()
		if e := os.Remove(p); e != nil && !os.IsNotExist(e) {
			return e
		}
		return nil
	}
	if action != "install" {
		return errors.New("未知 launchd 操作")
	}
	if e := setup(c); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	text := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>io.local.ccguard</string><key>ProgramArguments</key><array><string>` + xml(exe) + `</string><string>_serve</string></array><key>EnvironmentVariables</key><dict><key>CCG_CONFIG</key><string>` + xml(paths().Config) + `</string></dict><key>RunAtLoad</key><true/><key>KeepAlive</key><false/><key>StandardOutPath</key><string>` + xml(filepath.Join(paths().State, "service.log")) + `</string><key>StandardErrorPath</key><string>` + xml(filepath.Join(paths().State, "service.log")) + `</string></dict></plist>`
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	if e = os.WriteFile(p, []byte(text), 0600); e != nil {
		return e
	}
	if e = stopService(); e != nil {
		return e
	}
	_ = exec.Command("launchctl", "bootout", label).Run()
	if e = exec.Command("launchctl", "bootstrap", domain, p).Run(); e != nil {
		return errors.New("launchd 注册失败；配置文件已保存，ccg 仍支持按需启动")
	}
	return nil
}
