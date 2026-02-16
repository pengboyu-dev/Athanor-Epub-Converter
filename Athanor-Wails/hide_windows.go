//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideCmdWindow prevents a visible CMD flash on Windows when spawning
// child processes (pandoc, xelatex).
func hideCmdWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
