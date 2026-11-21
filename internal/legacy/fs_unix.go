//go:build unix

package legacy

import "syscall"

func nonblockFlag() int { return syscall.O_NONBLOCK }
