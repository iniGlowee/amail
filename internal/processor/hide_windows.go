//go:build windows

package processor

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps handler commands (cmd.exe, claude.exe ...) from opening
// a console window when the processor itself runs without one.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
