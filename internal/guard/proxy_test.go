package guard

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHTTPStripsEveryConnectionHeader(t *testing.T) {
	c := testConfig(t)
	seen := make(chan http.Header, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.Header().Add("Connection", "X-First")
		w.Header().Add("Connection", "X-Second")
		w.Header().Set("X-First", "response secret")
		w.Header().Set("X-Second", "response secret")
		w.Header().Set("X-End-To-End", "keep")
		fmt.Fprint(w, "ok")
	}))
	defer up.Close()
	c.Upstream = up.URL
	p := newProxy(c)
	defer p.close()
	s := httptest.NewServer(p)
	defer s.Close()
	u, _ := url.Parse(s.URL)
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", "http://example.test/", nil)
	req.Header.Add("Connection", "X-First")
	req.Header.Add("Connection", "X-Second")
	req.Header.Set("X-First", "request secret")
	req.Header.Set("X-Second", "request secret")
	req.Header.Set("X-End-To-End", "keep")
	resp, e := client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	for _, headers := range []http.Header{<-seen, resp.Header} {
		if headers.Get("X-First") != "" || headers.Get("X-Second") != "" {
			t.Fatal("connection-specific header forwarded")
		}
		if headers.Get("X-End-To-End") != "keep" {
			t.Fatal("end-to-end header removed")
		}
	}
}

func TestHTTPTruncatedStreamRemainsAnError(t *testing.T) {
	c := testConfig(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(rw, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n")
		rw.Flush() // Close without the terminating chunk, simulating a lost upstream.
	}))
	defer up.Close()
	c.Upstream = up.URL
	p := newProxy(c)
	defer p.close()
	s := httptest.NewServer(p)
	defer s.Close()
	u, _ := url.Parse(s.URL)
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	resp, e := client.Get("http://example.test/stream")
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(resp.Body)
	if string(body) != "hello" || e == nil {
		t.Fatalf("truncated response appeared complete: %q, %v", body, e)
	}
	if p.failures.Load() != 1 {
		t.Fatal("upstream stream failure was not counted")
	}
}
