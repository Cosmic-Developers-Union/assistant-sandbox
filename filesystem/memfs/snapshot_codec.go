package memfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stdfs "io/fs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

type Encodable interface {
	Encode(context.Context) ([]byte, error)
}

const SnapshotFormat = "assistant-sandbox-memfs-snapshot-1"

type nodeRecord struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Atime  int64  `json:"atime"`
	Mtime  int64  `json:"mtime"`
	Data   []byte `json:"data,omitempty"`
	Target string `json:"target,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type snapshotPayload struct {
	Format string       `json:"format"`
	Nodes  []nodeRecord `json:"nodes"`
	Digest string       `json:"digest"`
}

const (
	kindNameFile    = "file"
	kindNameDir     = "dir"
	kindNameSymlink = "symlink"
	kindNameFifo    = "fifo"
)

func kindName(kind inodeKind) (string, bool) {
	switch kind {
	case kindFile:
		return kindNameFile, true
	case kindDir:
		return kindNameDir, true
	case kindSymlink:
		return kindNameSymlink, true
	case kindFifo:
		return kindNameFifo, true
	default:
		return "", false
	}
}

func kindFromName(name string) (inodeKind, bool) {
	switch name {
	case kindNameFile:
		return kindFile, true
	case kindNameDir:
		return kindDir, true
	case kindNameSymlink:
		return kindSymlink, true
	case kindNameFifo:
		return kindFifo, true
	default:
		return 0, false
	}
}

func (s *snapshot) Encode(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.root == nil {
		return nil, errno.Wrap(errno.Invalid, "快照为空")
	}
	records := make([]nodeRecord, 0, 16)
	if err := collectRecords(ctx, s.root, "/", &records); err != nil {
		return nil, err
	}
	sortRecords(records)
	payload := snapshotPayload{Format: SnapshotFormat, Nodes: records}
	digest, err := payloadDigest(records)
	if err != nil {
		return nil, err
	}
	payload.Digest = digest
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, errno.Wrap(err, "编码内存文件系统快照")
	}
	return encoded, nil
}

func collectRecords(ctx context.Context, node *inode, path string, out *[]nodeRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, ok := kindName(node.kind)
	if !ok {
		return errno.Wrap(errno.Invalid, "未知节点类型")
	}
	record := nodeRecord{
		Path:  path,
		Kind:  name,
		Mode:  uint32(node.mode.Perm()),
		Atime: node.atime,
		Mtime: node.mtime,
	}
	switch node.kind {
	case kindSymlink:
		record.Target = node.target
	case kindFile:
		record.Data = append([]byte(nil), node.data...)
	}
	*out = append(*out, record)
	for childName, child := range node.children {
		if err := collectRecords(ctx, child, joinPath(path, childName), out); err != nil {
			return err
		}
	}
	return nil
}

func joinPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return parent + "/" + name
}

func sortRecords(records []nodeRecord) {
	for index := 1; index < len(records); index++ {
		for position := index; position > 0 && records[position].Path < records[position-1].Path; position-- {
			records[position], records[position-1] = records[position-1], records[position]
		}
	}
}

func payloadDigest(records []nodeRecord) (string, error) {
	encoded, err := json.Marshal(records)
	if err != nil {
		return "", errno.Wrap(err, "计算快照摘要")
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func DecodeSnapshot(ctx context.Context, data []byte) (vmfs.Snapshotted, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errno.Wrap(errno.Invalid, "快照字节为空")
	}
	var payload snapshotPayload
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errno.Wrap(errno.Invalid, "解析内存文件系统快照: "+err.Error())
	}
	if payload.Format != SnapshotFormat {
		return nil, errno.Wrap(errno.Version, "内存文件系统快照格式为 "+payload.Format)
	}
	digest, err := payloadDigest(payload.Nodes)
	if err != nil {
		return nil, err
	}
	if payload.Digest != digest {
		return nil, errno.Wrap(errno.Version, "内存文件系统快照摘要不符")
	}

	for _, record := range payload.Nodes {
		if _, ok := kindFromName(record.Kind); !ok {
			return nil, errno.Wrap(errno.Invalid, "未知节点类型 "+record.Kind)
		}
		if record.Path == "" || record.Path[0] != '/' {
			return nil, errno.Wrap(errno.Invalid, "节点路径不是客体绝对路径: "+record.Path)
		}
	}
	root, err := rebuildTree(ctx, payload.Nodes)
	if err != nil {
		return nil, err
	}
	return &snapshot{root: root}, nil
}

func rebuildTree(ctx context.Context, records []nodeRecord) (*inode, error) {
	if len(records) == 0 {
		return nil, errno.Wrap(errno.Invalid, "快照不含任何节点")
	}
	if records[0].Path != "/" {
		return nil, errno.Wrap(errno.Invalid, "快照缺少根节点")
	}
	byPath := make(map[string]*inode, len(records))
	root, err := nodeFromRecord(records[0])
	if err != nil {
		return nil, err
	}
	if root.kind != kindDir {
		return nil, errno.Wrap(errno.Invalid, "根节点必须是目录")
	}
	root.name = ""
	root.parent = nil
	byPath["/"] = root
	for _, record := range records[1:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, duplicate := byPath[record.Path]; duplicate {
			return nil, errno.Wrap(errno.Invalid, "快照含重复路径 "+record.Path)
		}
		parentPath, name := splitPath(record.Path)
		if name == "" {
			return nil, errno.Wrap(errno.Invalid, "快照路径非法 "+record.Path)
		}
		parent, ok := byPath[parentPath]
		if !ok {
			return nil, errno.Wrap(errno.Invalid, "快照缺少父目录 "+parentPath)
		}
		node, err := nodeFromRecord(record)
		if err != nil {
			return nil, err
		}
		node.name = name
		node.parent = parent
		if parent.children == nil {
			parent.children = map[string]*inode{}
		}
		parent.children[name] = node
		byPath[record.Path] = node
	}
	fixParents(root)

	assignIDs(root, 1)
	return root, nil
}

func assignIDs(node *inode, next uint64) uint64 {
	node.id = next
	next++

	node.nlink = 1
	for _, child := range node.children {
		if child.kind == kindDir {
			node.nlink++
		}
		next = assignIDs(child, next)
	}
	return next
}

func stdfsMode(permissions uint32, kind inodeKind) stdfs.FileMode {
	mode := stdfs.FileMode(permissions).Perm()
	switch kind {
	case kindDir:
		return stdfs.ModeDir | mode
	case kindSymlink:
		return stdfs.ModeSymlink | mode
	case kindFifo:
		return stdfs.ModeNamedPipe | mode
	default:
		return mode
	}
}

func nodeFromRecord(record nodeRecord) (*inode, error) {
	kind, ok := kindFromName(record.Kind)
	if !ok {
		return nil, errno.Wrap(errno.Invalid, "未知节点类型 "+record.Kind)
	}
	node := &inode{
		kind:  kind,
		mode:  stdfsMode(record.Mode, kind),
		atime: record.Atime,
		mtime: record.Mtime,
	}
	switch kind {
	case kindSymlink:
		if record.Target == "" {
			return nil, errno.Wrap(errno.Invalid, "符号链接缺少目标 "+record.Path)
		}
		node.target = record.Target
	case kindFile:
		node.data = append([]byte(nil), record.Data...)
	case kindDir:
		node.children = map[string]*inode{}
	}
	return node, nil
}

func splitPath(path string) (string, string) {
	if path == "" || path[0] != '/' {
		return "", ""
	}
	if path == "/" {
		return "", ""
	}
	for index := len(path) - 1; index > 0; index-- {
		if path[index] == '/' {
			parent := path[:index]
			if parent == "" {
				parent = "/"
			}
			return parent, path[index+1:]
		}
	}

	return "/", path[1:]
}
