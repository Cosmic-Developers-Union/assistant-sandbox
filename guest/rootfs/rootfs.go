// Package rootfs 构建最小客体根文件系统(N02):固定目录布局、基础
// 配置文件与可选镜像内容,产物清单可复现并可在启动前校验。
package rootfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/filesystem/memfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/image"
)

// WorkRoot 是默认客体工作区。
const WorkRoot = "/work"

type standardDir struct {
	path string
	mode uint32
}

var standardDirs = []standardDir{
	{"/bin", 0o755},
	{"/dev", 0o755},
	{"/etc", 0o755},
	{"/home", 0o755},
	{"/lib", 0o755},
	{"/lib64", 0o755},
	{"/proc", 0o755},
	{"/root", 0o700},
	{"/sys", 0o755},
	{"/tmp", 0o1777},
	{"/usr", 0o755},
	{"/usr/bin", 0o755},
	{"/usr/lib", 0o755},
	{"/usr/local", 0o755},
	{"/usr/local/bin", 0o755},
	{"/usr/share", 0o755},
}

type standardFile struct {
	path    string
	mode    uint32
	content string
}

var standardFiles = []standardFile{
	{"/etc/hostname", 0o644, "sandbox\n"},
	{"/etc/os-release", 0o644, "NAME=\"Assistant Sandbox Linux\"\nID=assistant\nVERSION_ID=\"1\"\nPRETTY_NAME=\"Assistant Sandbox Linux 1\"\n"},
	{"/etc/nsswitch.conf", 0o644, "passwd: files\ngroup: files\n"},
}

// DefaultPath 与 DefaultLocale 是客体会话的运行时默认值(N04)。
const (
	DefaultPath   = "/usr/local/bin:/usr/bin:/bin"
	DefaultLocale = "C.UTF-8"
)

// User 是根文件系统中的默认客体身份(N04)。
type User struct {
	Name  string
	UID   uint32
	GID   uint32
	Home  string
	Shell string
}

// DefaultUser 是 sandbox 用户。
var DefaultUser = User{Name: "sandbox", UID: 1000, GID: 1000, Home: WorkRoot, Shell: "/bin/sh"}

// Options 是根文件系统的构造参数。
type Options struct {
	Clock clock.Source

	Workdir string

	// User 是默认客体身份;零值使用 DefaultUser。
	User User

	// Image 是可选的只读内容层:文件按清单权限物化到根文件系统。
	Image image.LayerImage

	// Entry 是镜像清单记录的入口;为空时使用 /bin/sh。
	Entry string
}

// Root 是构建完成的根文件系统与可复现清单。
type Root struct {
	System   *memfs.FS
	Manifest image.Manifest
	User     User
}

// Environment 返回客体会话默认环境(N04):HOME/PATH/LANG/USER/LOGNAME
// 与 TMPDIR,由客体经 getenv 查询。
func (r *Root) Environment() map[string]string {
	return map[string]string{
		"HOME":    r.User.Home,
		"PATH":    DefaultPath,
		"LANG":    DefaultLocale,
		"LC_ALL":  DefaultLocale,
		"USER":    r.User.Name,
		"LOGNAME": r.User.Name,
		"TMPDIR":  "/tmp",
	}
}

// Digest 返回根文件系统清单摘要。
func (r *Root) Digest() string { return r.Manifest.Digest }

// Build 生成固定目录布局并物化可选镜像;相同输入产生相同清单摘要。
func Build(ctx context.Context, opts Options) (*Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source := opts.Clock
	if source == nil {
		source = &clock.Virtual{}
	}
	workdir := opts.Workdir
	if workdir == "" {
		workdir = WorkRoot
	}
	if !strings.HasPrefix(workdir, "/") || strings.ContainsRune(workdir, 0) {
		return nil, fmt.Errorf("工作目录 %q 无效: %w", workdir, errno.Invalid)
	}
	system, err := memfs.New(source, memfs.Options{})
	if err != nil {
		return nil, err
	}
	files := map[string]image.FileEntry{}
	for _, dir := range standardDirs {
		if err := system.Mkdir(ctx, dir.path, modeOf(dir.mode)); err != nil {
			return nil, fmt.Errorf("建立根目录 %s: %w", dir.path, err)
		}
		files[dir.path] = image.FileEntry{Path: dir.path, Mode: dir.mode, Kind: "directory"}
	}
	if !containsDir(workdir) {
		if err := ensureDir(ctx, system, path.Dir(workdir)); err != nil {
			return nil, err
		}
		if err := system.Mkdir(ctx, workdir, 0o755); err != nil && !errors.Is(err, errno.Exist) {
			return nil, fmt.Errorf("建立工作目录 %s: %w", workdir, err)
		}
		files[workdir] = image.FileEntry{Path: workdir, Mode: 0o755, Kind: "directory"}
	}
	for _, file := range standardFiles {
		if err := writeFile(ctx, system, file.path, file.mode, []byte(file.content)); err != nil {
			return nil, err
		}
		recordFile(files, file.path, file.mode, []byte(file.content), "file")
	}
	user := opts.User
	if user.Name == "" {
		user = DefaultUser
	}
	if user.Home == "" {
		user.Home = WorkRoot
	}
	if user.Shell == "" {
		user.Shell = "/bin/sh"
	}
	if !containsDir(user.Home) {
		if err := ensureDir(ctx, system, path.Dir(user.Home)); err != nil {
			return nil, err
		}
		if err := system.Mkdir(ctx, user.Home, 0o755); err != nil && !errors.Is(err, errno.Exist) {
			return nil, fmt.Errorf("建立用户目录 %s: %w", user.Home, err)
		}
		files[user.Home] = image.FileEntry{Path: user.Home, Mode: 0o755, Kind: "directory"}
	}
	for _, file := range identityFiles(user) {
		if err := writeFile(ctx, system, file.path, file.mode, []byte(file.content)); err != nil {
			return nil, err
		}
		recordFile(files, file.path, file.mode, []byte(file.content), "file")
	}
	if opts.Image != nil {
		if err := materialize(ctx, system, opts.Image, files); err != nil {
			return nil, err
		}
	}
	entry := opts.Entry
	if entry == "" {
		entry = "/bin/sh"
	}
	manifest := image.Manifest{
		Version:      image.ManifestVersion,
		Architecture: image.ArchitectureRV64,
		ABI:          image.ABILinuxRV64,
		Entry:        entry,
		Files:        sortedFiles(files),
	}
	manifest.Digest = manifestDigest(manifest.Files)
	if err := manifest.Validate(image.Requirements{}); err != nil {
		return nil, fmt.Errorf("根文件系统清单无效: %w", err)
	}
	return &Root{System: system, Manifest: manifest, User: user}, nil
}

// identityFiles 生成身份与运行时配置(N04):passwd/group/profile 与
// locale.conf。UTF-8 由 C.UTF-8 locale 与 libc 的 UTF-8 多字节转换提供。
func identityFiles(user User) []standardFile {
	passwd := "root:x:0:0:root:/root:/bin/sh\n" +
		fmt.Sprintf("%s:x:%d:%d:%s:%s:%s\n", user.Name, user.UID, user.GID, user.Name, user.Home, user.Shell)
	group := "root:x:0:\n" + fmt.Sprintf("%s:x:%d:\n", user.Name, user.GID)
	profile := fmt.Sprintf("export HOME=%s\nexport PATH=%s\nexport LANG=%s\nexport LC_ALL=%s\n", user.Home, DefaultPath, DefaultLocale, DefaultLocale)
	return []standardFile{
		{"/etc/passwd", 0o644, passwd},
		{"/etc/group", 0o644, group},
		{"/etc/profile", 0o644, profile},
		{"/etc/locale.conf", 0o644, "LANG=" + DefaultLocale + "\n"},
		{"/etc/environment", 0o644, "PATH=" + DefaultPath + "\nLANG=" + DefaultLocale + "\n"},
	}
}

func recordFile(files map[string]image.FileEntry, name string, mode uint32, data []byte, kind string) {
	files[name] = image.FileEntry{
		Path: name, Digest: image.ContentDigest(data), Mode: mode,
		Size: int64(len(data)), Kind: kind,
	}
}

func containsDir(dir string) bool {
	for _, candidate := range standardDirs {
		if candidate.path == dir {
			return true
		}
	}
	return false
}

func writeFile(ctx context.Context, system vmfs.System, name string, mode uint32, data []byte) error {
	if err := ensureDir(ctx, system, path.Dir(name)); err != nil {
		return err
	}
	file, err := system.Open(ctx, name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, modeOf(mode))
	if err != nil {
		return fmt.Errorf("创建根文件系统文件 %s: %w", name, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return fmt.Errorf("写入根文件系统文件 %s: %w", name, errors.Join(writeErr, closeErr))
	}
	return nil
}

func ensureDir(ctx context.Context, system vmfs.System, dir string) error {
	if dir == "/" || dir == "." || dir == "" {
		return nil
	}
	if err := ensureDir(ctx, system, path.Dir(dir)); err != nil {
		return err
	}
	if err := system.Mkdir(ctx, dir, 0o755); err != nil && !errors.Is(err, errno.Exist) {
		return fmt.Errorf("建立根文件系统目录 %s: %w", dir, err)
	}
	return nil
}

func materialize(ctx context.Context, system vmfs.System, source image.LayerImage, files map[string]image.FileEntry) error {
	manifest, err := source.Manifest(ctx)
	if err != nil {
		return fmt.Errorf("读取根文件系统内容清单: %w", err)
	}
	entries := map[string]image.FileEntry{}
	for _, entry := range manifest.Files {
		entries[entry.Path] = entry
	}
	for _, name := range source.Paths() {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, declared := entries[name]
		mode := uint32(0o644)
		if declared && entry.Mode != 0 {
			mode = entry.Mode
		}
		file, err := system.Open(ctx, name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, modeOf(mode))
		if err != nil {
			return fmt.Errorf("物化根文件系统文件 %s: %w", name, err)
		}
		reader, err := source.Open(ctx, name)
		if err != nil {
			_ = file.Close()
			return fmt.Errorf("读取根文件系统文件 %s: %w", name, err)
		}
		data, readErr := io.ReadAll(reader)
		closeReaderErr := reader.Close()
		if readErr != nil || closeReaderErr != nil {
			_ = file.Close()
			return fmt.Errorf("读取根文件系统文件 %s: %w", name, errors.Join(readErr, closeReaderErr))
		}
		_, writeErr := file.Write(data)
		closeFileErr := file.Close()
		if writeErr != nil || closeFileErr != nil {
			return fmt.Errorf("写入根文件系统文件 %s: %w", name, errors.Join(writeErr, closeFileErr))
		}
		files[name] = image.FileEntry{
			Path: name, Digest: image.ContentDigest(data), Mode: mode,
			Size: int64(len(data)), Kind: "file",
		}
	}
	return nil
}

func sortedFiles(files map[string]image.FileEntry) []image.FileEntry {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]image.FileEntry, 0, len(names))
	for _, name := range names {
		out = append(out, files[name])
	}
	return out
}

func modeOf(mode uint32) stdfs.FileMode {
	result := stdfs.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		result |= stdfs.ModeSetuid
	}
	if mode&0o2000 != 0 {
		result |= stdfs.ModeSetgid
	}
	if mode&0o1000 != 0 {
		result |= stdfs.ModeSticky
	}
	return result
}

func manifestDigest(files []image.FileEntry) string {
	hash := sha256.New()
	for _, file := range files {
		_, _ = fmt.Fprintf(hash, "%s:%o:%s:%d;", file.Path, file.Mode, file.Digest, file.Size)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
