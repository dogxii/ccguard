package guard

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	d, e := os.MkdirTemp("/tmp", "ccg-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	t.Setenv("CCG_CONFIG", filepath.Join(d, "config.json"))
	c := defaultConfig()
	c.Timeout = 1
	c.Interval = 10
	return c
}

func TestCredentialAndProxyIsolation(t *testing.T) {
	c := testConfig(t)
	env := environment(c, []string{"PATH=/usr/bin", "ANTHROPIC_AUTH_TOKEN=SECRET", "ANTHROPIC_API_KEY=SECRET", "ANTHROPIC_BASE_URL=https://unwanted.example", "https_proxy=http://unwanted.example", "NO_PROXY=*", "CLAUDE_CONFIG_DIR=/old", "NODE_TLS_REJECT_UNAUTHORIZED=0"}, c.ClaudeDir)
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"SECRET", "unwanted.example", "NO_PROXY=*", "CLAUDE_CONFIG_DIR=/old", "NODE_TLS_REJECT_UNAUTHORIZED=0"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("unsafe inherited value: %s", bad)
		}
	}
	for _, want := range []string{"https_proxy=http://" + c.Listen, "HTTPS_PROXY=http://" + c.Listen, "CLAUDE_CONFIG_DIR=" + c.ClaudeDir, "ANTHROPIC_BASE_URL=https://api.anthropic.com"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if e := validateClaudeArgs([]string{"--settings=/tmp/unsafe"}); e == nil {
		t.Fatal("settings override accepted")
	}
	if e := validateClaudeArgs([]string{"--resume", "session", "--model", "sonnet"}); e != nil {
		t.Fatal(e)
	}
}

func TestConfigRejectsExposedListenerAndPassword(t *testing.T) {
	c := testConfig(t)
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	c.Listen = "0.0.0.0:17897"
	if c.validate() == nil {
		t.Fatal("LAN listener allowed")
	}
	c.Listen = "127.0.0.1:17897"
	c.Upstream = "http://user:password@localhost:7897"
	if c.validate() == nil {
		t.Fatal("saved password allowed")
	}
	c.Upstream = "socks5://127.0.0.1:7897"
	if c.validate() == nil {
		t.Fatal("unsupported proxy allowed")
	}
	c.Upstream = "http://localhost:17897"
	if c.validate() == nil {
		t.Fatal("self proxy loop allowed")
	}
}

func TestDefaultConfigPathKeepsSameStateAcrossBackgroundProcess(t *testing.T) {
	t.Setenv("CCG_CONFIG", "")
	before := paths()
	t.Setenv("CCG_CONFIG", before.Config)
	after := paths()
	if before != after {
		t.Fatalf("explicit default path changes runtime directories: before=%+v after=%+v", before, after)
	}
}

func echoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "example.test:443" {
			http.Error(w, "bad target", 400)
			return
		}
		c, b, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer c.Close()
		fmt.Fprint(b, "HTTP/1.1 200 Connection Established\r\n\r\n")
		b.Flush()
		_, _ = io.Copy(c, b)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestConnectPreservesBufferedBytesAndClosesTunnels(t *testing.T) {
	c := testConfig(t)
	up := echoUpstream(t)
	c.Upstream = up.URL
	p := newProxy(c)
	s := httptest.NewServer(p)
	defer s.Close()
	defer p.close()
	u, _ := url.Parse(s.URL)
	conn, e := net.Dial("tcp", u.Host)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(conn, "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\n\r\nhello")
	r := bufio.NewReader(conn)
	resp, e := http.ReadResponse(r, &http.Request{Method: "CONNECT"})
	if e != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %v", e)
	}
	b := make([]byte, 5)
	if _, e = io.ReadFull(r, b); e != nil || string(b) != "hello" {
		t.Fatalf("buffered bytes lost: %q %v", b, e)
	}
	p.close()
	if _, e = r.ReadByte(); e == nil {
		t.Fatal("tunnel stayed open after shutdown")
	}
}

func TestConnectFailureDoesNotBecomeDirect(t *testing.T) {
	c := testConfig(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "denied", 407) }))
	defer up.Close()
	c.Upstream = up.URL
	p := newProxy(c)
	s := httptest.NewServer(p)
	defer s.Close()
	defer p.close()
	u, _ := url.Parse(s.URL)
	conn, e := net.Dial("tcp", u.Host)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(conn, "CONNECT example.test:443 HTTP/1.1\r\nHost: example.test:443\r\n\r\n")
	r, e := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if r.StatusCode != 502 {
		t.Fatalf("want 502, got %d", r.StatusCode)
	}
	if p.failures.Load() != 1 {
		t.Fatal("failure not counted")
	}
}

func TestHTTPSUpstreamRequiresValidCertificate(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	if c, e := dialUpstream(context.Background(), u, "example.test:443", time.Second); e == nil {
		c.Close()
		t.Fatal("untrusted TLS upstream accepted")
	}
}

func TestHTTPStreamingAndHeaderPrivacy(t *testing.T) {
	c := testConfig(t)
	release := make(chan struct{})
	seen := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Proxy-Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "last\n")
	}))
	defer func() { close(release); up.Close() }()
	c.Upstream = up.URL
	p := newProxy(c)
	s := httptest.NewServer(p)
	defer s.Close()
	defer p.close()
	u, _ := url.Parse(s.URL)
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	cl := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", "http://example.test/events", nil)
	req.Header.Set("Proxy-Authorization", "should-not-reach-upstream")
	r, e := cl.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	line, e := bufio.NewReader(r.Body).ReadString('\n')
	if e != nil || line != "first\n" {
		t.Fatalf("stream was buffered: %q %v", line, e)
	}
	if h := <-seen; h != "" {
		t.Fatal("proxy auth leaked")
	}
}

func TestBrowserUsesSeparateProfileWithoutSecurityBypass(t *testing.T) {
	c := testConfig(t)
	args := strings.Join(browserArgs(c, "https://claude.ai/"), "\n")
	for _, want := range []string{"--user-data-dir=" + c.BrowserDir, "--proxy-server=http://" + c.Listen, "--disable-quic"} {
		if !strings.Contains(args, want) {
			t.Fatal(want)
		}
	}
	for _, bad := range []string{"remote-debugging", "ignore-certificate", "disable-web-security", "no-sandbox"} {
		if strings.Contains(args, bad) {
			t.Fatal(bad)
		}
	}
}

func TestDaemonLifecycleAndBlockedHealth(t *testing.T) {
	c := testConfig(t)
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	c.Listen = ln.Addr().String()
	ln.Close()
	// An upstream that is definitely down. No external network is used.
	c.Upstream = "http://127.0.0.1:1"
	done := make(chan error, 1)
	go func() { done <- serve(c) }()
	defer func() {
		_, _ = control("POST", "/stop")
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("daemon leaked")
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, e := control("GET", "/status"); e == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	st, e := control("POST", "/check")
	if e != nil {
		t.Fatal(e)
	}
	if st.Ready || len(st.Probes) != 3 {
		t.Fatalf("incorrect status: %+v", st)
	}
	// Client-only options must reuse the same proxy, even when health is blocked.
	client := c
	client.Timezone = "Asia/Tokyo"
	client.Desktop = "/tmp/Claude.app"
	client.Chrome = "/tmp/chrome"
	client.Claude = "/tmp/claude"
	client.ClaudeDir = filepath.Join(filepath.Dir(paths().Config), "another-claude")
	client.BrowserDir = filepath.Join(filepath.Dir(paths().Config), "another-browser")
	client.DisableNonessential = !c.DisableNonessential
	reused, e := ensureService(client)
	if e != nil || reused.PID != st.PID {
		t.Fatalf("client options should not restart proxy: %v", e)
	}
	network := c
	network.ExpectedCountry = "JP"
	if _, e := ensureService(network); e == nil {
		t.Fatal("network change silently reused stale proxy")
	}
	if requireReadyService(c) == nil {
		t.Fatal("blocked service accepted by launcher")
	}
	fi, e := os.Stat(socketPath())
	if e != nil {
		t.Fatal(e)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatal("control socket is exposed")
	}
	if _, e = control("POST", "/stop"); e != nil {
		t.Fatal(e)
	}
}
