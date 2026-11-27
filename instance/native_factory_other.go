//go:build !linux || !amd64

package instance

func registeredNativeFactory() BackendFactory { return nil }
