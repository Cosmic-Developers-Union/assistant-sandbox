package executable

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	contentdigest "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/digest"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const maxScriptBytes = 4 << 20

const maxImageBytes = 64 << 20

type ABI string

const (
	ABIBuiltin ABI = "builtin-go"

	ABIShebang ABI = "shebang"

	ABIELF ABI = "elf"
)

type BinaryFactory interface {
	New(path string) (Program, []string, error)
}

type Resolved struct {
	Name string
	ABI  ABI

	Program Program

	Args []string

	ContentDigest string
}

type ContentDigester interface {
	DigestFor(context.Context, string) (string, error)
}

type Resolver struct {
	registry Registry
	system   vmfs.System

	image ContentDigester

	binaries BinaryFactory
}

func (r *Resolver) WithBinaries(factory BinaryFactory) *Resolver {
	r.binaries = factory
	return r
}

func NewResolver(registry Registry, system vmfs.System) *Resolver {
	return &Resolver{registry: registry, system: system}
}

func NewBoundResolver(registry Registry, system vmfs.System, digester ContentDigester) *Resolver {
	return &Resolver{registry: registry, system: system, image: digester}
}

const (
	maxShebangLine  = 512
	maxResolveDepth = 4
)

func (r *Resolver) Resolve(ctx context.Context, name string) (Resolved, error) {
	return r.resolve(ctx, name, 0)
}

func (r *Resolver) resolve(ctx context.Context, name string, depth int) (Resolved, error) {
	if depth > maxResolveDepth {
		return Resolved{}, fmt.Errorf("解释器解析过深: %w", errno.LinkLoop)
	}
	if program, err := r.registry.Lookup(name); err == nil {
		return Resolved{Name: name, ABI: ABIBuiltin, Program: program}, nil
	}
	if !strings.Contains(name, "/") || r.system == nil {
		return Resolved{}, fmt.Errorf("程序%s: %w", name, errno.NotFound)
	}
	head, err := r.head(ctx, name)
	if err != nil {

		if errors.Is(err, errno.NotFound) {
			if program, lookupErr := r.registry.Lookup(path.Base(name)); lookupErr == nil && path.Base(name) != name {
				return Resolved{Name: path.Base(name), ABI: ABIBuiltin, Program: program}, nil
			}
		}
		return Resolved{}, err
	}
	interpreter, arg, ok := cutShebang(firstLine(head))
	if !ok {
		if r.binaries != nil && isBinaryImage(head) {

			digest, err := r.bindContent(ctx, name, maxImageBytes)
			if err != nil {
				return Resolved{}, err
			}
			program, args, err := r.binaries.New(name)
			if err != nil {
				return Resolved{}, err
			}
			return Resolved{Name: name, ABI: ABIELF, Program: program, Args: args, ContentDigest: digest}, nil
		}
		return Resolved{}, fmt.Errorf("%s 不是可执行格式: %w", name, errno.NoExec)
	}

	resolved, err := r.resolve(ctx, interpreter, depth+1)
	if err != nil {
		return Resolved{}, fmt.Errorf("解释器%s: %w", interpreter, errno.NotFound)
	}
	digest, err := r.bindContent(ctx, name, maxScriptBytes)
	if err != nil {
		return Resolved{}, err
	}
	args := make([]string, 0, 2)
	if arg != "" {
		args = append(args, arg)
	}
	args = append(args, name)

	return Resolved{Name: resolved.Name, ABI: ABIShebang, Program: resolved.Program, Args: args, ContentDigest: digest}, nil
}

func (r *Resolver) bindContent(ctx context.Context, path string, limit int64) (string, error) {
	if r.image == nil {
		return "", nil
	}
	expected, err := r.image.DigestFor(ctx, path)
	if err != nil {
		return "", fmt.Errorf("脚本%s不在镜像内: %w", path, errno.NoExec)
	}
	actual, err := r.contentDigest(ctx, path, limit)
	if err != nil {
		return "", err
	}
	if actual != expected {
		return "", fmt.Errorf("脚本%s内容与镜像登记不一致: %w", path, errno.NoExec)
	}
	return actual, nil
}

func (r *Resolver) contentDigest(ctx context.Context, path string, limit int64) (digest string, resultErr error) {
	file, err := r.system.Open(ctx, path, 0, 0)
	if err != nil {
		return "", fmt.Errorf("脚本%s: %w", path, errno.NotFound)
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", fmt.Errorf("读取%s: %w", path, err)
	}
	if int64(len(content)) > limit {
		return "", fmt.Errorf("%s超过内容预算: %w", path, errno.NoExec)
	}
	return contentdigest.Content(content), nil
}

func firstLine(head []byte) string {
	line := string(head)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	return strings.TrimSuffix(line, "\r")
}

func isBinaryImage(head []byte) bool {
	return len(head) >= 4 && head[0] == 0x7f && head[1] == 'E' && head[2] == 'L' && head[3] == 'F'
}

func (r *Resolver) head(ctx context.Context, path string) (head []byte, resultErr error) {
	file, err := r.system.Open(ctx, path, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("程序%s: %w", path, errno.NotFound)
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()
	buf := make([]byte, maxShebangLine)
	n, err := file.Read(buf)
	if err != nil && n == 0 {
		return nil, fmt.Errorf("%s 为空: %w", path, errno.NoExec)
	}
	return buf[:n], nil
}

func cutShebang(line string) (interpreter, arg string, ok bool) {
	if !strings.HasPrefix(line, "#!") {
		return "", "", false
	}
	fields := strings.Fields(line[2:])
	if len(fields) == 0 || fields[0] == "" {
		return "", "", false
	}
	interpreter = fields[0]
	if len(fields) > 1 {
		arg = fields[1]
	}
	if len(fields) > 2 {
		return "", "", false
	}
	return interpreter, arg, true
}
