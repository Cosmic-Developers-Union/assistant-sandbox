package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	contentdigest "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const ABIBuiltin = "builtin-go"

type Memory struct {
	mu     sync.Mutex
	man    Manifest
	commit string
	files  map[string][]byte

	digests map[string]string
}

func New(abi, entry string, files map[string][]byte) (*Memory, error) {
	if abi == "" || entry == "" {
		return nil, errno.Invalid
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("镜像内容为空: %w", errno.Invalid)
	}
	if _, ok := files[entry]; !ok {
		return nil, fmt.Errorf("镜像入口%s缺失: %w", entry, errno.NotFound)
	}
	for name := range files {
		if name == "" || !strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
			return nil, fmt.Errorf("镜像路径%s无效: %w", name, errno.Invalid)
		}
	}
	h := sha256.New()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		_, _ = fmt.Fprintf(h, "%s:%d:", name, len(files[name]))
		_, _ = h.Write(files[name])
	}
	digest := "sha256:" + hex.EncodeToString(h.Sum(nil))
	cloned := make(map[string][]byte, len(files))
	digests := make(map[string]string, len(files))
	for name, data := range files {
		cloned[name] = append([]byte(nil), data...)
		digests[name] = ContentDigest(data)
	}
	return &Memory{
		man: Manifest{
			Version: ManifestVersion, Digest: digest, ABI: abi, Entry: entry,
			Architecture: ArchitectureRV64, Files: fileEntries(files, entry),
		},
		files:   cloned,
		digests: digests,
		commit:  digest,
	}, nil
}

// fileEntries 按路径排序生成文件清单;入口文件记录可执行权限,其余
// 文件默认 0644(N01)。
func fileEntries(files map[string][]byte, entry string) []FileEntry {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	entries := make([]FileEntry, 0, len(names))
	for _, name := range names {
		mode := uint32(0o644)
		if name == entry {
			mode = 0o755
		}
		entries = append(entries, FileEntry{
			Path:   name,
			Digest: ContentDigest(files[name]),
			Mode:   mode,
			Size:   int64(len(files[name])),
			Kind:   "file",
		})
	}
	return entries
}

// DeclareDependencies 记录镜像运行所需的外部组件(N01)。
func (m *Memory) DeclareDependencies(dependencies ...Dependency) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.man.Dependencies = append([]Dependency(nil), dependencies...)
}

func ContentDigest(data []byte) string { return contentdigest.Content(data) }

func (m *Memory) DigestFor(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	digest, ok := m.digests[path]
	if !ok {
		return "", fmt.Errorf("镜像不包含%s: %w", path, errno.NotFound)
	}
	return digest, nil
}

func (m *Memory) Manifest(ctx context.Context) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.man, nil
}

func (m *Memory) DeclareBuilder(registry, language string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.man.Registry = registry
	m.man.Language = language
}

func (m *Memory) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("镜像不包含%s: %w", path, errno.NotFound)
	}
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (m *Memory) Paths() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.files))
	for name := range m.files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type LayerImage interface {
	Paths() []string
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Manifest(ctx context.Context) (Manifest, error)
}
