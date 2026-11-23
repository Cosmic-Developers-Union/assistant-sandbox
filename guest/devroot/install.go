// Package devroot installs the embedded development rootfs assets used by
// the CLI: CPython, Node.js, npm, TinyCC and their runtime dependencies.
package devroot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const maxArchiveFileBytes = 128 << 20

//go:embed assets/*
var assets embed.FS

func asset(name string) ([]byte, error) {
	return assets.ReadFile("assets/" + name)
}

// Install 展开开发工具到客体根文件系统。重复调用可覆盖文件，但已存在
// 的符号链接和硬链接不会被替换。
func Install(ctx context.Context, system vmfs.System) error {
	if system == nil {
		return errno.Invalid
	}
	for _, dir := range []string{
		"/usr/bin", "/usr/include", "/usr/lib/python3.12",
		"/usr/lib/python3.12/site-packages", "/usr/lib/node_modules",
	} {
		if err := mkdirAll(ctx, system, dir); err != nil {
			return err
		}
	}
	pythonAPK, err := asset("python3-3.12.16-r0.apk")
	if err != nil {
		return err
	}
	if err := extract(ctx, system, pythonAPK, "/", pythonAPKEntry); err != nil {
		return fmt.Errorf("安装 CPython: %w", err)
	}
	for _, item := range []struct {
		name      string
		dest      string
		onlyFiles bool
	}{
		{"python312-libs.tar.gz", "/", false},
		{"python312-pyc.tar.gz", "/usr/lib/python3.12", true},
		{"python312-dev-headers.tar.gz", "/", false},
		{"node22-runtime.tar.gz", "/", false},
		{"npm-11.6.4.tar.gz", "/", false},
		{"tcc-riscv64-toolchain.tar.gz", "/", false},
	} {
		data, err := asset(item.name)
		if err != nil {
			return err
		}
		if err := extract(ctx, system, data, item.dest, func(name string) bool {
			return !item.onlyFiles || !strings.HasSuffix(name, "/")
		}); err != nil {
			return fmt.Errorf("安装 %s: %w", item.name, err)
		}
	}
	for _, link := range [][2]string{
		{"/usr/bin/python", "python3.12"},
		{"/usr/bin/python3", "python3.12"},
		{"/usr/bin/cc", "tcc"},
		{"/usr/bin/gcc", "tcc"},
		{"/usr/bin/env", "/bin/env"},
	} {
		if err := symlink(ctx, system, link[1], link[0]); err != nil {
			return err
		}
	}
	if _, err := system.Stat(ctx, "/usr/bin/node-bin"); err != nil {
		if !errors.Is(err, errno.NotFound) && !errors.Is(err, stdfs.ErrNotExist) {
			return err
		}
		if err := system.Rename(ctx, "/usr/bin/node", "/usr/bin/node-bin"); err != nil {
			return fmt.Errorf("移动 Node.js: %w", err)
		}
	}
	if err := writeFile(ctx, system, "/usr/bin/node",
		[]byte("#!/bin/sh\ncase \" $* \" in\n  *\" --jitless \"*) exec /usr/bin/node-bin \"$@\" ;;\n  *) exec /usr/bin/node-bin --jitless \"$@\" ;;\nesac\n"), 0o755); err != nil {
		return err
	}
	// 当前 VM 的 vfork 兼容路径会让嵌套构建子进程异常退出；开发工具
	// 统一走已验证的 fork 路径。
	if err := writeFile(ctx, system, "/usr/lib/python3.12/sitecustomize.py",
		[]byte("import subprocess\nsubprocess._USE_VFORK = False\n"), 0o644); err != nil {
		return err
	}
	return nil
}

func pythonAPKEntry(name string) bool {
	return name == "usr/bin/python3.12" ||
		name == "usr/lib/libpython3.12.so.1.0" ||
		strings.HasPrefix(name, "usr/lib/python3.12/") ||
		strings.HasPrefix(name, "usr/include/python3.12/")
}

func extract(
	ctx context.Context,
	system vmfs.System,
	archive []byte,
	destination string,
	include func(string) bool,
) error {
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			continue
		}
		if include != nil && !include(name) {
			continue
		}
		target := path.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := mkdirAll(ctx, system, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size > maxArchiveFileBytes {
				return fmt.Errorf("%s 超过单文件上限", target)
			}
			data, err := io.ReadAll(io.LimitReader(files, maxArchiveFileBytes+1))
			if err != nil {
				return err
			}
			mode := header.FileInfo().Mode().Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := writeFile(ctx, system, target, data, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := symlink(ctx, system, header.Linkname, target); err != nil {
				return err
			}
		case tar.TypeLink:
			source := path.Join(destination, path.Clean(header.Linkname))
			if err := link(ctx, system, source, target); err != nil {
				return err
			}
		}
	}
}

func mkdirAll(ctx context.Context, system vmfs.System, name string) error {
	if info, err := system.Stat(ctx, name); err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("%s 不是目录", name)
	}
	if parent := path.Dir(name); parent != name {
		if err := mkdirAll(ctx, system, parent); err != nil {
			return err
		}
	}
	if err := system.Mkdir(ctx, name, 0o755); err != nil {
		if info, statErr := system.Stat(ctx, name); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

func writeFile(ctx context.Context, system vmfs.System, name string, data []byte, mode stdfs.FileMode) error {
	if err := mkdirAll(ctx, system, path.Dir(name)); err != nil {
		return err
	}
	file, err := system.Open(ctx, name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, mode)
	if err != nil {
		return fmt.Errorf("写入 %s: %w", name, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入 %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭 %s: %w", name, err)
	}
	return nil
}

func symlink(ctx context.Context, system vmfs.System, target, name string) error {
	if err := mkdirAll(ctx, system, path.Dir(name)); err != nil {
		return err
	}
	if _, err := system.Lstat(ctx, name); err == nil {
		return nil
	}
	if err := system.Symlink(ctx, target, name); err != nil && !errors.Is(err, errno.Exist) {
		return fmt.Errorf("建立 %s: %w", name, err)
	}
	return nil
}

func link(ctx context.Context, system vmfs.System, source, target string) error {
	if err := mkdirAll(ctx, system, path.Dir(target)); err != nil {
		return err
	}
	if _, err := system.Lstat(ctx, target); err == nil {
		return nil
	}
	if err := system.Link(ctx, source, target); err != nil && !errors.Is(err, errno.Exist) {
		return fmt.Errorf("建立硬链接 %s -> %s: %w", target, source, err)
	}
	return nil
}
