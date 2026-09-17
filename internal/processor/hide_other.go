//go:build !windows

package processor

import "os/exec"

func hideWindow(*exec.Cmd) {}
