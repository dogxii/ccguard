package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDetachedProcessHelper(t *testing.T) {
	if os.Getenv("CCG_TEST_DETACHED") != "1" {
		return
	}
	stdin, _ := os.Stdin.Stat()
	stderr, _ := os.Stderr.Stat()
	if syscall.Getpgrp() != os.Getpid() || stdin.Mode()&os.ModeCharDevice == 0 || stderr.Mode()&os.ModeCharDevice == 0 {
		os.Exit(1)
	}
	_, _ = os.Stdout.WriteString("detached " + strconv.Itoa(os.Getpid()) + "\n")
	os.Exit(0)
}

func TestDetachedProcessKeepsLogAndDetachesTerminal(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "service.log")
	f, e := os.OpenFile(logPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedProcessHelper$")
	cmd.Env = append(os.Environ(), "CCG_TEST_DETACHED=1")
	cmd.Stdout = f
	pid, e := startDetached(cmd)
	if e != nil {
		t.Fatal(e)
	}
	want := "detached " + strconv.Itoa(pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(logPath)
		if strings.TrimSpace(string(b)) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detached process failed to preserve its log or detach from terminal")
}
