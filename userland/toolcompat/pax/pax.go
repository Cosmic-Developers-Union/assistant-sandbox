package pax

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

type Kind string

const (
	KindFile     Kind = "file"
	KindDir      Kind = "dir"
	KindSymlink  Kind = "symlink"
	KindHardlink Kind = "hardlink"
	KindFIFO     Kind = "fifo"
	KindOther    Kind = "other"
)

type Member struct {
	Path       string
	Kind       Kind
	Linkname   string
	Size       int64
	Mode       fs.FileMode
	ModTime    clock.Time
	Format     tar.Format
	PAXRecords map[string]string
}

type PlannedMember struct {
	Member
	Target string

	Index int
}

type RejectedMember struct {
	Member
	Reason string
	Index  int
}

type Plan struct {
	Root       string
	Members    []PlannedMember
	Rejected   []RejectedMember
	Superseded []Member
	Unsafe     bool
	Formats    []tar.Format
	FormatsBy  map[tar.Format]int
}

var ErrDestinationExists = errors.New("destination exists")

func IsDestinationExists(reason string) bool {
	return strings.Contains(reason, ErrDestinationExists.Error())
}

type FS interface {
	Lstat(path string) (fs.FileInfo, error)
	Readlink(path string) (string, error)
}

type VFS struct{}

func (VFS) Lstat(name string) (fs.FileInfo, error) { return vfs.Lstat(name) }
func (VFS) Readlink(name string) (string, error)   { return vfs.Readlink(name) }

func ReadManifest(r io.Reader) ([]Member, error) {
	tr := tar.NewReader(r)
	var out []Member
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, memberFromHeader(hdr))
	}
}

func PlanExtraction(r io.Reader, root string, filesystem FS) (*Plan, error) {
	if err := validateRoot(root, filesystem); err != nil {
		return nil, err
	}
	if filesystem == nil {
		filesystem = VFS{}
	}
	root = path.Clean(root)
	plan := &Plan{Root: root, FormatsBy: map[tar.Format]int{}}
	entries := make([]candidate, 0)
	byTarget := make(map[string][]int)
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		member := memberFromHeader(hdr)
		plan.FormatsBy[member.Format]++
		entry := candidate{member: member}
		target, err := secureJoin(root, member.Path)
		if err != nil {
			entry.reject(err.Error())
		} else {
			entry.target = target
			byTarget[target] = append(byTarget[target], len(entries))
			switch member.Kind {
			case KindOther:
				entry.reject("unsupported special archive entry")
			case KindSymlink:
				if _, err := resolveLinkWithinRoot(root, target, member.Linkname); err != nil {
					entry.reject(err.Error())
				}
			case KindHardlink:
				linkTarget, err := secureJoin(root, member.Linkname)
				if err != nil {
					entry.reject(fmt.Sprintf("hardlink target %q: %v", member.Linkname, err))
				} else {
					entry.linkTarget = linkTarget
				}
			}
		}
		entries = append(entries, entry)
	}

	superseded := make(map[int]bool)
	for target, indexes := range byTarget {
		if len(indexes) < 2 {
			continue
		}
		kind := effectiveUpdateKind(entries[indexes[0]].member.Kind)
		uniform := true
		for _, i := range indexes {
			if effectiveUpdateKind(entries[i].member.Kind) != kind {
				uniform = false
				break
			}
		}
		if !uniform {
			for _, i := range indexes {
				entries[i].reject(fmt.Sprintf("duplicate destination %q", target))
			}
			continue
		}
		for _, i := range indexes[:len(indexes)-1] {

			if entries[i].reason == "" {
				superseded[i] = true
			}
		}
	}

	valid := make(map[string]int)
	for i := range entries {
		if entries[i].reason != "" || superseded[i] {
			continue
		}
		if err := validateExistingTarget(filesystem, entries[i].target, entries[i].member.Kind); err != nil {
			entries[i].reject(err.Error())
			continue
		}
		if err := validateExistingParents(filesystem, root, entries[i].target); err != nil {
			entries[i].reject(err.Error())
			continue
		}
		valid[entries[i].target] = i
	}

	for changed := true; changed; {
		changed = false
		for i := range entries {
			if entries[i].reason != "" || superseded[i] {
				continue
			}
			if err := validateArchiveParents(entries[i].target, root, valid, entries); err != nil {
				entries[i].reject(err.Error())
				delete(valid, entries[i].target)
				changed = true
			}
		}
	}

	for changed := true; changed; {
		changed = false
		for i := range entries {
			if entries[i].reason != "" || superseded[i] || entries[i].member.Kind != KindHardlink {
				continue
			}
			if err := validateHardlink(i, root, entries, valid, filesystem); err != nil {
				entries[i].reject(err.Error())
				delete(valid, entries[i].target)
				changed = true
			}
		}
	}
	for i, entry := range entries {
		if entry.reason != "" {
			plan.Rejected = append(plan.Rejected, RejectedMember{Member: entry.member, Reason: entry.reason, Index: i})
			continue
		}
		if superseded[i] {
			plan.Superseded = append(plan.Superseded, entry.member)
			continue
		}
		plan.Members = append(plan.Members, PlannedMember{Member: entry.member, Target: entry.target, Index: i})
	}
	plan.Unsafe = len(plan.Rejected) != 0
	plan.Formats = sortedFormats(plan.FormatsBy)
	return plan, nil
}

func effectiveUpdateKind(kind Kind) Kind {
	if kind == KindHardlink {
		return KindFile
	}
	return kind
}

type candidate struct {
	member     Member
	target     string
	linkTarget string
	reason     string
}

func (c *candidate) reject(reason string) {
	if c.reason == "" {
		c.reason = reason
	}
}

func validateRoot(root string, filesystem FS) error {
	if root == "" {
		return fmt.Errorf("pax: destination root is required")
	}
	if !path.IsAbs(root) {
		return fmt.Errorf("pax: destination root %q must be absolute", root)
	}
	if filesystem == nil {
		filesystem = VFS{}
	}
	fi, err := filesystem.Lstat(path.Clean(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pax: inspect destination root: %w", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("pax: destination root %q must be a directory, not a symlink", root)
	}
	return nil
}

func validateExistingTarget(filesystem FS, target string, kind Kind) error {
	fi, err := filesystem.Lstat(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect existing target %q: %w", target, err)
	}
	if kind == KindDir && fi.IsDir() && fi.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	return fmt.Errorf("%w: destination %q already exists", ErrDestinationExists, target)
}

func validateExistingParents(filesystem FS, root, target string) error {
	parts, err := parentParts(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, part := range parts {
		current = path.Join(current, part)
		fi, err := filesystem.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect parent %q: %w", current, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("member %q would traverse existing symlink parent %q", target, current)
		}
		if !fi.IsDir() {
			return fmt.Errorf("member %q has non-directory parent %q", target, current)
		}
	}
	return nil
}

func validateArchiveParents(target, root string, valid map[string]int, entries []candidate) error {
	parts, err := parentParts(root, target)
	if err != nil {
		return err
	}
	current := root
	for _, part := range parts {
		current = path.Join(current, part)
		if i, ok := valid[current]; ok {
			switch entries[i].member.Kind {
			case KindDir:
			case KindSymlink:
				return fmt.Errorf("member %q would traverse archive symlink parent %q", target, current)
			default:
				return fmt.Errorf("member %q has archive non-directory parent %q", target, current)
			}
		}
	}
	return nil
}

func validateHardlink(i int, root string, entries []candidate, valid map[string]int, filesystem FS) error {
	seen := map[int]bool{}
	for {
		if seen[i] {
			return fmt.Errorf("hardlink %q has a cyclic archive source", entries[i].member.Path)
		}
		seen[i] = true
		target := entries[i].linkTarget
		if source, ok := valid[target]; ok {
			switch entries[source].member.Kind {
			case KindFile:
				return nil
			case KindHardlink:
				i = source
				continue
			default:
				return fmt.Errorf("hardlink target %q is not a regular file", entries[i].member.Linkname)
			}
		}
		if err := validateExistingParents(filesystem, root, target); err != nil {
			return fmt.Errorf("hardlink target %q: %w", entries[i].member.Linkname, err)
		}
		fi, err := filesystem.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("hardlink target %q does not exist in the archive or destination", entries[i].member.Linkname)
		}
		if err != nil {
			return fmt.Errorf("inspect hardlink target %q: %w", entries[i].member.Linkname, err)
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return fmt.Errorf("hardlink target %q is not a regular file", entries[i].member.Linkname)
		}
		return nil
	}
}

func parentParts(root, target string) ([]string, error) {
	rel, err := relativeTo(root, target)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(path.Dir(rel), "/")
	if len(parts) == 1 && parts[0] == "." {
		return nil, nil
	}
	return parts, nil
}

func memberFromHeader(hdr *tar.Header) Member {
	return Member{Path: hdr.Name, Kind: kindFromHeader(hdr), Linkname: hdr.Linkname, Size: hdr.Size, Mode: hdr.FileInfo().Mode(), ModTime: clock.Unix(hdr.ModTime.Unix(), int64(hdr.ModTime.Nanosecond())), Format: hdr.Format, PAXRecords: cloneMap(hdr.PAXRecords)}
}

func kindFromHeader(hdr *tar.Header) Kind {
	switch hdr.Typeflag {
	case tar.TypeReg, 0:
		return KindFile
	case tar.TypeDir:
		return KindDir
	case tar.TypeSymlink:
		return KindSymlink
	case tar.TypeLink:
		return KindHardlink
	case tar.TypeFifo:
		return KindFIFO
	default:
		return KindOther
	}
}

func secureJoin(root, member string) (string, error) {
	name := strings.TrimSuffix(member, "/")
	if name == "" || name == "." {
		return "", fmt.Errorf("empty member path")
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("absolute member path %q", member)
	}
	if path.IsAbs(name) {
		return "", fmt.Errorf("absolute member path %q", member)
	}
	target := path.Join(root, name)
	if !withinRoot(root, target) || samePath(root, target) {
		return "", fmt.Errorf("member path %q escapes or names root", member)
	}
	return target, nil
}

func resolveLinkWithinRoot(root, linkPath, linkname string) (string, error) {
	if strings.TrimSpace(linkname) == "" {
		return "", fmt.Errorf("empty symlink target")
	}
	if path.IsAbs(linkname) {
		return "", fmt.Errorf("symlink target %q is absolute", linkname)
	}
	target := path.Clean(path.Join(path.Dir(linkPath), linkname))
	if !withinRoot(root, target) {
		return "", fmt.Errorf("symlink target %q escapes root", linkname)
	}
	return target, nil
}

func withinRoot(root, target string) bool {
	root, target = path.Clean(root), path.Clean(target)
	if samePath(root, target) {
		return true
	}

	return strings.HasPrefix(target, root+"/")
}

func relativeTo(root, target string) (string, error) {
	root, target = path.Clean(root), path.Clean(target)
	if root == target {
		return ".", nil
	}
	if !strings.HasPrefix(target, root+"/") {
		return "", fmt.Errorf("%q 不在 %q 内", target, root)
	}
	return strings.TrimPrefix(target, root+"/"), nil
}

func samePath(a, b string) bool {
	return path.Clean(a) == path.Clean(b)
}

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedFormats(counts map[tar.Format]int) []tar.Format {
	if len(counts) == 0 {
		return nil
	}
	out := make([]tar.Format, 0, len(counts))
	for f := range counts {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
