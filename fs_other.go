//go:build !unix

package sandbox

func nonblockFlag() int { return 0 }
