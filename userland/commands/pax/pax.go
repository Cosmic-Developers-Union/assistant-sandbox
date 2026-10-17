package paxcmd

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/pax"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
)

var cmd = &toolcompat.Tool{
	Name:     "pax",
	Synopsis: "Read POSIX archive members (portable archive interchange).",
	Usage: `pax [-cdnv] [-H|-L] [-f archive] [pattern...]
  pax -r [-cdknuv] [-H|-L] [-f archive] [pattern...]
  pax -w [-adituvX] [-H|-L] [-f archive] [-x format] [file...]`,
}

func init() { cmd.Run = run; toolcompat.Register(cmd) }

const sixMonths = 15778476 * 1e9

type options struct {
	read          bool
	write         bool
	archive       string
	verbose       bool
	format        string
	keep          bool
	update        bool
	first         bool
	complement    bool
	append        bool
	dirsNoDescend bool
	followCmdline bool
	followAll     bool
	operands      []string
}

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)

	fs.SetInterspersed(false)
	var o options
	fs.BoolVarP(&o.read, "read", "r", false, "从归档读取（解包）")
	fs.BoolVarP(&o.write, "write", "w", false, "写入归档（创建）")
	fs.StringVarP(&o.archive, "file", "f", "", "归档路径名（缺省为标准输入）")
	fs.BoolVarP(&o.verbose, "verbose", "v", false, "详细输出")
	fs.StringVarP(&o.format, "format", "x", "pax", "归档格式：pax、ustar 或 cpio")
	fs.BoolVarP(&o.keep, "keep", "k", false, "不覆盖已存在的文件")
	fs.BoolVarP(&o.update, "update", "u", false, "只解包比目标更新的成员")
	fs.BoolVarP(&o.first, "first", "n", false, "每个模式只选第一个匹配")
	fs.BoolVarP(&o.complement, "complement", "c", false, "选择**不**匹配模式的成员")
	fs.BoolVarP(&o.dirsNoDescend, "no-descend", "d", false, "不进入目录（只读面为 no-op）")
	fs.BoolVarP(&o.followCmdline, "H", "H", false, "跟随命令行操作数的符号链接（只读面为 no-op）")
	fs.BoolVarP(&o.followAll, "L", "L", false, "跟随所有符号链接（只读面为 no-op）")

	subst := fs.StringArrayP("subst", "s", nil, "用替换表达式改写成员名")
	preserve := fs.StringArrayP("preserve", "p", nil, "保留文件属性")
	listOptions := fs.StringArrayP("options", "o", nil, "扩展头与算法选项")
	blocksize := fs.StringP("blocksize", "b", "", "物理块大小")
	fs.BoolVarP(&o.append, "append", "a", false, "追加到归档")
	interactive := fs.BoolP("interactive", "i", false, "交互式改名")
	linkMode := fs.BoolP("link", "l", false, "尽量建立硬链接而不是复制")
	resetAtime := fs.BoolP("t", "t", false, "重置访问时间")
	crossDevice := fs.BoolP("X", "X", false, "不跨设备边界进入目录")
	operands, code := toolcompat.Parse(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	o.operands = operands

	switch o.format {
	case "pax", "ustar", "cpio", "posix":
	default:
		return toolcompat.UsageError(rc, cmd, "unsupported archive format %q", o.format)
	}
	switch {
	case o.read && o.write:
		return toolcompat.NotSupported(rc, cmd, "-r -w（复制模式）")
	case o.append && !o.write:
		return toolcompat.UsageError(rc, cmd, "-a 只能与 -w 一起使用")
	case o.write && o.format == "cpio":
		return toolcompat.NotSupported(rc, cmd, "-x cpio（写面只提供 pax 与 ustar）")
	case len(*subst) > 0:
		return toolcompat.NotSupported(rc, cmd, "-s（替换表达式）")
	case len(*preserve) > 0:
		return toolcompat.NotSupported(rc, cmd, "-p（属性保留）")
	case len(*listOptions) > 0:
		return toolcompat.NotSupported(rc, cmd, "-o（扩展头选项）")
	case *blocksize != "":
		return toolcompat.NotSupported(rc, cmd, "-b（物理块大小）")
	case *interactive:
		return toolcompat.NotSupported(rc, cmd, "-i（交互改名，客体没有交互终端）")
	case *linkMode:
		return toolcompat.NotSupported(rc, cmd, "-l（硬链接优先复制）")
	case *resetAtime:
		return toolcompat.NotSupported(rc, cmd, "-t（重置访问时间）")
	case *crossDevice:
		return toolcompat.NotSupported(rc, cmd, "-X（跨设备边界，客体没有设备号）")
	}

	if o.write {
		return writeArchive(rc, o)
	}
	open, cleanup, ok := archiveOpener(rc, o.archive)
	if !ok {
		return 1
	}
	defer cleanup()

	if !o.read {
		return listArchive(rc, o, open)
	}
	return extractArchive(rc, o, open)
}

func archiveOpener(rc *toolcompat.RunContext, name string) (func() (io.ReadCloser, error), func(), bool) {
	if name != "" && name != "-" {
		return func() (io.ReadCloser, error) { return vfs.Open(name) }, func() {}, true
	}
	directory := vfs.Getenv("TMPDIR")
	if directory == "" {
		directory = rc.Dir
	}
	file, tempName, err := createTemp(directory, ".pax-")
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %v\n", err)
		return nil, func() {}, false
	}
	if _, err := io.Copy(file, readerOrEmpty(rc.In)); err != nil {
		_ = file.Close()
		_ = vfs.Remove(tempName)
		_, _ = fmt.Fprintf(rc.Err, "pax: 读取标准输入: %v\n", err)
		return nil, func() {}, false
	}
	if err := file.Close(); err != nil {
		_ = vfs.Remove(tempName)
		_, _ = fmt.Fprintf(rc.Err, "pax: %v\n", err)
		return nil, func() {}, false
	}
	return func() (io.ReadCloser, error) { return vfs.Open(tempName) }, func() { _ = vfs.Remove(tempName) }, true
}

func createTemp(dir, prefix string) (io.ReadWriteCloser, string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		name := path.Join(dir, prefix+strconv.Itoa(attempt))
		file, err := vfs.OpenFile(name, int(vfs.O_RDWR|vfs.O_CREATE|vfs.O_EXCL), 0o600)
		if err == nil {
			return file, name, nil
		}
		if !vfs.Exist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("无法创建临时文件")
}

func readerOrEmpty(r io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return strings.NewReader("")
}

func listArchive(rc *toolcompat.RunContext, o options, open func() (io.ReadCloser, error)) (status int) {
	stream, err := open()
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", archiveName(o.archive), toolcompat.SysErr(err))
		return 1
	}
	defer func() {
		if err := stream.Close(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: close input: %v\n", archiveName(o.archive), err)
			status = 1
		}
	}()
	members, err := pax.ReadManifest(stream)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", archiveName(o.archive), err)
		return 1
	}
	now, err := clock.Now(rc.Ctx)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %v\n", err)
		return 1
	}
	selected := selectPaths(members, o)
	status = 0
	for _, member := range members {
		if !selected[member.Path] {
			continue
		}
		if o.verbose {
			if _, err := fmt.Fprintln(rc.Out, verboseLine(member, now)); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "pax: 写标准输出: %v\n", err)
				return 1
			}
			continue
		}
		if _, err := fmt.Fprintln(rc.Out, member.Path); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: 写标准输出: %v\n", err)
			return 1
		}
	}
	return status
}

func extractArchive(rc *toolcompat.RunContext, o options, open func() (io.ReadCloser, error)) (status int) {
	stream, err := open()
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", archiveName(o.archive), toolcompat.SysErr(err))
		return 1
	}
	plan, planErr := pax.PlanExtraction(stream, rc.Dir, pax.VFS{})
	_ = stream.Close()
	if planErr != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", archiveName(o.archive), planErr)
		return 1
	}

	fatal := 0

	for _, member := range plan.Members {
		switch member.Kind {
		case pax.KindFIFO, pax.KindOther:
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: 客体无法创建 %s 类型的节点（没有 FIFO/设备节点能力）\n", member.Path, member.Kind)
			fatal++
		}
	}

	for _, rejected := range plan.Rejected {
		if pax.IsDestinationExists(rejected.Reason) {

			if !o.keep {
				continue
			}
			if o.verbose {
				_, _ = fmt.Fprintf(rc.Err, "pax: %s: 已存在，按 -k 跳过\n", rejected.Path)
			}
			continue
		}
		_, _ = fmt.Fprintf(rc.Err, "pax: %s: %s\n", rejected.Path, rejected.Reason)
		fatal++
	}
	if fatal > 0 {
		_, _ = fmt.Fprintf(rc.Err, "pax: 归档被拒绝：%d 个成员不安全，未落地任何内容\n", fatal)
		return 1
	}

	type planned struct {
		index  int
		member pax.Member
		target string
	}
	all := make([]pax.Member, 0, len(plan.Members)+len(plan.Rejected))
	for _, member := range plan.Members {
		all = append(all, member.Member)
	}
	for _, rejected := range plan.Rejected {
		all = append(all, rejected.Member)
	}
	wanted := selectPaths(all, o)
	selected := make([]planned, 0, len(plan.Members))
	for _, member := range plan.Members {
		if !wanted[member.Path] {
			continue
		}
		selected = append(selected, planned{index: member.Index, member: member.Member, target: member.Target})
	}
	if !o.keep {
		for _, rejected := range plan.Rejected {
			if !pax.IsDestinationExists(rejected.Reason) || !wanted[rejected.Path] {
				continue
			}
			target, err := destination(rc.Dir, rejected.Path)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", rejected.Path, err)
				return 1
			}
			selected = append(selected, planned{index: rejected.Index, member: rejected.Member, target: target})
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].index < selected[j].index })

	now, err := clock.Now(rc.Ctx)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %v\n", err)
		return 1
	}
	stream, err = open()
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", archiveName(o.archive), toolcompat.SysErr(err))
		return 1
	}
	defer func() {
		if err := stream.Close(); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: close input: %v\n", archiveName(o.archive), err)
			status = 1
		}
	}()
	status = 0
	next := 0
	index := -1

	type deferredMode struct {
		member string
		target string
		mode   fs.FileMode
	}
	var directoryModes []deferredMode
	reader := tar.NewReader(stream)
	for next < len(selected) {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", archiveName(o.archive), err)
			return 1
		}
		index++
		if index != selected[next].index {
			continue
		}
		entry := selected[next]
		next++
		if o.update && !needsUpdate(entry.target, entry.member) {
			continue
		}
		if err := materialize(rc.Dir, entry.target, header, reader); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", entry.member.Path, err)
			status = 1
			continue
		}
		if header.Typeflag == tar.TypeDir {
			directoryModes = append(directoryModes, deferredMode{
				member: entry.member.Path, target: entry.target, mode: header.FileInfo().Mode(),
			})
		}
		if o.verbose {
			if _, err := fmt.Fprintln(rc.Out, verboseLine(entry.member, now)); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "pax: 写标准输出: %v\n", err)
				return 1
			}
		}
	}

	for i := len(directoryModes) - 1; i >= 0; i-- {
		entry := directoryModes[i]
		if err := restoreMode(entry.target, entry.mode); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", entry.member, err)
			status = 1
		}
	}
	if next < len(selected) {
		_, _ = fmt.Fprintf(rc.Err, "pax: 归档在成员 %s 之前结束\n", selected[next].member.Path)
		return 1
	}
	return status
}

func destination(root, member string) (string, error) {
	name := strings.TrimSuffix(member, "/")
	if name == "" || name == "." || path.IsAbs(name) {
		return "", fmt.Errorf("成员路径无效")
	}
	return path.Join(root, name), nil
}

func needsUpdate(target string, member pax.Member) bool {
	info, err := vfs.Lstat(target)
	if err != nil {
		return true
	}
	modified := info.ModTime()
	return clock.Unix(modified.Unix(), int64(modified.Nanosecond())).Before(member.ModTime)
}

func materialize(root, target string, header *tar.Header, content io.Reader) error {
	switch header.Typeflag {
	case tar.TypeDir:
		return vfs.MkdirAll(target, header.FileInfo().Mode().Perm()|0o700)
	case tar.TypeReg:
		if err := vfs.MkdirAll(path.Dir(target), 0o777); err != nil {
			return err
		}
		file, err := vfs.OpenFile(target, int(vfs.O_WRONLY|vfs.O_CREATE|vfs.O_TRUNC), header.FileInfo().Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(file, content); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := restoreMode(target, header.FileInfo().Mode()); err != nil {
			return err
		}
		return restoreTimes(target, header)
	case tar.TypeSymlink:
		if err := replaceNode(target); err != nil {
			return err
		}
		return vfs.Symlink(header.Linkname, target)
	case tar.TypeLink:
		linkTarget, err := destination(root, header.Linkname)
		if err != nil {
			return err
		}
		if err := replaceNode(target); err != nil {
			return err
		}
		if err := vfs.Link(linkTarget, target); err != nil {
			return err
		}
		return restoreTimes(target, header)
	default:
		return fmt.Errorf("不支持的成员类型 %q", string(header.Typeflag))
	}
}

func replaceNode(target string) error {
	if _, err := vfs.Lstat(target); err != nil {
		if vfs.NotExist(err) {
			return nil
		}
		return err
	}
	return vfs.Remove(target)
}

func restoreMode(target string, mode fs.FileMode) error {
	if err := vfs.Chmod(target, mode); err != nil && !errors.Is(err, errno.NotSupported) {
		return err
	}
	return nil
}

func restoreTimes(target string, header *tar.Header) error {
	if header.ModTime.IsZero() {
		return nil
	}

	modified := header.ModTime.UnixNano()
	if err := vfs.Chtimes(target, modified, modified); err != nil && !errors.Is(err, errno.NotSupported) {
		return err
	}
	return nil
}

func selectPaths(members []pax.Member, o options) map[string]bool {
	selected := make(map[string]bool, len(members))
	firstSeen := make(map[string]bool, len(o.operands))
	for _, member := range members {
		if len(o.operands) == 0 {
			if !o.complement {
				selected[member.Path] = true
			}
			continue
		}
		hit := false
		for _, pattern := range o.operands {
			if !matchPattern(pattern, member.Path) {
				continue
			}
			if o.first {
				if firstSeen[pattern] {
					continue
				}
				firstSeen[pattern] = true
			}
			hit = true
		}
		if o.complement {
			hit = !hit
		}
		if hit {
			selected[member.Path] = true
		}
	}
	return selected
}

func matchPattern(pattern, name string) bool {
	if ok, err := path.Match(pattern, name); err == nil && ok {
		return true
	}
	if !strings.Contains(pattern, "/") {
		if ok, err := path.Match(pattern, path.Base(name)); err == nil && ok {
			return true
		}
	}
	return false
}

func verboseLine(member pax.Member, now clock.Time) string {
	stamp := member.ModTime
	layout := "Jan _2 15:04"
	if stamp.After(now) || now.Sub(stamp) >= sixMonths {
		layout = "Jan _2  2006"
	}
	size := member.Size
	if member.Kind == pax.KindSymlink {
		size = int64(len(member.Linkname))
	}
	line := fmt.Sprintf("%s %2d %-8s %-8s %8d %s %s", modeString(member.Mode), 1, "", "", size, stamp.Format(layout), member.Path)
	switch member.Kind {
	case pax.KindSymlink:
		line += " -> " + member.Linkname
	case pax.KindHardlink:
		line += " == " + member.Linkname
	}
	return line
}

func modeString(mode fs.FileMode) string {
	text := mode.String()
	if strings.HasPrefix(text, "L") {
		text = "l" + text[1:]
	}
	return text
}

func archiveName(name string) string {
	if name == "" || name == "-" {
		return "标准输入"
	}
	return name
}

func writeArchive(rc *toolcompat.RunContext, o options) (status int) {
	if len(o.operands) == 0 {
		return toolcompat.UsageError(rc, cmd, "缺少要归档的操作数")
	}
	var out = rc.Out
	var file vmfs.File
	if o.archive != "" && o.archive != "-" {
		flags := int(vfs.O_WRONLY | vfs.O_CREATE)
		if o.append {
			flags = int(vfs.O_RDWR | vfs.O_CREATE)
		} else {
			flags |= int(vfs.O_TRUNC)
		}
		opened, err := vfs.OpenFile(o.archive, flags, 0o666)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", o.archive, toolcompat.SysErr(err))
			return 1
		}
		defer func() {
			if err := opened.Close(); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "pax: %s: close archive: %v\n", o.archive, err)
				status = 1
			}
		}()
		file = opened
		out = opened
		if o.append {
			offset, err := appendOffset(file)
			if err != nil {
				_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", o.archive, toolcompat.SysErr(err))
				return 1
			}
			if _, err := file.Seek(offset, io.SeekStart); err != nil {
				_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", o.archive, toolcompat.SysErr(err))
				return 1
			}
		}
	}
	now, err := clock.Now(rc.Ctx)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %v\n", err)
		return 1
	}
	writer := tar.NewWriter(out)
	status = 0
	for _, operand := range o.operands {
		if err := addPath(rc, writer, operand, operand, o, now, true); err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pax: %s: %v\n", operand, err)
			status = 1
		}
	}
	if err := writer.Close(); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pax: %v\n", err)
		return 1
	}
	return status
}

func appendOffset(file vmfs.File) (int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	block := make([]byte, 512)
	var offset int64
	for {
		if _, err := io.ReadFull(file, block); err != nil {
			if errors.Is(err, io.EOF) {
				return offset, nil
			}
			return offset, err
		}
		zero := true
		for _, b := range block {
			if b != 0 {
				zero = false
				break
			}
		}
		if zero {
			return offset, nil
		}
		offset += 512
	}
}

func addPath(rc *toolcompat.RunContext, writer *tar.Writer, display, full string, o options, now clock.Time, fromCommandLine bool) error {
	follow := o.followAll || (fromCommandLine && o.followCmdline)
	info, err := statFollowing(full, follow)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		header := memberHeader(info, display+"/", tar.TypeDir, "")
		header.Format = formatFor(o)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if err := verboseWrite(rc, header, now, o); err != nil {
			return err
		}
		if o.dirsNoDescend {
			return nil
		}
		entries, err := vfs.ReadDir(full)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			child := path.Join(full, entry.Name())
			if err := addPath(rc, writer, path.Join(display, entry.Name()), child, o, now, false); err != nil {
				return err
			}
		}
		return nil
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := vfs.Readlink(full)
		if err != nil {
			return err
		}
		header := memberHeader(info, display, tar.TypeSymlink, target)
		header.Format = formatFor(o)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		return verboseWrite(rc, header, now, o)
	case info.Mode().IsRegular():
		header := memberHeader(info, display, tar.TypeReg, "")
		header.Format = formatFor(o)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		content, err := vfs.Open(full)
		if err != nil {
			return err
		}
		if _, err := io.Copy(writer, content); err != nil {
			_ = content.Close()
			return err
		}
		if err := content.Close(); err != nil {
			return err
		}
		return verboseWrite(rc, header, now, o)
	default:

		return fmt.Errorf("不支持的成员类型 %v", info.Mode())
	}
}

func memberHeader(info fs.FileInfo, name string, typeflag byte, linkname string) *tar.Header {
	mode := int64(info.Mode().Perm())
	if info.Mode()&fs.ModeSetuid != 0 {
		mode |= 0o4000
	}
	if info.Mode()&fs.ModeSetgid != 0 {
		mode |= 0o2000
	}
	if info.Mode()&fs.ModeSticky != 0 {
		mode |= 0o1000
	}
	size := info.Size()
	if typeflag != tar.TypeReg {
		size = 0
	}
	return &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     size,
		ModTime:  info.ModTime(),
		Typeflag: typeflag,
		Linkname: linkname,
	}
}

func statFollowing(name string, follow bool) (fs.FileInfo, error) {
	if follow {
		return vfs.Stat(name)
	}
	return vfs.Lstat(name)
}

func formatFor(o options) tar.Format {
	if o.format == "ustar" {
		return tar.FormatUSTAR
	}
	return tar.FormatUnknown
}

func verboseWrite(rc *toolcompat.RunContext, header *tar.Header, now clock.Time, o options) error {
	if !o.verbose {
		return nil
	}
	modified := header.ModTime
	line := verboseLine(pax.Member{
		Path:     header.Name,
		Kind:     kindOfHeader(header),
		Linkname: header.Linkname,
		Size:     header.Size,
		Mode:     header.FileInfo().Mode(),
		ModTime:  clock.Unix(modified.Unix(), int64(modified.Nanosecond())),
	}, now)
	if _, err := fmt.Fprintln(rc.Out, line); err != nil {
		return fmt.Errorf("写标准输出: %w", err)
	}
	return nil
}

func kindOfHeader(header *tar.Header) pax.Kind {
	switch header.Typeflag {
	case tar.TypeDir:
		return pax.KindDir
	case tar.TypeSymlink:
		return pax.KindSymlink
	case tar.TypeLink:
		return pax.KindHardlink
	default:
		return pax.KindFile
	}
}
