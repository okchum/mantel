//go:build !unix

package collect

import "os/exec"

// killGroup keeps exec.CommandContext's default here: cancellation kills the
// direct child only.
func killGroup(*exec.Cmd) {}
