//go:build unix

package sandbox

import "syscall"

func nonblockFlag() int { return syscall.O_NONBLOCK }
