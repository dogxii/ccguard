package guard

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestControlRequestHonorsItsTimeout(t *testing.T) {
	c := testConfig(t)
	if e := setup(c); e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("unix", socketPath())
	if e != nil {
		t.Fatal(e)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(50 * time.Millisecond):
			json.NewEncoder(w).Encode(Status{Version: Version, Ready: true})
		}
	})}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	defer func() {
		srv.Close()
		<-done
	}()
	if _, e = controlWithTimeout("POST", "/check", time.Millisecond); e == nil {
		t.Fatal("control request ignored timeout")
	}
	st, e := controlWithTimeout("POST", "/check", time.Second)
	if e != nil || !st.Ready {
		t.Fatalf("control request failed with sufficient timeout: %+v, %v", st, e)
	}
}
