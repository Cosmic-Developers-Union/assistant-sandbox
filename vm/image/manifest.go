// manifest.go 定义版本化镜像清单(N01):记录架构、ABI、入口、文件
// 权限与内容摘要、依赖,并支持启动前兼容性与摘要校验。
package image

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// Encode 把清单编码为稳定的 JSON 文档(字段顺序由结构定义固定)。
func (m Manifest) Encode() ([]byte, error) {
	if err := m.Validate(Requirements{}); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("编码镜像清单: %w", err)
	}
	return append(encoded, '\n'), nil
}

// DecodeManifest 解析并校验版本化镜像清单。
func DecodeManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("解析镜像清单: %w: %w", errno.Version, err)
	}
	if err := manifest.Validate(Requirements{}); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate 校验清单自身完整性;requirements 中的非空字段必须匹配。
func (m Manifest) Validate(requirements Requirements) error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("镜像清单版本 %d 不受支持(需要 %d): %w", m.Version, ManifestVersion, errno.Version)
	}
	if requirements.Version != 0 && requirements.Version != m.Version {
		return fmt.Errorf("镜像清单版本 %d 与要求 %d 不符: %w", m.Version, requirements.Version, errno.Version)
	}
	if m.Architecture == "" {
		return fmt.Errorf("镜像清单缺少架构: %w", errno.Invalid)
	}
	if requirements.Architecture != "" && requirements.Architecture != m.Architecture {
		return fmt.Errorf("镜像架构 %s 与要求 %s 不符: %w", m.Architecture, requirements.Architecture, errno.Version)
	}
	if m.ABI == "" {
		return fmt.Errorf("镜像清单缺少 ABI: %w", errno.Invalid)
	}
	if requirements.ABI != "" && requirements.ABI != m.ABI {
		return fmt.Errorf("镜像 ABI %s 与要求 %s 不符: %w", m.ABI, requirements.ABI, errno.Version)
	}
	if m.Entry == "" {
		return fmt.Errorf("镜像清单缺少入口: %w", errno.Invalid)
	}
	if requirements.Entry != "" && requirements.Entry != m.Entry {
		return fmt.Errorf("镜像入口 %s 与要求 %s 不符: %w", m.Entry, requirements.Entry, errno.Version)
	}
	if m.Digest != "" && !validDigest(m.Digest) {
		return fmt.Errorf("镜像摘要格式无效 %q: %w", m.Digest, errno.Version)
	}
	if requirements.Digest != "" && requirements.Digest != m.Digest {
		return fmt.Errorf("镜像摘要 %s 与要求 %s 不符: %w", m.Digest, requirements.Digest, errno.Version)
	}
	seen := map[string]bool{}
	for index, file := range m.Files {
		if file.Path == "" || !strings.HasPrefix(file.Path, "/") {
			return fmt.Errorf("清单第 %d 个文件路径 %q 无效: %w", index, file.Path, errno.Invalid)
		}
		if seen[file.Path] {
			return fmt.Errorf("清单文件 %s 重复: %w", file.Path, errno.Exist)
		}
		seen[file.Path] = true
		if file.Kind != "directory" && !validDigest(file.Digest) {
			return fmt.Errorf("清单文件 %s 摘要无效: %w", file.Path, errno.Version)
		}
		if file.Size < 0 {
			return fmt.Errorf("清单文件 %s 大小无效: %w", file.Path, errno.Invalid)
		}
	}
	for index, dependency := range m.Dependencies {
		if dependency.Name == "" {
			return fmt.Errorf("清单第 %d 个依赖缺少名称: %w", index, errno.Invalid)
		}
	}
	return nil
}

// Verify 在启动前校验镜像清单与实际内容摘要(N01):版本/架构/ABI/
// 入口不兼容或文件摘要不一致都拒绝启动。
func Verify(ctx context.Context, img Image, requirements Requirements) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if img == nil {
		return fmt.Errorf("缺少镜像: %w", errno.Invalid)
	}
	manifest, err := img.Manifest(ctx)
	if err != nil {
		return fmt.Errorf("读取镜像清单: %w", err)
	}
	if err := manifest.Validate(requirements); err != nil {
		return err
	}
	if len(manifest.Files) > 0 {
		found := false
		for _, file := range manifest.Files {
			if file.Path == manifest.Entry {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("镜像入口 %s 不在文件清单中: %w", manifest.Entry, errno.NoExec)
		}
	}
	for _, file := range manifest.Files {
		if file.Kind == "directory" || file.Kind == "symlink" {
			continue
		}
		digest, err := img.DigestFor(ctx, file.Path)
		if err != nil {
			return fmt.Errorf("校验镜像文件 %s: %w", file.Path, err)
		}
		if digest != file.Digest {
			return fmt.Errorf("镜像文件 %s 摘要 %s 与清单 %s 不符: %w", file.Path, digest, file.Digest, errno.Version)
		}
	}
	return nil
}

func validDigest(digest string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) || len(digest) != len(prefix)+64 {
		return false
	}
	_, err := hex.DecodeString(digest[len(prefix):])
	return err == nil && strings.ToLower(digest[len(prefix):]) == digest[len(prefix):]
}
