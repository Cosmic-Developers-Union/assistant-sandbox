// Package native 提供 CLI 使用的真实 riscv64 sh/Bash 客体资产。
// 内建 mvdan shell 仍保留给 SDK/模型兼容,但不是 CLI 的默认执行路径。
package native

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	stdfs "io/fs"
	"path"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

// Shell 是 CLI 支持的原生客体 shell。
type Shell string

const (
	Bash Shell = "bash"
	Sh   Shell = "sh"
)

// Parse 解析原生 shell 名称。zsh 暂不提供客体资产。
func Parse(name string) (Shell, error) {
	switch Shell(name) {
	case Bash:
		return Bash, nil
	case Sh:
		return Sh, nil
	case "zsh":
		return "", errors.New("zsh 暂不支持:没有 riscv64 客体 zsh 资产")
	default:
		return "", fmt.Errorf("不支持的 shell %q(可用: bash, sh)", name)
	}
}

// Program 返回客体中的原生 shell 路径。
func (s Shell) Program() string {
	if s == Sh {
		return "/bin/sh"
	}
	return "/bin/bash"
}

//go:embed assets/bash
var bashELF []byte

//go:embed assets/busybox.static
var busyboxELF []byte

//go:embed assets/ld-musl-riscv64.so.1
var muslLoader []byte

//go:embed assets/libreadline.so.8.2
var readline []byte

//go:embed assets/libncursesw.so.6.5
var ncurses []byte

// Install 把 Bash、BusyBox sh、musl 加载器和 Bash 的 readline/ncurses
// 依赖装入客体根文件系统。重复调用是幂等的。
func Install(ctx context.Context, system vmfs.System) error {
	if system == nil {
		return errno.Invalid
	}
	for _, dir := range []string{"/bin", "/lib", "/usr/lib"} {
		if err := mkdirAll(ctx, system, dir); err != nil {
			return fmt.Errorf("建立 %s: %w", dir, err)
		}
	}
	files := []struct {
		path string
		data []byte
		mode uint32
	}{
		{"/lib/ld-musl-riscv64.so.1", muslLoader, 0o755},
		{"/usr/lib/libreadline.so.8.2", readline, 0o755},
		{"/usr/lib/libncursesw.so.6.5", ncurses, 0o755},
		{"/bin/bash", bashELF, 0o755},
		{"/bin/busybox", busyboxELF, 0o755},
	}
	for _, file := range files {
		if err := write(ctx, system, file.path, file.data, file.mode); err != nil {
			return err
		}
	}
	links := [][2]string{
		{"/lib/libc.musl-riscv64.so.1", "ld-musl-riscv64.so.1"},
		{"/usr/lib/libreadline.so.8", "libreadline.so.8.2"},
		{"/usr/lib/libncursesw.so.6", "libncursesw.so.6.5"},
		{"/bin/sh", "busybox"},
	}
	for _, applet := range busyboxApplets {
		links = append(links, [2]string{"/bin/" + applet, "busybox"})
	}
	for _, link := range links {
		if _, err := system.Lstat(ctx, link[0]); err == nil {
			continue
		}
		if err := system.Symlink(ctx, link[1], link[0]); err != nil && !errors.Is(err, errno.Exist) {
			return fmt.Errorf("建立 %s: %w", link[0], err)
		}
	}
	return nil
}

// busyboxApplets 是 CLI 工作区可直接调用的常用基础工具。BusyBox 以
// 符号链接目标 argv[0] 选择 applet;未列出的工具仍可经 busybox 调用。
var busyboxApplets = []string{
	"awk", "base64", "basename", "cat", "chgrp", "chmod", "chown",
	"cksum", "clear", "cmp", "comm", "cp", "cut", "date", "dd", "df",
	"diff", "dirname", "dmesg", "du", "echo", "env", "expand", "expr",
	"false", "find", "fold", "free", "grep", "groups", "gunzip", "gzip",
	"head", "hostname", "id", "kill", "killall", "less", "ln", "logname",
	"ls", "md5sum", "mkdir", "mkfifo", "mknod", "mktemp", "more", "mv",
	"nc", "netstat", "nice", "nl", "nohup", "nproc", "od", "paste",
	"patch", "pgrep", "pidof", "pkill", "printenv", "printf", "ps", "pwd",
	"readlink", "realpath", "reset", "rm", "rmdir", "sed", "seq",
	"sha1sum", "sha256sum", "sha512sum", "sleep", "sort", "split", "stat",
	"strings", "stty", "sum", "sync", "tac", "tail", "tar", "tee", "test",
	"time", "timeout", "top", "touch", "tr", "true", "tty", "umount",
	"uname", "unexpand", "uniq", "unlink", "unshare", "unxz", "uptime",
	"vi", "watch", "wc", "wget", "which", "who", "whoami", "xargs", "xxd",
	"xz", "xzcat", "yes", "zcat",
}

func mkdirAll(ctx context.Context, system vmfs.System, name string) error {
	if info, err := system.Stat(ctx, name); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s 不是目录", name)
		}
		return nil
	} else if !errors.Is(err, errno.NotFound) && !errors.Is(err, stdfs.ErrNotExist) {
		return err
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

func write(ctx context.Context, system vmfs.System, name string, data []byte, mode uint32) error {
	file, err := system.Open(ctx, name, vmfs.Create|vmfs.Truncate|vmfs.ReadWrite, stdfs.FileMode(mode))
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
