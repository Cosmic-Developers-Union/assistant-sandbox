package pathchkcmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/vfs"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat"
)

var cmd = &toolcompat.Tool{
	Name:     "pathchk",
	Synopsis: "Check whether file names are valid or portable.",
	Usage:    "pathchk [OPTION]... NAME...",
}

const (
	posixPathMax = 256
	posixNameMax = 14
)

func init() { cmd.Run = run; toolcompat.Register(cmd) }

func run(rc *toolcompat.RunContext, args []string) int {
	fs := toolcompat.NewFlags(cmd.Name)
	posix := fs.BoolP("posix", "p", false, "check for most POSIX systems")
	special := fs.BoolP("posix-special", "P", false, "check for empty names and leading hyphens")
	portability := fs.Bool("portability", false, "check both POSIX and special portability")
	paths, code := toolcompat.ParseRequireOrder(rc, cmd, fs, args)
	if code >= 0 {
		return code
	}
	if len(paths) == 0 {
		return toolcompat.UsageError(rc, cmd, "missing operand")
	}
	status := 0
	for _, p := range paths {
		ok := true
		if *portability || *posix {
			ok = checkPOSIX(rc, p) && ok
		}
		if *portability || *special {
			ok = checkSpecial(rc, p) && ok
		}

		if !*portability && !*posix {
			ok = checkDefault(rc, p) && ok
		}
		if !ok {
			status = 1
		}
	}
	return status
}

func checkDefault(rc *toolcompat.RunContext, p string) bool {
	return checkDefaultWith(rc, p, filesystemLimits)
}

func filesystemLimits(string) (int, int, error) { return 0, 0, nil }

type limitLookup func(string) (pathMax, nameMax int, err error)

func checkDefaultWith(rc *toolcompat.RunContext, p string, limits limitLookup) bool {
	if p == "" {
		_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: No such file or directory\n", p)
		return false
	}
	base, components, trailing := pathParts(rc, p)
	pathLimit, nameLimit, err := limits(base)
	if err != nil {
		_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: cannot determine filesystem limits: %v\n", p, err)
		return false
	}

	if pathLimit > 0 && len(p) >= pathLimit {
		_, _ = fmt.Fprintf(rc.Err, "pathchk: path %q has length %d; exceeds limit %d\n", p, len(p), pathLimit)
		return false
	}
	current := base
	missing := false
	for i, c := range components {
		if c == "" {
			continue
		}
		if !missing {
			if _, searchErr := vfs.Stat(rawPathJoin(current, ".")); searchErr != nil {
				_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: directory is not searchable at %q\n", p, current)
				return false
			}
		}
		if nameLimit > 0 && len(c) > nameLimit {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: name %q has length %d; exceeds limit %d\n", c, len(c), nameLimit)
			return false
		}
		if missing {
			continue
		}
		candidate := rawPathJoin(current, c)
		_, lstatErr := vfs.Lstat(candidate)
		if errors.Is(lstatErr, vfs.ErrNotExist) {
			missing = true
			continue
		}
		if lstatErr != nil {
			if errors.Is(lstatErr, vfs.ErrPermission) {
				_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: directory is not searchable at %q\n", p, current)
				return false
			}
			_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: byte sequence is not valid or component cannot be accessed at %q: %v\n", p, candidate, lstatErr)
			return false
		}
		last := i == len(components)-1 && !trailing
		if last {
			continue
		}
		st, statErr := vfs.Stat(candidate)
		if statErr != nil {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: cannot access directory at %q: %v\n", p, candidate, statErr)
			return false
		}
		if !st.IsDir() {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: not a directory at %q\n", p, candidate)
			return false
		}
		current = candidate
		_, nameLimit, err = limits(current)
		if err != nil {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: %q: cannot determine filesystem limits at %q: %v\n", p, current, err)
			return false
		}
	}
	return true
}

func rawPathJoin(dir, component string) string {
	if strings.HasSuffix(dir, "/") {
		return dir + component
	}
	return dir + "/" + component
}

func pathParts(rc *toolcompat.RunContext, p string) (base string, components []string, trailing bool) {
	const separator = "/"
	trailing = strings.HasSuffix(p, separator)
	if strings.HasPrefix(p, separator) {
		base = separator
		p = strings.TrimPrefix(p, separator)
	} else {
		base = rc.Dir
		if base == "" {
			base = "."
		}
	}
	return base, strings.Split(p, separator), trailing
}

func checkPOSIX(rc *toolcompat.RunContext, p string) bool {
	if p == "" {

		return true
	}

	if len(p) >= posixPathMax {
		_, _ = fmt.Fprintf(rc.Err, "pathchk: path %q has length %d; exceeds POSIX limit %d\n", p, len(p), posixPathMax)
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if len(c) > posixNameMax {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: name %q has length %d; exceeds POSIX limit %d\n", c, len(c), posixNameMax)
			return false
		}
		if !portableChars(c) {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: %q contains a nonportable character\n", c)
			return false
		}
	}
	return true
}

func checkSpecial(rc *toolcompat.RunContext, p string) bool {
	if p == "" {
		_, _ = fmt.Fprintln(rc.Err, "pathchk: empty file name")
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if strings.HasPrefix(c, "-") {
			_, _ = fmt.Fprintf(rc.Err, "pathchk: %q has a component beginning with a hyphen\n", p)
			return false
		}
	}
	return true
}

func portableChars(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		switch r {
		case '.', '_', '-':
			continue
		default:
			return false
		}
	}
	return true
}
