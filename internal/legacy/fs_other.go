//go:build !unix

package legacy

func nonblockFlag() int { return 0 }
