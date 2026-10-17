package localedef

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"mvdan.cc/sh/v3/interp"
)

const localeRoot = "/usr/lib/locale"

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "localedef")
	lenient := flags.BoolP("force", "c", false, "不认识的指令降级为警告并继续")
	charmapFile := flags.StringP("charmap", "f", "", "字符映射文件（只提取 <code_set_name>）")
	inputFile := flags.StringP("input", "i", "", "语言环境定义源文件（缺省 stdin）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	operands := flags.Args()
	if len(operands) != 1 {
		_, _ = fmt.Fprintln(h.Stderr, "用法: localedef [-c] [-f 字符映射文件] [-i 源文件] 归档名")
		return interp.ExitStatus(2)
	}
	name := operands[0]
	if !safeName(name) {
		_, _ = fmt.Fprintf(h.Stderr, "localedef: %s: 归档名必须是单个名字\n", name)
		return interp.ExitStatus(1)
	}
	source, err := readInput(ctx, *inputFile)
	if err != nil {
		return err
	}
	parsed, warnings, parseErr := parse(source)
	for _, warning := range warnings {
		if *lenient {
			_, _ = fmt.Fprintf(h.Stderr, "localedef: %s（-c 降级为警告）\n", warning)
			continue
		}
		_, _ = fmt.Fprintf(h.Stderr, "localedef: %s\n", warning)
	}
	if parseErr != nil {
		_, _ = fmt.Fprintf(h.Stderr, "localedef: %v\n", parseErr)
		return interp.ExitStatus(1)
	}
	if len(warnings) > 0 && !*lenient {
		return interp.ExitStatus(1)
	}
	system, err := commandio.Filesystem(ctx)
	if err != nil {
		return err
	}
	archive := path.Join(localeRoot, name)
	if _, err := system.Stat(ctx, archive); err == nil {

		_, _ = fmt.Fprintf(h.Stderr, "localedef: %s: 语言环境归档已存在\n", name)
		return interp.ExitStatus(1)
	} else if !missing(err) {
		return err
	}
	conf := &strings.Builder{}
	conf.WriteString("# 客体语言环境归档：localedef 生成的明文键值格式（KEY=VALUE，'#' 起始为注释），\n" +
		"# 不是 glibc 的二进制归档；locale 命令按同一路径 /usr/lib/locale/<名>/locale.conf 读取。\n")
	if *charmapFile != "" {
		codeset, err := charmapName(ctx, *charmapFile)
		if err != nil {
			return err
		}
		fmt.Fprintf(conf, "charmap=%s\n", codeset)
	}
	for _, entry := range parsed.entries {
		fmt.Fprintf(conf, "%s=%s\n", entry.key, entry.value)
	}
	if err := mkdirAll(ctx, system, archive); err != nil {
		return err
	}
	file, err := system.Open(ctx, path.Join(archive, "locale.conf"), vmfs.Create|vmfs.Truncate|vmfs.WriteOnly, 0o644)
	if err != nil {
		return err
	}
	_, err = io.WriteString(file, conf.String())
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readInput(ctx context.Context, inputFile string) (string, error) {
	if inputFile == "" || inputFile == "-" {
		return commandio.ReadText(ctx, commandio.Context(ctx).Stdin)
	}
	file, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), inputFile), vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	text, err := commandio.ReadText(ctx, file)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return text, err
}

func charmapName(ctx context.Context, file string) (string, error) {
	handle, err := commandio.Open(ctx, commandio.Path(commandio.Directory(ctx), file), vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	text, err := commandio.ReadText(ctx, handle)
	if closeErr := handle.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if fields[0] == "<code_set_name>" && len(fields) >= 2 {
			return unquote(fields[1]), nil
		}
	}
	return path.Base(file), nil
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, errno.NotFound)
}

func mkdirAll(ctx context.Context, system vmfs.System, name string) error {
	info, err := system.Stat(ctx, name)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("localedef: %s: 不是目录", name)
		}
		return nil
	}
	if !missing(err) {
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
