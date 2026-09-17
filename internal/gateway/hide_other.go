//go:build !windows

package gateway

import "os/exec"

func hideWindow(*exec.Cmd) {}
