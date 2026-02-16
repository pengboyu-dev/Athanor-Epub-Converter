//go:build !windows

package main

import "os/exec"

// hideCmdWindow is a no-op on macOS and Linux.
func hideCmdWindow(cmd *exec.Cmd) {
	// Nothing to do — Unix does not spawn visible console windows.
	_ = cmd
}
