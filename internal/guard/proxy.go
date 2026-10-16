package guard

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type proxy struct {
	c         Config
	upstream  *url.URL
	transport *http.Transport
	ready     atomic.Bool
	active    atomic.Int64
	failures  atomic.Int64
	mu        sync.Mutex
	conns     map[net.Conn]bool
	closed    bool
}

func newProxy(c Config) *proxy {
	u, _ := url.Parse(c.Upstream)
	p := &proxy{c: c, upstream: u, conns: make(map[net.Conn]bool)}
	p.ready.Store(true)
	p.transport = &http.Transport{Proxy: http.ProxyURL(u), DialContext: (&net.Dialer{Timeout: time.Duration(c.Timeout) * time.Second}).DialContext, TLSHandshakeTimeout: time.Duration(c.Timeout) * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second}
	return p
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func dialUpstream(ctx context.Context, u *url.URL, target string, timeout time.Duration) (net.Conn, error) {
	address := u.Host
	if u.Port() == "" {
		p := "80"
		if u.Scheme == "https" {
			p = "443"
		}
		address = net.JoinHostPort(u.Hostname(), p)
	}
	d := net.Dialer{Timeout: timeout}
	c, e := d.DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	if u.Scheme == "https" {
		t := tls.Client(c, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if e = t.HandshakeContext(ctx); e != nil {
			c.Close()
			return nil, e
		}
		c = t
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if e = req.Write(c); e != nil {
		c.Close()
		return nil, e
	}
	b := bufio.NewReader(c)
	resp, e := http.ReadResponse(b, req)
	if e != nil {
		c.Close()
		return nil, e
	}
	if resp.StatusCode != http.StatusOK {
		c.Close()
		return nil, fmt.Errorf("上游 CONNECT 返回 %d", resp.StatusCode)
	}
	// No deadline on a live TLS tunnel: model streams can legitimately be idle.
	_ = c.SetDeadline(time.Time{})
	return &bufferedConn{c, b}, nil
}

func targetHost(address string) (string, error) {
	h, p, e := net.SplitHostPort(address)
	if e != nil {
		return "", errors.New("目标必须包含端口")
	}
	n, e := strconv.Atoi(p)
	if e != nil || n < 1 || n > 65535 {
		return "", errors.New("端口无效")
	}
	h = strings.ToLower(h)
	if len(h) == 0 || len(h) > 253 || strings.ContainsAny(h, "\r\n /\\@%") {
		return "", errors.New("主机名无效")
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return "", errors.New("回环请求应直接访问本机")
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return "", errors.New("不转发本地网络地址")
	}
	return h, nil
}

func (p *proxy) track(cs ...net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		for _, c := range cs {
			c.Close()
		}
		return false
	}
	for _, c := range cs {
		p.conns[c] = true
	}
	return true
}
func (p *proxy) untrack(cs ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range cs {
		delete(p.conns, c)
	}
}
func (p *proxy) close() {
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	p.transport.CloseIdleConnections()
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() && r.Method == http.MethodGet && r.URL.Path == "/__ccg/browser" && r.Host == p.c.Listen {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src https://api.anthropic.com https://claude.ai https://platform.claude.com; base-uri 'none'; frame-ancestors 'none'")
		io.WriteString(w, browserHTML)
		return
	}
	if !p.ready.Load() {
		http.Error(w, "ccg: 上游代理不可用；没有直连回退", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if !r.URL.IsAbs() || r.URL.Scheme != "http" || r.URL.User != nil {
		http.Error(w, "ccg: 需要 HTTP 代理请求或 HTTPS CONNECT", 400)
		return
	}
	address := r.URL.Host
	if r.URL.Port() == "" {
		address = net.JoinHostPort(r.URL.Hostname(), "80")
	}
	h, e := targetHost(address)
	if e != nil {
		http.Error(w, "ccg: 不允许的目标", 403)
		return
	}
	q := r.Clone(r.Context())
	q.RequestURI = ""
	stripHop(q.Header)
	resp, e := p.transport.RoundTrip(q)
	if e != nil {
		p.failures.Add(1)
		event("http", h, "upstream_error")
		http.Error(w, "ccg: 上游请求失败；没有直连回退", 502)
		return
	}
	defer resp.Body.Close()
	stripHop(resp.Header)
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	_ = http.NewResponseController(w).Flush()
	buf := make([]byte, 32*1024)
	for {
		n, e := resp.Body.Read(buf)
		if n > 0 {
			if _, we := w.Write(buf[:n]); we != nil {
				break
			}
			_ = http.NewResponseController(w).Flush()
		}
		if e != nil {
			break
		}
	}
	event("http", h, "complete")
}

func stripHop(h http.Header) {
	for _, s := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(s))
	}
	for _, s := range []string{"Connection", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(s)
	}
}

func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	h, e := targetHost(r.Host)
	if e != nil {
		http.Error(w, "ccg: 不允许的 CONNECT 目标", 403)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(p.c.Timeout)*time.Second)
	defer cancel()
	up, e := dialUpstream(ctx, p.upstream, r.Host, time.Duration(p.c.Timeout)*time.Second)
	if e != nil {
		p.failures.Add(1)
		event("connect", h, "upstream_error")
		http.Error(w, "ccg: 上游 CONNECT 失败；没有直连回退", 502)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "ccg: 不支持隧道", 500)
		return
	}
	client, rw, e := hj.Hijack()
	if e != nil {
		up.Close()
		return
	}
	defer client.Close()
	defer up.Close()
	if !p.track(client, up) {
		return
	}
	defer p.untrack(client, up)
	p.active.Add(1)
	defer p.active.Add(-1)
	if _, e = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
		return
	}
	if e = rw.Flush(); e != nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(up, rw); close(done); up.Close(); client.Close() }()
	_, _ = io.Copy(client, up)
	client.Close()
	up.Close()
	<-done
	event("connect", h, "closed")
}
