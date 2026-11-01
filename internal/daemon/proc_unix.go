//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
