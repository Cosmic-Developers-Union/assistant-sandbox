package image

import (
	"context"
	"io"
)

// ManifestVersion 是当前镜像清单格式版本(N01)。
const ManifestVersion = 1

// 客体架构与目标 ABI(N01/A04)。
const (
	ArchitectureRV64 = "riscv64"
	ABILinuxRV64     = "linux-rv64gc"
)

// FileEntry 记录镜像文件的内容身份、权限与类型(N01):启动前可逐项
// 校验,不依赖宿主路径或隐式状态。
type FileEntry struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind,omitempty"`
	Target string `json:"target,omitempty"`
}

// Dependency 是镜像运行所需的外部组件记录(N01):版本与摘要可追溯。
type Dependency struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

type Manifest struct {
	Version int `json:"version"`

	Digest string `json:"digest"`
	ABI    string `json:"abi"`
	Entry  string `json:"entry"`

	Architecture string `json:"architecture,omitempty"`

	Registry string `json:"registry,omitempty"`

	Language string `json:"language,omitempty"`

	Files []FileEntry `json:"files,omitempty"`

	Dependencies []Dependency `json:"dependencies,omitempty"`
}

// Requirements 是启动前对镜像的兼容性要求(N01)。
type Requirements struct {
	Version      int
	Architecture string
	ABI          string
	Entry        string
	Digest       string
}

type Image interface {
	Manifest(context.Context) (Manifest, error)
	Open(context.Context, string) (io.ReadCloser, error)

	DigestFor(context.Context, string) (string, error)
}
