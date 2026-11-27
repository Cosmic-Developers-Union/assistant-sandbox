//go:build !linux || !amd64

package hostroot

// Archive 在非 linux/amd64 构建中不可用；原生后端不会在这些平台注册。
func Archive() []byte { return nil }
