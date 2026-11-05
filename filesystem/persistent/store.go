package persistent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
)

const (
	sessionSuffix = ".session"
	snapshotDir   = "snapshots"
	tempPrefix    = ".tmp-"

	digestPrefix = "sha256:"
)

// sessionFile 是会话文件的自校验容器:I17——摘要行与内容同文件,
// 单次原子重命名提交,消除"数据与摘要两个文件"提交窗口。格式:
//
//	sha256:<64 位十六进制>\n<内容字节>
type sessionFile struct {
	digest string
	data   []byte
}

func encodeSession(data []byte) []byte {
	buffer := make([]byte, 0, len(digestPrefix)+64+1+len(data))
	buffer = append(buffer, digestPrefix...)
	buffer = append(buffer, strings.TrimPrefix(digestOf(data), digestPrefix)...)
	buffer = append(buffer, '\n')
	return append(buffer, data...)
}

func decodeSession(raw []byte) (sessionFile, bool) {
	if !strings.HasPrefix(string(raw), digestPrefix) {
		return sessionFile{}, false
	}
	newline := bytes.IndexByte(raw, '\n')
	if newline < 0 {
		return sessionFile{}, false
	}
	digest := string(raw[:newline])
	if len(digest) != len(digestPrefix)+64 || !isHex(strings.TrimPrefix(digest, digestPrefix)) {
		return sessionFile{}, false
	}
	return sessionFile{digest: digest, data: raw[newline+1:]}, true
}

// verify 校验容器内容与摘要一致;不一致为已定义的损坏状态。
func (f sessionFile) verify() bool {
	return digestOf(f.data) == f.digest
}

type Store struct {
	root string

	// commit 是提交原语层(I17):默认宿主文件系统实现,测试注入
	// 按步失败以模拟写入关键步骤的中断。
	commit commitFS
}

// commitFS 是原子提交所需的宿主文件系统原语集合;每一步都可能
// 独立失败,测试按步注入中断。
type commitFS interface {
	CreateTemp(dir, pattern string) (*os.File, error)
	Write(*os.File, []byte) error
	Sync(*os.File) error
	Close(*os.File) error
	Chmod(name string, mode os.FileMode) error
	Rename(oldname, newname string) error
	SyncDir(dir string) error
	Remove(name string) error
}

type hostCommitFS struct{}

func (hostCommitFS) CreateTemp(dir, pattern string) (*os.File, error) {
	return os.CreateTemp(dir, pattern)
}

func (hostCommitFS) Write(file *os.File, data []byte) error {
	_, err := file.Write(data)
	return err
}

func (hostCommitFS) Sync(file *os.File) error                  { return file.Sync() }
func (hostCommitFS) Close(file *os.File) error                 { return file.Close() }
func (hostCommitFS) Chmod(name string, mode os.FileMode) error { return os.Chmod(name, mode) }
func (hostCommitFS) Rename(oldname, newname string) error      { return os.Rename(oldname, newname) }
func (hostCommitFS) SyncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}

func (hostCommitFS) Remove(name string) error { return os.Remove(name) }

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errno.Wrap(errno.Invalid, "存储根目录不能为空")
	}
	store := &Store{root: root, commit: hostCommitFS{}}
	for _, dir := range []string{root, path.Join(root, snapshotDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, errno.Wrap(err, "创建存储目录")
		}
	}
	// I17 重启恢复:上次进程中断遗留的临时文件不可能是已提交状态
	// (提交是单次原子重命名),按定义丢弃,存储回到最近一次提交。
	if err := store.discardTemps(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

// discardTemps 移除全部临时残留;只处理本存储根下的 .tmp- 前缀。
func (s *Store) discardTemps(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return errno.Wrap(err, "读取存储目录")
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), tempPrefix) {
			continue
		}
		if err := os.Remove(path.Join(s.root, entry.Name())); err != nil && !os.IsNotExist(err) {
			return errno.Wrap(err, "清理未完成提交残留")
		}
	}
	return nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) sessionPath(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	return path.Join(s.root, name+sessionSuffix), nil
}

func validateName(name string) error {
	if name == "" {
		return errno.Wrap(errno.Invalid, "会话名不能为空")
	}
	if name == "." || name == ".." {
		return errno.Wrap(errno.Invalid, "会话名不能是相对路径段")
	}
	if strings.ContainsAny(name, "/\\") {
		return errno.Wrap(errno.Invalid, "会话名不能包含路径分隔符")
	}

	if strings.TrimSpace(name) != name {
		return errno.Wrap(errno.Invalid, "会话名不能有首尾空白")
	}
	return nil
}

func (s *Store) Save(ctx context.Context, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.sessionPath(name)
	if err != nil {
		return err
	}
	// 单文件自校验容器 + 单次原子重命名(I17):任意步骤中断后,
	// 目标要么保持旧版本完整,要么保持新版本完整。
	return s.commitAtomic(ctx, target, encodeSession(data))
}

// commitAtomic 以"临时文件→fsync→重命名→目录 fsync"提交内容;
// 每一步失败都回滚临时文件且不触碰目标。
func (s *Store) commitAtomic(ctx context.Context, target string, content []byte) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := path.Dir(target)
	temp, err := s.commit.CreateTemp(dir, tempPrefix+path.Base(target)+"-*")
	if err != nil {
		return errno.Wrap(err, "创建临时文件")
	}
	tempName := temp.Name()

	closed := false
	committed := false
	defer func() {
		var closeErr error
		if !closed {
			closeErr = s.commit.Close(temp)
		}
		var removeErr error
		if !committed {
			removeErr = s.commit.Remove(tempName)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
		}
		resultErr = errors.Join(resultErr, closeErr, removeErr)
	}()
	if err := s.commit.Write(temp, content); err != nil {
		return errno.Wrap(err, "写入临时文件")
	}

	if err := s.commit.Sync(temp); err != nil {
		return errno.Wrap(err, "同步临时文件")
	}
	if err := s.commit.Close(temp); err != nil {
		return errno.Wrap(err, "关闭临时文件")
	}
	closed = true
	if err := s.commit.Chmod(tempName, 0o600); err != nil {
		return errno.Wrap(err, "设置文件权限")
	}
	if err := s.commit.Rename(tempName, target); err != nil {
		return errno.Wrap(err, "提交重命名")
	}
	committed = true

	if err := s.commit.SyncDir(dir); err != nil {
		return errno.Wrap(err, "同步目录")
	}
	return nil
}

func (s *Store) Load(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.sessionPath(name)
	if err != nil {
		return nil, err
	}

	if err := s.rejectLeftovers(ctx); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errno.Wrap(errno.NotFound, "会话不存在: "+name)
		}
		return nil, errno.Wrap(err, "读取会话")
	}
	container, ok := decodeSession(raw)
	// 已定义的损坏状态:容器格式非法或摘要不符——明确报错,
	// 不返回未验证内容。
	if !ok {
		return nil, errno.Wrap(errno.Version, "会话容器格式非法: "+name)
	}
	if !container.verify() {
		return nil, errno.Wrap(errno.Version, "会话摘要不符，内容可能损坏: "+name)
	}
	return container.data, nil
}

func (s *Store) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.sessionPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return errno.Wrap(errno.NotFound, "会话不存在: "+name)
		}
		return errno.Wrap(err, "删除会话内容")
	}
	return s.syncRoot(ctx)
}

func (s *Store) syncRoot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.commit.SyncDir(s.root); err != nil {
		return errno.Wrap(err, "同步目录")
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, errno.Wrap(err, "读取存储目录")
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		entryName := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(entryName, sessionSuffix) {
			continue
		}
		if strings.HasPrefix(entryName, tempPrefix) {
			continue
		}
		// 只列可能完整的会话:容器摘要行存在(Load 为权威校验)。
		// entryName 来自本存储根的 ReadDir(不含路径分隔符),
		// 受控输入。
		raw, err := os.ReadFile(path.Join(s.root, entryName)) // #nosec G304 -- 目录枚举得到的名,不含分隔符
		if err != nil {
			continue
		}
		if _, ok := decodeSession(raw); !ok {
			continue
		}
		names = append(names, strings.TrimSuffix(entryName, sessionSuffix))
	}
	sortStrings(names)
	return names, nil
}

func (s *Store) Put(ctx context.Context, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	digest := digestOf(data)
	target := s.snapshotPath(digest)

	if _, err := os.Stat(target); err == nil {
		return digest, nil
	} else if !os.IsNotExist(err) {
		return "", errno.Wrap(err, "检查快照")
	}
	// 内容寻址:文件名即摘要,Get 时复核——任意步骤中断后快照
	// 要么不存在,要么完整(临时残留由重启恢复丢弃)。
	if err := s.commitAtomic(ctx, target, data); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) Get(ctx context.Context, digest string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(digest, digestPrefix) {
		return nil, errno.Wrap(errno.Invalid, "快照标识格式非法")
	}
	encoded := strings.TrimPrefix(digest, digestPrefix)

	if len(encoded) != 64 || !isHex(encoded) {
		return nil, errno.Wrap(errno.Invalid, "快照标识格式非法")
	}
	data, err := os.ReadFile(s.snapshotPath(digest))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errno.Wrap(errno.NotFound, "快照不存在")
		}
		return nil, errno.Wrap(err, "读取快照")
	}

	if digestOf(data) != digest {
		return nil, errno.Wrap(errno.Version, "快照内容与标识不符")
	}
	return data, nil
}

func isHex(text string) bool {
	for index := 0; index < len(text); index++ {
		switch {
		case text[index] >= '0' && text[index] <= '9':
		case text[index] >= 'a' && text[index] <= 'f':
		default:
			return false
		}
	}
	return true
}

func (s *Store) snapshotPath(digest string) string {
	encoded := strings.TrimPrefix(digest, digestPrefix)
	return path.Join(s.root, snapshotDir, encoded)
}

func (s *Store) rejectLeftovers(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return errno.Wrap(err, "读取存储目录")
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) {
			return errno.Wrap(errno.Version,
				"存在未完成的提交残留，请先确认存储状态: "+entry.Name())
		}
	}
	return nil
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return digestPrefix + hex.EncodeToString(sum[:])
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position++ {
			values[position], values[position-1] = values[position-1], values[position]
		}
	}
}

var (
	_ interface {
		Load(context.Context, string) ([]byte, error)
		Save(context.Context, string, []byte) error
		Delete(context.Context, string) error
	} = (*Store)(nil)
	_ interface {
		Get(context.Context, string) ([]byte, error)
		Put(context.Context, []byte) (string, error)
	} = (*Store)(nil)
)
