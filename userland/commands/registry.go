package commands

import (
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	arcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ar"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/at"
	awkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/awk"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/base64"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/basename"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/bash"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/batch"
	bccmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/bc"
	bgcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/bg"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/cat"
	chgrpcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/chgrp"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/chmod"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/chown"
	cksumcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/cksum"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/cmp"
	commcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/comm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/cp"
	crontabcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/crontab"
	csplitcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/csplit"
	ctagscmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ctags"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/cut"
	datecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/date"
	ddcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/dd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/df"
	diffcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/diff"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/dirname"
	ducmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/du"
	echocmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/echo"
	edcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ed"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/env"
	excmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ex"
	expandcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/expand"
	exprcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/expr"
	falsecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/false"
	fgcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/fg"
	filecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/file"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/find"
	foldcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/fold"
	getconfcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/getconf"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/grep"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/head"
	iconvcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/iconv"
	idcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/id"
	jobscmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/jobs"
	joincmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/join"
	killcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/kill"
	linkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/link"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ln"
	localecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/locale"
	localedefcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/localedef"
	loggercmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/logger"
	lognamecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/logname"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/lp"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ls"
	m4cmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/m4"
	mailxcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mailx"
	makecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/make"
	mancmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/man"
	mesgcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mesg"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/messages"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mkdir"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mkfifo"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mktemp"
	morecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/more"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/mv"
	newgrpcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/newgrp"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/nice"
	nlcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/nl"
	nmcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/nm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/nohup"
	odcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/od"
	pastecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/paste"
	patchcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/patch"
	pathchkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/pathchk"
	paxcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/pax"
	prcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/pr"
	printfcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/printf"
	pscmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/ps"
	pwdcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/pwd"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/readlink"
	renicecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/renice"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/rm"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/rmdir"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/sed"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/seq"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/sh"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/shasum"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/sleep"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/sort"
	splitcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/split"
	stringscmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/strings"
	stripcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/strip"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/stty"
	sumcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/sum"
	tabscmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tabs"
	taccmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tac"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tail"
	talkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/talk"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tee"
	testcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/test"
	timecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/time"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/touch"
	tputcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tput"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tr"
	truecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/true"
	tsortcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tsort"
	ttycmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/tty"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/uname"
	unexpandcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/unexpand"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/uniq"
	unlinkcmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/unlink"
	uudecodecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/uudecode"
	uuencodecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/uuencode"
	vicmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/vi"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/wc"
	whocmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/who"
	writecmd "github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/write"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/xargs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commands/yes"
)

func Registry(newRunner bash.RunnerFactory) map[string]commandio.Handler {
	return map[string]commandio.Handler{
		"ar":        arcmd.Run,
		"at":        at.Run,
		"awk":       awkcmd.Run,
		"base64":    base64.Run,
		"basename":  basename.Run,
		"bash":      bash.Command{NewRunner: newRunner}.Run,
		"batch":     batch.Run,
		"bc":        bccmd.Run,
		"bg":        bgcmd.Run,
		"cat":       cat.Run,
		"ctags":     ctagscmd.Run,
		"chgrp":     chgrpcmd.Run,
		"chown":     chown.Run,
		"chmod":     chmod.Run,
		"cksum":     cksumcmd.Run,
		"cmp":       cmp.Run,
		"comm":      commcmd.Run,
		"cp":        cp.Run,
		"csplit":    csplitcmd.Run,
		"cut":       cut.Run,
		"dd":        ddcmd.Run,
		"ed":        edcmd.Run,
		"df":        df.Run,
		"crontab":   crontabcmd.Run,
		"date":      datecmd.Run,
		"diff":      diffcmd.Run,
		"dirname":   dirname.Run,
		"du":        ducmd.Run,
		"echo":      echocmd.Run,
		"false":     falsecmd.Run,
		"env":       env.Run,
		"ex":        excmd.Run,
		"expand":    expandcmd.Run,
		"expr":      exprcmd.Run,
		"find":      find.Run,
		"fold":      foldcmd.Run,
		"getconf":   getconfcmd.Run,
		"grep":      grep.Run,
		"head":      head.Run,
		"iconv":     iconvcmd.Run,
		"id":        idcmd.Run,
		"join":      joincmd.Run,
		"file":      filecmd.Run,
		"fg":        fgcmd.Run,
		"jobs":      jobscmd.Run,
		"kill":      killcmd.Run,
		"link":      linkcmd.Run,
		"locale":    localecmd.Run,
		"localedef": localedefcmd.Run,
		"logger":    loggercmd.Run,
		"mailx":     mailxcmd.Run,
		"lp":        lp.Run,
		"ln":        ln.Run,
		"logname":   lognamecmd.Run,
		"ls":        ls.Run,
		"man":       mancmd.Run,
		"m4":        m4cmd.Run,
		"make":      makecmd.Run,
		"mesg":      mesgcmd.Run,
		"messages":  messages.Run,
		"mkdir":     mkdir.Run,
		"mkfifo":    mkfifo.Run,
		"mktemp":    mktemp.Run,
		"newgrp":    newgrpcmd.Run,
		"nm":        nmcmd.Run,
		"nice":      nice.Run,
		"mv":        mv.Run,
		"nohup":     nohup.Run,
		"renice":    renicecmd.Run,
		"strip":     stripcmd.Run,
		"tabs":      tabscmd.Run,
		"talk":      talkcmd.Run,
		"tput":      tputcmd.Run,
		"tty":       ttycmd.Run,
		"vi":        vicmd.Run,
		"more":      morecmd.Run,
		"nl":        nlcmd.Run,
		"od":        odcmd.Run,
		"paste":     pastecmd.Run,
		"patch":     patchcmd.Run,
		"pax":       paxcmd.Run,
		"pathchk":   pathchkcmd.Run,
		"pr":        prcmd.Run,
		"printf":    printfcmd.Run,
		"ps":        pscmd.Run,
		"pwd":       pwdcmd.Run,
		"readlink":  readlink.Run,
		"rmdir":     rmdir.Run,
		"rm":        rm.Run,
		"sed":       sed.Run,
		"seq":       seq.Run,
		"split":     splitcmd.Run,
		"sh":        sh.Command{NewRunner: newRunner}.Run,
		"shasum":    shasum.Run,
		"sort":      sort.Run,
		"strings":   stringscmd.Run,
		"stty":      stty.Run,
		"sum":       sumcmd.Run,
		"sleep":     sleep.Run,
		"tac":       taccmd.Run,
		"tail":      tail.Run,
		"test":      testcmd.Run,
		"time":      timecmd.Run,
		"tee":       tee.Run,
		"touch":     touch.Run,
		"true":      truecmd.Run,
		"tsort":     tsortcmd.Run,
		"tr":        tr.Run,
		"unlink":    unlinkcmd.Run,
		"uuencode":  uuencodecmd.Run,
		"unexpand":  unexpandcmd.Run,
		"uniq":      uniq.Run,
		"uname":     uname.Run,
		"uudecode":  uudecodecmd.Run,
		"wc":        wc.Run,
		"xargs":     xargs.Run,
		"who":       whocmd.Run,
		"write":     writecmd.Run,
		"yes":       yes.Run,
	}
}
