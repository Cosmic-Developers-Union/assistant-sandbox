package commands_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"io"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

func TestRegistryContractAndIsolation(t *testing.T) {
	first := commands.Registry(testRunner)
	second := commands.Registry(testRunner)
	want := "ar at awk base64 basename bash batch bc bg cat chgrp chmod chown cksum cmp comm cp crontab csplit ctags cut date dd df diff dirname du echo ed env ex expand expr false fg file find fold getconf grep head iconv id jobs join kill link ln locale localedef logger logname lp ls m4 mailx make man mesg messages mkdir mkfifo mktemp more mv newgrp nice nl nm nohup od paste patch pathchk pax pr printf ps pwd readlink renice rm rmdir sed seq sh shasum sleep sort split strings strip stty sum tabs tac tail talk tee test time touch tput tr true tsort tty uname unexpand uniq unlink uudecode uuencode vi wc who write xargs yes"
	if got := strings.Join(slices.Sorted(maps.Keys(first)), " "); got != want {
		t.Fatal(got)
	}
	delete(first, "wc")
	if second["wc"] == nil {
		t.Fatal("注册表相互影响")
	}
	r := commandtest.Run(t, second["bash"], t.TempDir(), []string{"-c", "printf 子解释器"}, "")
	if r.Code != 0 || r.Stdout != "子解释器" {
		t.Fatal(r)
	}
}

func testRunner(dir string, env []string, in io.Reader, out, diagnostic io.Writer) (*interp.Runner, error) {
	return interp.New(interp.Dir(dir), interp.Env(expand.ListEnviron(env...)), interp.StdIO(in, out, diagnostic))
}
