package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	stdfs "io/fs"
	"slices"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

const EnvelopeFormat = "assistant-sandbox-snapshot-1"

type Envelope struct {
	Format string            `json:"format"`
	Digest string            `json:"digest"`
	FSSha  string            `json:"fs_sha256"`
	Lang   string            `json:"language,omitempty"`
	Env    map[string]string `json:"environment,omitempty"`

	CursorPos uint64 `json:"replay_cursor,omitempty"`

	snapshot vmfs.Snapshotted `json:"-"`

	ProcTable []byte `json:"proc_table,omitempty"`

	FSBody []byte `json:"fs_body,omitempty"`
}

type SnapshotState interface {
	QuietPoint() error

	ProcTable(context.Context) ([]byte, error)

	RestoreProcTable(context.Context, []byte) error
}

func (e *Envelope) HasProcTable() bool { return e != nil && len(e.ProcTable) > 0 }

func (e *Envelope) CaptureState(ctx context.Context, state SnapshotState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil {
		return errno.Invalid
	}
	if state == nil {
		return errno.Wrap(errno.Invalid, "缺少运行期状态来源")
	}

	if err := state.QuietPoint(); err != nil {
		return err
	}
	table, err := state.ProcTable(ctx)
	if err != nil {
		return err
	}
	if len(table) == 0 {
		return errno.Wrap(errno.Invalid, "进程表快照为空")
	}
	e.ProcTable = table
	return nil
}

func (e *Envelope) RestoreState(ctx context.Context, state SnapshotState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || state == nil {
		return errno.Invalid
	}
	if !e.HasProcTable() {
		return errno.Wrap(errno.Invalid, "信封不含进程表")
	}
	if e.Format != EnvelopeFormat {
		return errno.Wrap(errno.Version, "快照信封格式为 "+e.Format)
	}

	if err := state.QuietPoint(); err != nil {
		return err
	}
	if err := state.RestoreProcTable(ctx, e.ProcTable); err != nil {
		return errno.Wrap(err, "恢复进程表")
	}
	return nil
}

type FSSnapshotEncoder interface {
	Encode(context.Context) ([]byte, error)
}

type FSSnapshotDecoder func(context.Context, []byte) (vmfs.Snapshotted, error)

func CaptureEncoded(ctx context.Context, source Source) (*Envelope, error) {
	envelope, err := Capture(ctx, source)
	if err != nil {
		return nil, err
	}
	encoder, ok := envelope.snapshot.(FSSnapshotEncoder)
	if !ok {
		return nil, errno.Wrap(errno.NotSupported, "文件系统后端不支持快照序列化")
	}
	body, err := encoder.Encode(ctx)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errno.Wrap(errno.Invalid, "文件系统快照本体为空")
	}
	envelope.FSBody = body
	return envelope, nil
}

func (e *Envelope) HasFSBody() bool {
	return e != nil && len(e.FSBody) > 0
}

func (e *Envelope) ResumeEncoded(ctx context.Context, target vmfs.System, decode FSSnapshotDecoder) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || decode == nil {
		return errno.Invalid
	}
	if !e.HasFSBody() {
		return errno.Wrap(errno.Invalid, "信封不含文件系统快照本体")
	}
	snapshot, err := decode(ctx, e.FSBody)
	if err != nil {
		return errno.Wrap(err, "解码文件系统快照本体")
	}
	return e.Resume(ctx, target, snapshot)
}

type Source struct {
	Filesystem vmfs.System

	Language string

	Environment map[string]string

	Cursor uint64
}

func Capture(ctx context.Context, source Source) (*Envelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source.Filesystem == nil {
		return nil, errno.Invalid
	}
	snapshottable, ok := source.Filesystem.(vmfs.Snapshotter)
	if !ok {
		return nil, errno.NotSupported
	}
	snapshotted, err := snapshottable.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	fsSHA, err := filesystemDigest(ctx, source.Filesystem)
	if err != nil {
		return nil, err
	}
	envelope := &Envelope{
		Format:    EnvelopeFormat,
		FSSha:     fsSHA,
		Lang:      source.Language,
		Env:       cloneEnvironment(source.Environment),
		CursorPos: source.Cursor,
		snapshot:  snapshotted,
	}
	if envelope.Lang == "" {
		envelope.Lang = defaultLanguage
	}
	digest, err := envelope.computeDigest()
	if err != nil {
		return nil, err
	}
	envelope.Digest = digest
	return envelope, nil
}

const defaultLanguage = "bash"

type digestPayload struct {
	Format string            `json:"format"`
	FSSha  string            `json:"fs_sha256"`
	Lang   string            `json:"language"`
	Env    map[string]string `json:"environment"`
	Cursor uint64            `json:"replay_cursor"`
}

func (e *Envelope) computeDigest() (string, error) {
	payload := digestPayload{
		Format: e.Format,
		FSSha:  e.FSSha,
		Lang:   e.Lang,
		Env:    sortedEnvironment(e.Env),
		Cursor: e.CursorPos,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", errno.Wrap(err, "编码快照摘要")
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (e *Envelope) Encode() ([]byte, error) {
	if e == nil {
		return nil, errno.Invalid
	}
	return json.Marshal(e)
}

func Decode(data []byte) (*Envelope, error) {
	if len(data) == 0 {
		return nil, errno.Invalid
	}
	envelope := &Envelope{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(envelope); err != nil {
		return nil, errno.Wrap(err, "解析快照信封")
	}
	if envelope.Format != EnvelopeFormat {
		return nil, errno.Wrap(errno.Version, "快照信封格式为 "+envelope.Format)
	}
	if envelope.Digest == "" {
		return nil, errno.Invalid
	}
	digest, err := envelope.computeDigest()
	if err != nil {
		return nil, err
	}
	if digest != envelope.Digest {
		return nil, errno.Wrap(errno.Version, "快照摘要不一致")
	}
	if envelope.Lang == "" {
		envelope.Lang = defaultLanguage
	}
	return envelope, nil
}

func (e *Envelope) Resume(ctx context.Context, target vmfs.System, snapshot vmfs.Snapshotted) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || snapshot == nil {
		return errno.Invalid
	}
	if e.Format != EnvelopeFormat {
		return errno.Wrap(errno.Version, "快照信封格式为 "+e.Format)
	}
	if target == nil {
		return errno.Invalid
	}
	fsSHA, err := filesystemDigest(ctx, target)
	if err != nil {
		return err
	}
	if e.FSSha != "" && fsSHA != e.FSSha {
		return errno.Wrap(errno.Version, "目标文件系统与快照不匹配")
	}
	if err := snapshot.Restore(ctx, target); err != nil {
		return errno.Wrap(err, "回放文件系统快照")
	}
	return nil
}

func (e *Envelope) LanguageID() string {
	if e == nil || e.Lang == "" {
		return defaultLanguage
	}
	return e.Lang
}

func (e *Envelope) Snapshot() vmfs.Snapshotted {
	if e == nil {
		return nil
	}
	return e.snapshot
}

func (e *Envelope) EnvironmentCopy() map[string]string {
	if e == nil {
		return nil
	}
	return cloneEnvironment(e.Env)
}

func (e *Envelope) Cursor() uint64 {
	if e == nil {
		return 0
	}
	return e.CursorPos
}

func filesystemDigest(ctx context.Context, system vmfs.System) (string, error) {
	if system == nil {
		return "", errno.Invalid
	}
	hasher := sha256.New()
	if err := walkDigest(ctx, system, "/", hasher, 0); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

const maxDigestDepth = 64

func walkDigest(ctx context.Context, system vmfs.System, dir string, hasher hashWriter, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > maxDigestDepth {
		return errno.LinkLoop
	}
	entries, err := system.ReadDir(ctx, dir)
	if err != nil {
		return errno.Wrap(err, "读取目录 "+dir)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	for _, name := range names {
		path := dir
		if path != "/" {
			path += "/"
		}
		path += name
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := system.Lstat(ctx, path)
		if err != nil {
			return errno.Wrap(err, "读取元数据 "+path)
		}
		if err := writeDigestField(hasher, path); err != nil {
			return err
		}
		if err := writeDigestField(hasher, info.Mode().String()); err != nil {
			return err
		}
		if info.IsDir() {
			if err := walkDigest(ctx, system, path, hasher, depth+1); err != nil {
				return err
			}
			continue
		}
		if info.Mode()&stdfs.ModeSymlink != 0 {
			target, err := system.Readlink(ctx, path)
			if err != nil {
				return errno.Wrap(err, "读取链接 "+path)
			}
			if err := writeDigestField(hasher, "->"+target); err != nil {
				return err
			}
			continue
		}
		file, err := system.Open(ctx, path, vmfs.ReadOnly, 0)
		if err != nil {
			return errno.Wrap(err, "打开 "+path)
		}
		content, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil {
			return errno.Wrap(readErr, "读取 "+path)
		}
		if closeErr != nil {
			return errno.Wrap(closeErr, "关闭 "+path)
		}
		if _, err := hasher.Write(content); err != nil {
			return errno.Wrap(err, "计算内容摘要 "+path)
		}
		if err := writeDigestField(hasher, ""); err != nil {
			return err
		}
	}
	return nil
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeDigestField(hasher hashWriter, value string) error {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	if _, err := hasher.Write(length[:]); err != nil {
		return errno.Wrap(err, "写入摘要字段长度")
	}
	if _, err := hasher.Write([]byte(value)); err != nil {
		return errno.Wrap(err, "写入摘要字段")
	}
	return nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	cloned := make(map[string]string, len(environment))
	for name, value := range environment {
		cloned[name] = value
	}
	return cloned
}

func sortedEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return map[string]string{}
	}
	return cloneEnvironment(environment)
}
