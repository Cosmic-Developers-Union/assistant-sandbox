//go:build linux && amd64

package hostroot

import _ "embed"

//go:embed rootfs-amd64.tar.gz
var embeddedArchive []byte

// Archive 返回嵌入的固定镜像；调用方不得修改返回切片。
func Archive() []byte { return embeddedArchive }
