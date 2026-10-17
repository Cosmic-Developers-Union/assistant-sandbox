package main

func probes() []probe {
	const base = "https://pubs.opengroup.org/onlinepubs/9699919799.2016edition/utilities/"
	shell := base + "V3_chap02.html"
	var result []probe
	add := func(id, area, script, want string, exit int, source string) {
		result = append(result, probe{id, area, script, want, source, exit})
	}
	add("shell.parameter-default", "shell", `unset X; printf '%s' "${X:-默认}"`, "默认", 0, shell)
	add("shell.quoting", "shell", `X='a b'; set -- "$X"; printf '%s:%s' "$#" "$1"`, "1:a b", 0, shell)
	add("shell.for", "shell", `for x in a b c; do printf '%s' "$x"; done`, "abc", 0, shell)
	add("shell.while", "shell", `x=0; while [ "$x" -lt 3 ]; do printf '%s' "$x"; x=$((x+1)); done`, "012", 0, shell)
	add("shell.case", "shell", `case abc in a*) printf matched;; *) printf wrong;; esac`, "matched", 0, shell)
	add("shell.function", "shell", `f() { printf '%s' "$1"; }; f 参数`, "参数", 0, shell)
	add("shell.heredoc", "shell", "cat <<'END'\n$literal\nEND", "$literal\n", 0, shell)
	add("shell.pipeline-status", "shell", `false | true; printf '%s' "$?"`, "0", 0, shell)
	add("shell.and-or", "shell", `false && printf wrong; false || printf right`, "right", 0, shell)
	add("shell.redirect", "shell", `printf content > file; cat < file`, "content", 0, shell)
	add("shell.exit", "shell", `exit 7`, "", 7, shell)
	add("shell.subshell", "shell", `X=父; (X=子); printf '%s' "$X"`, "父", 0, shell)
	for _, item := range []struct {
		id, script, want string
		exit             int
	}{
		{"cat", `printf 'a\nb\n' > data; cat data`, "a\nb\n", 0},
		{"cp", `printf content > first; cp first second; cat second`, "content", 0},
		{"mv", `printf content > first; mv first second; cat second`, "content", 0},
		{"mkdir", `mkdir sub; test -d sub`, "", 0},
		{"rm", `printf data > file; rm file; test ! -f file`, "", 0},
		{"touch", `touch file; test -f file`, "", 0},
		{"basename", `basename /a/b.txt .txt`, "b\n", 0},
		{"dirname", `dirname /a/b.txt`, "/a\n", 0},
		{"head", `printf 'a\nb\nc\n' | head -n 2`, "a\nb\n", 0},
		{"tail", `printf 'a\nb\nc\n' | tail -n 2`, "b\nc\n", 0},
		{"wc", `printf 'a b\nc\n' | wc -l`, "2\n", 0},
		{"grep", `printf 'alpha\nbeta\n' | grep -n alpha`, "1:alpha\n", 0},
		{"sort", `printf 'b\na\n' | sort`, "a\nb\n", 0},
		{"uniq", `printf 'a\na\nb\n' | uniq`, "a\nb\n", 0},
		{"cut", `printf 'a:b\n' | cut -d : -f 2`, "b\n", 0},
		{"tee", `printf content | tee file; cat file`, "contentcontent", 0},
		{"seq", `seq 3`, "1\n2\n3\n", 0},
		{"seq-step", `seq 3 2 7`, "3\n5\n7\n", 0},
		{"sh", `sh -c 'printf shell'`, "shell", 0},
		{"awk", `printf 'a 2\n' | awk '{print $2}'`, "2\n", 0},
		{"sed", `printf 'old\n' | sed 's/old/new/'`, "new\n", 0},
		{"tr", `printf abc | tr a-z A-Z`, "ABC", 0},
		{"env", `env KEY=value sh -c 'printf "%s" "$KEY"'`, "value", 0},

		{"xargs", `printf 'a b\n' | xargs -n1 printf '%s-'`, "a-b-", 0},

		{"nohup", `nohup printf '%s' kept`, "kept", 0},
	} {
		add("utility."+item.id, "utilities", item.script, item.want, item.exit, base+item.id+".html")
	}

	add("kill.decode-status", "utilities", `kill -l 143; kill -l 9`, "SIGTERM\nSIGKILL\n", 0, base+"kill.html")
	add("kill.list-count", "utilities", `set -- $(kill -l); echo $#`, "31\n", 0, base+"kill.html")

	add("ps.header", "utilities", `ps -e -o pid | sed -n 1p`, "PID\n", 0, base+"ps.html")
	add("ls.classify", "utilities", `mkdir -p d && printf x > f && ls -F -d d f`, "d/\nf\n", 0, base+"ls.html")
	add("ls.directory-itself", "utilities", `mkdir -p d && ls -d d`, "d\n", 0, base+"ls.html")
	add("sort.key-offset", "utilities", `printf 'abY\nabX\n' | sort -k1.3`, "abX\nabY\n", 0, base+"sort.html")
	add("sort.check", "utilities", `printf 'a\nc\nb\n' | sort -c; echo $?`, "1\n", 0, base+"sort.html")
	add("cut.bytes", "utilities", `printf 'abcd\n' | cut -b 2-3`, "bc\n", 0, base+"cut.html")
	add("cut.complement", "utilities", `printf 'a:b:c\n' | cut --complement -d: -f2`, "a:c\n", 0, base+"cut.html")
	add("sed.read-file", "utilities", `printf 'R\n' > r.txt; printf 'a\n' | sed 'r r.txt'`, "a\nR\n", 0, base+"sed.html")
	add("sed.write-file", "utilities", `printf 'a\nb\n' > i.txt; sed -n 'w o.txt' i.txt; cat o.txt`, "a\nb\n", 0, base+"sed.html")
	add("sed.list", "utilities", `printf 'a\tb\n' | sed -n l`, "a\\tb$\n", 0, base+"sed.html")
	add("sed.in-place", "utilities", `printf 'a\nb\n' > f.txt; sed -i.bak 's/a/A/' f.txt; cat f.txt f.txt.bak`, "A\nb\na\nb\n", 0, base+"sed.html")
	add("sed.extended-regexp", "utilities", `printf 'aaa\n' | sed -E 's/a{2}/X/'`, "Xa\n", 0, base+"sed.html")
	add("sed.branch-loop", "utilities", `printf 'aaa\n' | sed ':x s/a/b/;tx'`, "bbb\n", 0, base+"sed.html")
	add("sed.block-multiline", "utilities", `printf '1\n2\n3\n' | sed -n '2{N;s/\n/-/;p}'`, "2-3\n", 0, base+"sed.html")
	add("sed.range-address", "utilities", `printf '1\n2\n3\n4\n' | sed -n '2,3p'`, "2\n3\n", 0, base+"sed.html")
	add("sed.transliterate", "utilities", `printf 'abc\n' | sed 'y/abc/xyz/'`, "xyz\n", 0, base+"sed.html")
	add("sed.hold-space", "utilities", `printf '1\n2\n' | sed -n '1h;2G;p'`, "1\n2\n1\n", 0, base+"sed.html")
	add("grep.basic-regexp-plus", "utilities", `printf 'a+b\nab\n' | grep 'a+b'`, "a+b\n", 0, base+"grep.html")
	add("grep.multiple-patterns", "utilities", `printf 'a\nb\nc\n' | grep -e a -e c`, "a\nc\n", 0, base+"grep.html")
	add("grep.line-regexp", "utilities", `printf 'beta\nbetamax\n' | grep -x beta`, "beta\n", 0, base+"grep.html")
	add("tail.from-line", "utilities", `printf 'a\nb\nc\nd\n' | tail -n +2`, "b\nc\nd\n", 0, base+"tail.html")
	add("cut.field-range", "utilities", `printf 'a:b:c\n' | cut -d : -f 1-2`, "a:b\n", 0, base+"cut.html")

	add("time.real", "utilities", `command time true 2>&1 | sed -n 1p`, "real 0.00\n", 0, base+"time.html")
	add("time.sys-unmeasurable", "utilities", `command time true 2>&1 | sed -n 3p`,
		"sys 不可测（客体没有用户态/内核态 CPU 计时）\n", 0, base+"time.html")
	add("time.exit-status", "utilities", `command time sh -c 'exit 3' 2>/dev/null; echo $?`, "3\n", 0, base+"time.html")
	add("time.missing-utility", "utilities", `command time 未注册的程序 2>/dev/null; echo $?`, "127\n", 0, base+"time.html")

	add("df.capability-absent", "utilities", `df -P 2>&1 | sed -n 1p`,
		"df: 文件系统没有容量能力: ENOTSUP：能力明确不支持\n", 0, base+"df.html")
	add("df.capability-absent-status", "utilities", `df -P 2>/dev/null; echo $?`, "2\n", 0, base+"df.html")
	add("df.missing-operand", "utilities", `df -P /nope 2>/dev/null; echo $?`, "2\n", 0, base+"df.html")

	add("mkfifo.capability-absent", "utilities", `mkfifo pipe 2>&1 | sed -n 1p`,
		"mkfifo: pipe: 文件系统不能创建命名管道: ENOTSUP：能力明确不支持\n", 0, base+"mkfifo.html")

	add("mkfifo.capability-absent-status", "utilities", `mkfifo pipe 2>/dev/null; echo $?`, "1\n", 0, base+"mkfifo.html")
	return result
}
