//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package cli

import "os/exec"

func detachCommand(_ *exec.Cmd) {}
