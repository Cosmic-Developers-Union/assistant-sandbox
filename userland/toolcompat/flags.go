package toolcompat

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/pflag"
)

var Version = "dev"

func NewFlags(name string) *pflag.FlagSet {
	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetInterspersed(true)
	fs.SortFlags = false
	fs.Bool("help", false, "display this help and exit")
	fs.Bool("version", false, "output version information and exit")

	fs.SetOutput(discard{})
	return fs
}

func AliasHelpVersion(args []string) []string {
	var out []string
	rest := false
	for _, arg := range args {
		if rest {
			out = append(out, arg)
			continue
		}
		if arg == "--" {
			rest = true
			out = append(out, arg)
			continue
		}
		switch arg {
		case "-h":
			out = append(out, "--help")
		case "-V":
			out = append(out, "--version")
		default:
			if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
				kept := "-"
				for _, r := range arg[1:] {
					switch r {
					case 'h':
						out = append(out, "--help")
					case 'V':
						out = append(out, "--version")
					default:
						kept += string(r)
					}
				}
				if kept != "-" {
					out = append(out, kept)
				}
			} else {
				out = append(out, arg)
			}
		}
	}
	return out
}

const (
	helpAliasFlag    = "help-short"
	versionAliasFlag = "version-short"
)

func AddHelpVersionAliases(fs *pflag.FlagSet) {
	addUniversalAlias(fs, "help", "h", helpAliasFlag)
	addUniversalAlias(fs, "version", "V", versionAliasFlag)
}

func addUniversalAlias(fs *pflag.FlagSet, long, short, aliasName string) {
	canonical := fs.Lookup(long)
	if canonical == nil || fs.ShorthandLookup(short) != nil {
		return
	}
	_ = fs.BoolP(aliasName, short, false, canonical.Usage)
	fs.Lookup(aliasName).Hidden = true

	canonical.Shorthand = short
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func Parse(rc *RunContext, t *Tool, fs *pflag.FlagSet, args []string) ([]string, int) {
	return parse(rc, t, fs, args, true)
}

func ParseRequireOrder(rc *RunContext, t *Tool, fs *pflag.FlagSet, args []string) ([]string, int) {
	fs.SetInterspersed(false)
	return parse(rc, t, fs, args, false)
}

func parse(rc *RunContext, t *Tool, fs *pflag.FlagSet, args []string, interspersed bool) ([]string, int) {
	AddHelpVersionAliases(fs)
	var code int
	args, code = expandLongOptionPrefixes(rc, t, fs, args, interspersed)
	if code != -1 {
		return nil, code
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			printHelp(rc, t, fs)
			return nil, 0
		}
		_, _ = fmt.Fprintf(rc.Err, "%s: %v\n", t.Name, err)
		_, _ = fmt.Fprintf(rc.Err, "%s: not every GNU flag is implemented in pure-Go coreutils — see '%s --help' for the supported subset\n", t.Name, t.Name)
		return nil, 2
	}
	if flagBool(fs, "help") || flagBool(fs, helpAliasFlag) {
		printHelp(rc, t, fs)
		return nil, 0
	}
	if flagBool(fs, "version") || flagBool(fs, versionAliasFlag) {
		_, _ = fmt.Fprintf(rc.Out, "%s (assistant-sandbox) %s\n", t.Name, Version)
		return nil, 0
	}
	return fs.Args(), -1
}

func expandLongOptionPrefixes(rc *RunContext, t *Tool, fs *pflag.FlagSet, args []string, interspersed bool) ([]string, int) {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		arg := out[i]
		if arg == "--" {
			break
		}
		if !interspersed && (arg == "-" || !strings.HasPrefix(arg, "-")) {
			break
		}
		if strings.HasPrefix(arg, "--") && len(arg) > 2 {
			name, suffix, hasValue := strings.Cut(arg[2:], "=")
			flag := fs.Lookup(name)
			if flag == nil {
				var matches []string
				fs.VisitAll(func(f *pflag.Flag) {

					if f.Name == helpAliasFlag || f.Name == versionAliasFlag {
						return
					}
					if strings.HasPrefix(f.Name, name) {
						matches = append(matches, f.Name)
					}
				})
				sort.Strings(matches)
				switch len(matches) {
				case 0:
					continue
				case 1:
					flag = fs.Lookup(matches[0])
					if hasValue {
						out[i] = "--" + matches[0] + "=" + suffix
					} else {
						out[i] = "--" + matches[0]
					}
				default:
					_, _ = fmt.Fprintf(rc.Err, "%s: option '--%s' is ambiguous; possibilities:", t.Name, name)
					for _, match := range matches {
						_, _ = fmt.Fprintf(rc.Err, " '--%s'", match)
					}
					_, _ = fmt.Fprintln(rc.Err)
					return nil, 2
				}
			}
			if !hasValue && flagTakesValue(flag) {
				i++
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			for pos := 1; pos < len(arg); pos++ {
				flag := fs.ShorthandLookup(arg[pos : pos+1])
				if flag == nil {
					continue
				}
				if flagTakesValue(flag) {
					if pos == len(arg)-1 {
						i++
					}
					break
				}
			}
		}
	}
	return out, -1
}

func flagTakesValue(flag *pflag.Flag) bool {
	return flag != nil && flag.NoOptDefVal == "" && flag.Value.Type() != "bool"
}

func flagBool(fs *pflag.FlagSet, name string) bool {
	v, err := fs.GetBool(name)
	return err == nil && v
}

func printHelp(rc *RunContext, t *Tool, fs *pflag.FlagSet) {
	_, _ = fmt.Fprintf(rc.Out, "Usage: %s\n", t.Usage)
	if t.Synopsis != "" {
		_, _ = fmt.Fprintf(rc.Out, "%s\n", t.Synopsis)
	}
	u := fs.FlagUsages()
	for _, char := range []string{"1", "t", "S", "v", "g", "o", "C", "x", "p", "f", "U", "X", "q", "c", "u", "m", "Z"} {
		u = strings.ReplaceAll(u, ", --"+char+" ", "      ")
	}
	_, _ = fmt.Fprintf(rc.Out, "\nOptions:\n%s", u)
	_, _ = fmt.Fprintf(rc.Out, "\nImplements the documented GNU semantics for the flags above;\nanything else fails with a clear error (exit 2), never a silent guess.\n")
}

func UsageError(rc *RunContext, t *Tool, format string, a ...any) int {
	_, _ = fmt.Fprintf(rc.Err, "%s: %s\n", t.Name, fmt.Sprintf(format, a...))
	_, _ = fmt.Fprintf(rc.Err, "Try '%s --help' for more information.\n", t.Name)
	return 2
}

func NotSupported(rc *RunContext, t *Tool, what string) int {
	_, _ = fmt.Fprintf(rc.Err, "%s: %s is not supported by pure-Go coreutils — see '%s --help' for the supported subset\n", t.Name, what, t.Name)
	return 2
}
