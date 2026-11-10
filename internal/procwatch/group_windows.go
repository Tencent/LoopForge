//go:build windows

package procwatch

import "os/exec"

// killGroup keeps exec's default on Windows: kill the direct child.
func killGroup(*exec.Cmd) {}
