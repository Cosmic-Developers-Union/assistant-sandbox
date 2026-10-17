package persistent

import (
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
	sessionSuffix  = ".session"
	checksumSuffix = ".sha256"
	snapshotDir    = "snapshots"
	tempPrefix     = ".tmp-"

	digestPrefix = "sha256:"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, errno.Wrap(errno.Invalid, "存储根目录不能为空")
	}
	store := &Store{root: root}
	for _, dir := range []string{root, path.Join(root, snapshotDir)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, errno.Wrap(err, "创建存储目录")
		}
	}
	return store, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) sessionPath(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	return path.Join(s.root, name+sessionSuffix), nil
}

func (s *Store) checksumPath(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	return path.Join(s.root, name+sessionSuffix+checksumSuffix), nil
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
	checksum, err := s.checksumPath(name)
	if err != nil {
		return err
	}

	if err := writeAtomic(ctx, target, data); err != nil {
		return err
	}
	return writeAtomic(ctx, checksum, []byte(digestOf(data)))
}

func (s *Store) Load(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := s.sessionPath(name)
	if err != nil {
		return nil, err
	}
	checksum, err := s.checksumPath(name)
	if err != nil {
		return nil, err
	}

	if err := s.rejectLeftovers(ctx); err != nil {
		return nil, err
	}
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return nil, errno.Wrap(errno.NotFound, "会话不存在: "+name)
		}
		return nil, errno.Wrap(err, "读取会话")
	}

	expected, err := os.ReadFile(checksum)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errno.Wrap(errno.Version, "会话缺少摘要，无法验证完整性: "+name)
		}
		return nil, errno.Wrap(err, "读取会话摘要")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, errno.Wrap(err, "读取会话内容")
	}
	if digestOf(data) != strings.TrimSpace(string(expected)) {
		return nil, errno.Wrap(errno.Version, "会话摘要不符，内容可能损坏: "+name)
	}
	return data, nil
}

func (s *Store) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := s.sessionPath(name)
	if err != nil {
		return err
	}
	checksum, err := s.checksumPath(name)
	if err != nil {
		return err
	}
	contentErr := os.Remove(target)

	if err := os.Remove(checksum); err != nil && !os.IsNotExist(err) {
		return errno.Wrap(err, "删除会话摘要")
	}
	if contentErr != nil {
		if os.IsNotExist(contentErr) {
			return errno.Wrap(errno.NotFound, "会话不存在: "+name)
		}
		return errno.Wrap(contentErr, "删除会话内容")
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

		if strings.HasSuffix(entryName, sessionSuffix+checksumSuffix) {
			continue
		}
		if strings.HasPrefix(entryName, tempPrefix) {
			continue
		}
		name := strings.TrimSuffix(entryName, sessionSuffix)
		checksum := path.Join(s.root, entryName+checksumSuffix)
		if _, err := os.Stat(checksum); err != nil {
			continue
		}
		names = append(names, name)
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
	if err := writeAtomic(ctx, target, data); err != nil {
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

func writeAtomic(ctx context.Context, target string, data []byte) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := path.Dir(target)
	temp, err := os.CreateTemp(dir, tempPrefix+path.Base(target)+"-*")
	if err != nil {
		return errno.Wrap(err, "创建临时文件")
	}
	tempName := temp.Name()

	closed := false
	committed := false
	defer func() {
		var closeErr error
		if !closed {
			closeErr = temp.Close()
		}
		var removeErr error
		if !committed {
			removeErr = os.Remove(tempName)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
		}
		resultErr = errors.Join(resultErr, closeErr, removeErr)
	}()
	if _, err := temp.Write(data); err != nil {
		return errno.Wrap(err, "写入临时文件")
	}

	if err := temp.Sync(); err != nil {
		return errno.Wrap(err, "同步临时文件")
	}
	if err := temp.Close(); err != nil {
		return errno.Wrap(err, "关闭临时文件")
	}
	closed = true
	if err := os.Chmod(tempName, 0o600); err != nil {
		return errno.Wrap(err, "设置文件权限")
	}
	if err := os.Rename(tempName, target); err != nil {
		return errno.Wrap(err, "提交重命名")
	}
	committed = true

	return syncDir(dir)
}

func syncDir(dir string) (resultErr error) {
	handle, err := os.Open(dir)
	if err != nil {
		return errno.Wrap(err, "打开目录以同步")
	}
	defer func() {
		resultErr = errors.Join(resultErr, handle.Close())
	}()
	if err := handle.Sync(); err != nil {

		return errno.Wrap(errno.NotSupported, "宿主文件系统不支持目录同步")
	}
	return nil
}

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return digestPrefix + hex.EncodeToString(sum[:])
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position-- {
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
