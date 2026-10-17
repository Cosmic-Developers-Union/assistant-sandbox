package ar

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"slices"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const maxInputBytes = 4 << 20

var errOverBudget = errors.New("累计输入超过 4 MiB")

const keys = "dmpqrtx"

func isKey(c byte) bool { return strings.IndexByte(keys, c) >= 0 }

type invocation struct {
	key       byte
	verbose   bool
	quiet     bool
	onlyNewer bool
	after     bool
	before    bool
	symbol    bool
	posname   string
	archive   string
	members   []string
}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	out := &stickyWriter{writer: h.Stdout}
	diag := &stickyWriter{writer: h.Stderr}
	inv, usageErr := parseInvocation(args)
	if usageErr != nil {
		_, _ = fmt.Fprintf(diag, "ar: %s\n用法: ar 键[-修饰符] 档案 [成员...]\n", usageErr)
		return interp.ExitStatus(2)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(diag, "ar: %v\n", err)
		return interp.ExitStatus(1)
	}
	work := &work{
		ctx:    ctx,
		system: system,
		dir:    commandio.Directory(ctx),
		out:    out,
		diag:   diag,
		budget: maxInputBytes,
	}
	code := work.run(inv)

	if out.err != nil {
		_, _ = fmt.Fprintf(diag, "ar: 输出失败: %v\n", out.err)
		return interp.ExitStatus(1)
	}
	if diag.err != nil {
		return interp.ExitStatus(1)
	}
	if code != 0 {
		return interp.ExitStatus(code)
	}
	return nil
}

type stickyWriter struct {
	writer io.Writer
	err    error
}

func (w *stickyWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.err = err
	}
	return n, err
}

func parseInvocation(args []string) (*invocation, error) {
	inv := &invocation{}
	index := 0
	for index < len(args) && strings.HasPrefix(args[index], "-") && args[index] != "-" {
		cluster := strings.TrimLeft(args[index], "-")
		if cluster == "" {

			index++
			break
		}
		if err := inv.absorbCluster(cluster); err != nil {
			return nil, err
		}
		index++
	}
	if inv.key == 0 && index < len(args) && !strings.HasPrefix(args[index], "-") {
		if err := inv.absorbCluster(args[index]); err != nil {
			return nil, err
		}
		index++
	}
	if inv.key == 0 {
		return nil, errors.New("缺少操作键（d m p q r t x 之一）")
	}
	rest := args[index:]
	if inv.after || inv.before {
		if len(rest) == 0 {
			return nil, errors.New("定位修饰符 a/b/i 需要位置成员名")
		}
		inv.posname, rest = rest[0], rest[1:]
	}
	if len(rest) == 0 {
		return nil, errors.New("缺少档案名")
	}
	inv.archive, inv.members = rest[0], rest[1:]
	if inv.onlyNewer && inv.key != 'r' {
		return nil, errors.New("修饰符 u 只能与键 r 一起使用")
	}
	if (inv.after || inv.before) && inv.key != 'r' && inv.key != 'm' {
		return nil, errors.New("定位修饰符 a/b/i 只能与键 r 或 m 一起使用")
	}
	if (inv.key == 'd' || inv.key == 'm') && len(inv.members) == 0 {
		return nil, fmt.Errorf("键 %c 需要至少一个成员名", inv.key)
	}
	return inv, nil
}

func (inv *invocation) absorbCluster(cluster string) error {
	for i := 0; i < len(cluster); i++ {
		c := cluster[i]
		if isKey(c) {
			if inv.key != 0 && inv.key != c {
				return fmt.Errorf("一次只能给一个操作键，已有 %q 又遇到 %q", string(inv.key), string(c))
			}
			inv.key = c
			continue
		}
		switch c {
		case 'v':
			inv.verbose = true
		case 'c':
			inv.quiet = true
		case 'u':
			inv.onlyNewer = true
		case 'a':
			inv.after = true
		case 'b', 'i':
			inv.before = true
		case 's':

			inv.symbol = true
		default:
			return fmt.Errorf("不支持的键或修饰符 %q", string(c))
		}
	}
	return nil
}

type work struct {
	ctx    context.Context
	system vmfs.System
	dir    string
	out    *stickyWriter
	diag   *stickyWriter
	budget int
}

func (w *work) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.out, format, args...)
}

func (w *work) diagf(format string, args ...any) {
	_, _ = fmt.Fprintf(w.diag, format, args...)
}

func (w *work) report(format string, args ...any) {
	w.diagf("ar: "+format+"\n", args...)
}

func (w *work) run(inv *invocation) int {
	archivePath := commandio.Path(w.dir, inv.archive)
	info, statErr := w.system.Stat(w.ctx, archivePath)
	if statErr != nil && !isNotExist(statErr) {
		w.report("%v", statErr)
		return 1
	}
	var members []member
	switch {
	case statErr == nil && info.IsDir():
		w.report("%s: 是目录", inv.archive)
		return 1
	case statErr == nil:
		data, err := w.read(archivePath)
		if err != nil {
			w.report("%s: %v", inv.archive, err)
			return 1
		}
		parsed, err := parseArchive(data)
		if err != nil {
			w.report("%s: %v", inv.archive, err)
			return 1
		}
		members = parsed
	case inv.key == 'r' || inv.key == 'q':

		if !inv.quiet {
			w.diagf("ar: 创建档案 %s\n", inv.archive)
		}
	default:
		w.report("%s: 档案不存在", inv.archive)
		return 1
	}
	switch inv.key {
	case 't':
		return w.list(inv, members)
	case 'p':
		return w.print(inv, members)
	case 'x':
		return w.extract(inv, members)
	case 'd':
		return w.delete(inv, members, archivePath)
	case 'q':
		return w.append(inv, members, archivePath)
	case 'r':
		return w.replace(inv, members, archivePath)
	case 'm':
		return w.move(inv, members, archivePath)
	}
	return 0
}

func (w *work) list(inv *invocation, members []member) int {
	code := 0
	for _, m := range filterMembers(w, inv, members, &code) {
		if inv.verbose {
			w.printf("%o %d %s\n", m.mode&0o7777, len(m.data), m.name)
		} else {
			w.printf("%s\n", m.name)
		}
	}
	return code
}

func (w *work) print(inv *invocation, members []member) int {
	code := 0
	for _, m := range filterMembers(w, inv, members, &code) {
		if inv.verbose {
			w.printf("\n<member %s>\n\n", m.name)
		}
		_, _ = w.out.Write(m.data)
	}
	return code
}

func (w *work) extract(inv *invocation, members []member) int {
	code := 0
	timestamps, canStamp := w.system.(vmfs.Timestamps)
	for _, m := range filterMembers(w, inv, members, &code) {
		target := commandio.Path(w.dir, m.name)
		perm := stdfs.FileMode(m.mode & 0o777)
		if perm == 0 {

			perm = 0o644
		}
		file, err := w.system.Open(w.ctx, target, vmfs.WriteOnly|vmfs.Create|vmfs.Truncate, perm)
		if err != nil {
			w.report("%s: %v", m.name, err)
			code = 1
			continue
		}
		_, writeErr := file.Write(m.data)
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			w.report("%s: %v", m.name, writeErr)
			code = 1
			continue
		}
		if canStamp {
			nanos := m.mtime * 1_000_000_000
			if err := timestamps.SetTimes(w.ctx, target, nanos, nanos); err != nil {
				w.diagf("ar: %s: 回填时间戳失败: %v\n", m.name, err)
			}
		}
		if inv.verbose {
			w.printf("x - %s\n", m.name)
		}
	}
	return code
}

func (w *work) delete(inv *invocation, members []member, archivePath string) int {
	wanted := nameSet(inv.members)
	var kept []member
	deleted := map[string]bool{}
	for _, m := range members {
		if !wanted[m.name] {
			kept = append(kept, m)
			continue
		}
		deleted[m.name] = true
		if inv.verbose {
			w.printf("d - %s\n", m.name)
		}
	}
	for _, name := range inv.members {
		if !deleted[name] {
			w.diagf("ar: %s: 档案里没有那个成员\n", name)
		}
	}
	return w.rewrite(inv, archivePath, kept)
}

func (w *work) append(inv *invocation, members []member, archivePath string) int {
	added, code := w.readMembers(inv)
	if len(added) > 0 {
		members = append(members, added...)
	}
	if len(added) == 0 && code != 0 && len(members) == 0 {

		return code
	}
	if result := w.rewrite(inv, archivePath, members); result != 0 {
		return result
	}
	return code
}

func (w *work) replace(inv *invocation, members []member, archivePath string) int {
	list := members
	var additions []member
	code := 0
	for _, name := range inv.members {
		m, err := w.readMember(inv, name)
		if err != nil {
			w.report("%v", err)
			code = 1
			continue
		}
		if index := indexOf(list, m.name); index >= 0 {
			if inv.onlyNewer && m.mtime <= list[index].mtime {
				continue
			}
			list[index] = m
			if inv.verbose {
				w.printf("r - %s\n", m.name)
			}
			continue
		}
		additions = append(additions, m)
		if inv.verbose {
			w.printf("r - %s\n", m.name)
		}
	}
	if len(additions) > 0 {
		list = w.insertAdditions(inv, list, additions, &code)
	}
	if len(additions) == 0 && code != 0 && membersEqual(list, members) {

		return code
	}
	if result := w.rewrite(inv, archivePath, list); result != 0 {
		return result
	}
	return code
}

func (w *work) insertAdditions(inv *invocation, list, additions []member, code *int) []member {
	if !inv.after && !inv.before {
		return append(list, additions...)
	}
	anchor := indexOf(list, inv.posname)
	if anchor < 0 {
		w.report("%s: 档案里没有那个成员", inv.posname)
		*code = 1
		return list
	}

	position := anchor
	if inv.after {
		position++
	}
	for _, m := range additions {
		list = insertAt(list, position, m)
		position++
	}
	return list
}

func (w *work) move(inv *invocation, members []member, archivePath string) int {

	for _, name := range inv.members {
		if !nameIn(members, name) {
			w.report("%s: 档案里没有那个成员", name)
			return 1
		}
	}
	wanted := nameSet(inv.members)
	var moved, kept []member
	for _, m := range members {
		if wanted[m.name] {
			moved = append(moved, m)
			continue
		}
		kept = append(kept, m)
	}

	var list []member
	if inv.after || inv.before {
		anchor := indexOf(kept, inv.posname)
		if anchor < 0 {
			w.report("%s: 档案里没有那个成员", inv.posname)
			return 1
		}
		position := anchor
		if inv.after {
			position++
		}
		list = insertAll(kept, position, moved)
	} else {
		list = kept
		list = append(list, moved...)
	}
	if inv.verbose {
		for _, m := range moved {
			w.printf("m - %s\n", m.name)
		}
	}
	return w.rewrite(inv, archivePath, list)
}

func (w *work) rewrite(inv *invocation, archivePath string, members []member) int {
	perm := stdfs.FileMode(0o666)
	if info, err := w.system.Stat(w.ctx, archivePath); err == nil {
		perm = info.Mode().Perm()
	}
	if err := w.writeViaTemp(archivePath, buildArchive(members), perm); err != nil {
		w.report("%s: %v", inv.archive, err)
		return 1
	}
	return 0
}

func (w *work) readMembers(inv *invocation) ([]member, int) {
	var out []member
	code := 0
	for _, name := range inv.members {
		m, err := w.readMember(inv, name)
		if err != nil {
			w.report("%v", err)
			code = 1
			continue
		}
		if inv.verbose {
			w.printf("q - %s\n", m.name)
		}
		out = append(out, m)
	}
	return out, code
}

func (w *work) readMember(inv *invocation, name string) (member, error) {
	if len(name) > maxPlainName {
		return member{}, fmt.Errorf("%s: 成员名超过 %d 字符，客体档案不做扩展名段", name, maxPlainName)
	}
	if strings.Contains(name, "/") {
		return member{}, fmt.Errorf("%s: 成员名不能包含 '/'，无法与档案头的名字终止符区分", name)
	}
	target := commandio.Path(w.dir, name)
	info, err := w.system.Stat(w.ctx, target)
	if err != nil {
		return member{}, err
	}
	if info.IsDir() {
		return member{}, fmt.Errorf("%s: 是目录", name)
	}
	data, err := w.read(target)
	if err != nil {
		return member{}, err
	}
	return member{
		name:  name,
		mtime: info.ModTime().UnixNano() / 1_000_000_000,
		mode:  uint32(info.Mode().Perm()),
		data:  data,
	}, nil
}

func (w *work) read(target string) ([]byte, error) {
	file, err := w.system.Open(w.ctx, target, vmfs.ReadOnly, 0)
	if err != nil {
		return nil, err
	}

	data, err := io.ReadAll(io.LimitReader(file, int64(w.budget+1)))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if len(data) > w.budget {
		return nil, errOverBudget
	}
	w.budget -= len(data)
	return data, nil
}

func (w *work) writeViaTemp(target string, content []byte, perm stdfs.FileMode) error {
	file, tempName, err := w.createTemp(path.Dir(target))
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = w.system.Remove(w.ctx, tempName)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := w.system.Rename(w.ctx, tempName, target); err != nil {
		return err
	}
	committed = true

	if modes, ok := w.system.(vmfs.Modes); ok {
		if err := modes.Chmod(w.ctx, target, perm); err != nil {
			w.diagf("ar: %s: 恢复权限失败: %v\n", target, err)
		}
	}
	return nil
}

func (w *work) createTemp(dir string) (vmfs.File, string, error) {
	for attempt := 0; attempt < 64; attempt++ {
		name := path.Join(dir, fmt.Sprintf(".ar-%d", attempt))
		file, err := w.system.Open(w.ctx, name, vmfs.WriteOnly|vmfs.Create|vmfs.Exclusive, 0o600)
		if err == nil {
			return file, name, nil
		}
		if !isExist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("无法创建临时文件")
}

func filterMembers(w *work, inv *invocation, members []member, code *int) []member {
	if len(inv.members) == 0 {
		return members
	}
	wanted := nameSet(inv.members)
	var out []member
	for _, m := range members {
		if wanted[m.name] {
			out = append(out, m)
		}
	}
	for _, name := range inv.members {
		if !nameIn(members, name) {
			w.report("%s: 档案里没有那个成员", name)
			*code = 1
		}
	}
	return out
}

func indexOf(members []member, name string) int {
	for i, m := range members {
		if m.name == name {
			return i
		}
	}
	return -1
}

func nameIn(members []member, name string) bool { return indexOf(members, name) >= 0 }

func nameSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

func insertAt(list []member, pos int, m member) []member {
	return insertAll(list, pos, []member{m})
}

func insertAll(list []member, pos int, items []member) []member {
	if len(items) == 0 {
		return list
	}
	grown := slices.Grow(list, len(items))
	grown = append(grown, make([]member, len(items))...)
	copy(grown[pos+len(items):], grown[pos:])
	copy(grown[pos:pos+len(items)], items)
	return grown
}

func membersEqual(a, b []member) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].name != b[i].name {
			return false
		}
	}
	return true
}

func isNotExist(err error) bool {
	return errors.Is(err, errno.NotFound) || errors.Is(err, stdfs.ErrNotExist)
}

func isExist(err error) bool {
	return errors.Is(err, errno.Exist) || errors.Is(err, stdfs.ErrExist)
}
