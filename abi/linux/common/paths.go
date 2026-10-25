package common

import (
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

// 客体路径解析(I01):全部文件类系统调用经同一入口解析,
// 语义一致且不逃逸客体根。
//
//   - 绝对路径直接使用,按词法消解 "." 与 "..";
//   - 相对路径以进程工作目录为基准;
//   - 词法消解在客体根处收敛:"/.." 等于 "/",不会越出授权根;
//   - 符号链接由底层文件系统按客体语义解析(含 ELOOP 上限),
//     解析器只负责路径的词法部分。
//
// 路径长度上限为 PathLimit(4096 字节),空路径与含 NUL 的路径按
// EINVAL 拒绝。

// Resolver 按工作目录解析客体路径。
type Resolver struct {
	// Dir 是进程工作目录(客体绝对路径)。
	Dir string
}

// NewResolver 建立以 dir 为工作目录的路径解析器;dir 非绝对路径时
// 以 "/" 为基准。
func NewResolver(dir string) Resolver {
	if dir == "" || !strings.HasPrefix(dir, "/") {
		dir = "/"
	}
	return Resolver{Dir: path.Clean(dir)}
}

// Resolve 解析客体路径为客体绝对路径。
func (r Resolver) Resolve(name string) (string, error) {
	if name == "" {
		return "", errno.Wrap(errno.Invalid, "客体路径不能为空")
	}
	if strings.ContainsRune(name, 0) {
		return "", errno.Wrap(errno.Invalid, "客体路径不能含 NUL")
	}
	if len(name) > PathLimit {
		return "", errno.Wrap(errno.NameTooLong, "客体路径超长")
	}
	base := r.Dir
	if base == "" {
		base = "/"
	}
	if strings.HasPrefix(name, "/") {
		base = "/"
	}
	// path.Join 消解 "." 与 "..",并在根部收敛。
	return path.Join(base, name), nil
}

// ResolvePair 解析一对客体路径(改名、硬链等)。
func (r Resolver) ResolvePair(first, second string) (string, string, error) {
	left, err := r.Resolve(first)
	if err != nil {
		return "", "", err
	}
	right, err := r.Resolve(second)
	if err != nil {
		return "", "", err
	}
	return left, right, nil
}
