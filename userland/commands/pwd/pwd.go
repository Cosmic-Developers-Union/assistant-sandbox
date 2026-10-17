package pwdcmd

import (
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"
)

var cmd = &toolcompat.Tool{
	Name:     "pwd",
	Synopsis: "Print the full filename of the current working directory.",
	Usage:    "pwd [OPTION]...",
}

const maxLinkDepth = 32

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	args = toolcompat.AliasHelpVersion(args)
	flags := toolcompat.NewFlags(cmd.Name)
	flags.BoolP("logical", "L", false, "print the logical working directory, even with symlinks")
	flags.BoolP("physical", "P", false, "print the physical directory, with all symlinks resolved")
	operands, code := toolcompat.Parse(rc, cmd, flags, args)
	if code >= 0 {
		return code
	}
	if len(operands) > 0 {

		_, _ = fmt.Fprintln(rc.Err, "pwd: ignoring non-option arguments")
	}
	if rc.Dir == "" {
		_, _ = fmt.Fprintln(rc.Err, "pwd: cannot determine current directory")
		return 1
	}
	defaultMode := byte('P')
	for _, env := range rc.Env {
		if strings.HasPrefix(env, "POSIXLY_CORRECT=") {
			defaultMode = 'L'
			break
		}
	}
	if lastLP(args, defaultMode) == 'L' {
		if logical := logicalDir(rc); logical != "" {
			return writeDirectory(rc, logical)
		}
	}

	resolved, err := physicalDir(rc)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pwd: %v\n", err)
		return 1
	}
	return writeDirectory(rc, resolved)
}

func writeDirectory(rc *toolcompat.RunContext, dir string) int {
	if _, err := fmt.Fprintln(rc.Out, dir); err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pwd: write error: %v\n", err)
		return 1
	}
	return 0
}

func logicalDir(rc *toolcompat.RunContext) string {
	pwd := rc.Getenv("PWD")
	if !path.IsAbs(pwd) || hasDotComponent(pwd) {
		return ""
	}
	if path.Clean(pwd) != path.Clean(rc.Dir) {
		return ""
	}
	return pwd
}

func hasDotComponent(name string) bool {
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func physicalDir(rc *toolcompat.RunContext) (string, error) {
	if !path.IsAbs(rc.Dir) {
		return "", fmt.Errorf("cannot determine absolute path for %q", rc.Dir)
	}
	return resolveLinks(rc.Dir, 0)
}

func resolveLinks(name string, depth int) (string, error) {
	if depth > maxLinkDepth {
		return "", fmt.Errorf("too many levels of symbolic links")
	}
	parts := strings.Split(strings.TrimPrefix(path.Clean(name), "/"), "/")
	resolved := make([]string, 0, len(parts))
	for index := 0; index < len(parts); index++ {
		component := parts[index]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		candidate := "/" + strings.Join(append(append([]string{}, resolved...), component), "/")
		info, err := vfs.Lstat(candidate)
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			resolved = append(resolved, component)
			continue
		}
		target, err := vfs.Readlink(candidate)
		if err != nil {
			resolved = append(resolved, component)
			continue
		}
		rest := strings.Join(parts[index+1:], "/")
		var joined string
		if path.IsAbs(target) {
			joined = path.Join(target, rest)
		} else {
			joined = path.Join("/", strings.Join(resolved, "/"), target, rest)
		}
		return resolveLinks(path.Clean(joined), depth+1)
	}
	return "/" + strings.Join(resolved, "/"), nil
}

func lastLP(args []string, defaultMode byte) byte {
	mode := defaultMode
	for _, a := range args {
		switch {
		case a == "--":
			return mode
		case isLogicalLong(a):
			mode = 'L'
		case isPhysicalLong(a):
			mode = 'P'
		case a == "--logical":
			mode = 'L'
		case a == "--physical":
			mode = 'P'
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			for _, c := range a[1:] {
				switch c {
				case 'L':
					mode = 'L'
				case 'P':
					mode = 'P'
				}
			}
		}
	}
	return mode
}

func isLogicalLong(arg string) bool {
	return longBoolFlagHasValue(arg, "logical")
}

func isPhysicalLong(arg string) bool {
	return longBoolFlagHasValue(arg, "physical")
}

func longBoolFlagHasValue(arg, name string) bool {
	if !strings.HasPrefix(arg, "--") || arg == "--" || len(arg) <= 2 {
		return false
	}
	if arg[2] == '-' {
		return false
	}
	nameAndValue := arg[2:]
	flagName, rawValue, hasValue := strings.Cut(nameAndValue, "=")
	if !strings.HasPrefix(name, flagName) {
		return false
	}
	if !hasValue {
		return true
	}
	if rawValue == "" {
		return false
	}
	value, err := strconv.ParseBool(rawValue)
	return err == nil && value
}
