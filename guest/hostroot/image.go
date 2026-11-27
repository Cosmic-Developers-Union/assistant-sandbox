// Package hostroot 提供固定的 linux/amd64 客体根文件系统。
package hostroot

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// ArchiveSHA256 锁定 rootfs-amd64.tar.gz 的内容。
	ArchiveSHA256   = "73a1ff00753239a733df5da2326b7b08e76c0336c93682824d8b29d927b08f06"
	maxArchiveBytes = 512 << 20
	maxFileBytes    = 128 << 20
)

var (
	// ErrInvalidArchive 表示镜像摘要、格式或必需文件不符合固定契约。
	ErrInvalidArchive = errors.New("invalid amd64 root filesystem")
)

// Verify 校验固定摘要、gzip/tar 完整性和必需运行时路径。
func Verify(archive []byte) error {
	digest := sha256.Sum256(archive)
	if hex.EncodeToString(digest[:]) != ArchiveSHA256 {
		return fmt.Errorf("%w: sha256=%s want %s", ErrInvalidArchive, hex.EncodeToString(digest[:]), ArchiveSHA256)
	}
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("%w: gzip: %w", ErrInvalidArchive, err)
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	required := map[string]bool{
		"bin/bash": false, "bin/sh": false,
		"usr/bin/node": false, "usr/bin/python3.12": false,
		"usr/bin/assistant-init":  false,
		"lib/ld-musl-x86_64.so.1": false,
	}
	var total int64
	for {
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: tar: %w", ErrInvalidArchive, err)
		}
		total += header.Size
		if total > maxArchiveBytes {
			return fmt.Errorf("%w: expanded size exceeds %d", ErrInvalidArchive, maxArchiveBytes)
		}
		name := cleanArchivePath(header.Name)
		if _, ok := required[name]; ok && header.Typeflag != tar.TypeDir {
			required[name] = true
		}
	}
	for name, found := range required {
		if !found {
			return fmt.Errorf("%w: missing %s", ErrInvalidArchive, name)
		}
	}
	return nil
}

// Extract 把已校验镜像安全解包到 destination。调用方应传入新的空目录。
func Extract(ctx context.Context, archive []byte, destination string) error {
	if err := Verify(archive); err != nil {
		return err
	}
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidArchive, err)
	}
	defer func() { _ = reader.Close() }()
	files := tar.NewReader(reader)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := files.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidArchive, err)
		}
		name := cleanArchivePath(header.Name)
		if name == "." || name == "" {
			continue
		}
		if header.Size < 0 || header.Size > maxFileBytes || total+header.Size > maxArchiveBytes {
			return fmt.Errorf("%w: size limit at %s", ErrInvalidArchive, name)
		}
		total += header.Size
		target := path.Join(destination, name)
		if err := ensureParents(destination, target); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalidArchive, name, err)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, archiveMode(header.Mode)|0o700); err != nil {
				return fmt.Errorf("%w: mkdir %s: %w", ErrInvalidArchive, name, err)
			}
		case tar.TypeReg:
			if err := extractFile(target, files, header.Size, archiveMode(header.Mode)); err != nil {
				return fmt.Errorf("%w: file %s: %w", ErrInvalidArchive, name, err)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(header.Linkname, target); err != nil {
				return fmt.Errorf("%w: symlink %s: %w", ErrInvalidArchive, name, err)
			}
		case tar.TypeLink:
			source := path.Join(destination, cleanArchivePath(header.Linkname))
			if !withinRoot(destination, source) {
				return fmt.Errorf("%w: hardlink escapes root: %s", ErrInvalidArchive, header.Linkname)
			}
			if err := os.Link(source, target); err != nil {
				return fmt.Errorf("%w: hardlink %s: %w", ErrInvalidArchive, name, err)
			}
		}
	}
}

func archiveMode(mode int64) fs.FileMode {
	return fs.FileMode(uint32(mode) & 0o7777) //nolint:gosec // tar permission bits are explicitly masked
}

func cleanArchivePath(name string) string {
	name = strings.TrimPrefix(path.Clean(strings.TrimPrefix(name, "./")), "/")
	if name == ".." || strings.HasPrefix(name, "../") {
		return ""
	}
	return name
}

func withinRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, "../")
}

func ensureParents(root, target string) error {
	for parent := path.Dir(target); parent != root && parent != "."; parent = path.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("parent %s is not a directory", parent)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err := os.Mkdir(parent, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

func extractFile(target string, source io.Reader, size int64, mode fs.FileMode) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) //nolint:gosec // target is a cleaned archive path under the caller's root
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, size))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != size {
		return io.ErrUnexpectedEOF
	}
	return os.Chmod(target, mode)
}
