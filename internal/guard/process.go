package guard

import (
	"os"
	"os/exec"
	"syscall"
)

// Background apps and the proxy must survive the launching terminal and must
// not inherit its pipes. A caller can still provide a dedicated log file.
func startDetached(cmd *exec.Cmd) (int, error) {
	f, e := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	if cmd.Stdin == nil {
		cmd.Stdin = f
	}
	if cmd.Stdout == nil {
		cmd.Stdout = f
	}
	if cmd.Stderr == nil {
		cmd.Stderr = f
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		return 0, e
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}
