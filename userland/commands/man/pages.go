package man

import "strings"

type entry struct {
	Name    string
	Section string
	Summary string

	Body []string
}

var pages = []entry{
	{
		Name: "man", Section: "1", Summary: "an interface to the object manuals",
		Body: []string{
			"SYNOPSIS",
			"     man [-k|-f] section... name...",
			"",
			"DESCRIPTION",
			"     查询客体的手册集。手册集是客体自有的声明数据（不读宿主 man 树，",
			"     没有 roff 排版）：每页含标题、小节与客体差异陈述。",
			"     -k 按名字与描述子串检索 whatis 条目（大小写不敏感）；-f 按名字",
			"     精确检索。前导纯数字操作数按小节跳过（man 1 at 的惯例）。",
			"     渲染宽度取标准描述符上终端的列数；非终端按 66 列断行。",
		},
	},
	{
		Name: "nice", Section: "1", Summary: "invoke a utility with an adjusted scheduling priority",
		Body: []string{
			"SYNOPSIS",
			"     nice [-n increment] utility [argument...]",
			"",
			"DESCRIPTION",
			"     以「父进程 nice + increment」为初始优先级启动 utility（真子进程，",
			"     退出码取 utility）。无 utility 时输出自身当前 nice 值。",
			"     优先级是进程事实（-20..19，子进程继承）：它真实影响就绪队列的",
			"     排序——常驻作业调度器（at/batch 到期作业）按 (nice, 提交序) 选择",
			"     执行。客体是协作式串行执行、没有抢占点，已在执行中的任务不受",
			"     影响——这是声明的边界。",
		},
	},
	{
		Name: "renice", Section: "1", Summary: "alter priority of running processes",
		Body: []string{
			"SYNOPSIS",
			"     renice [-n increment] [-p | -g | -u] id...",
			"     renice priority -p id...  (obsolete, absolute)",
			"",
			"DESCRIPTION",
			"     调整既有进程的调度优先级。-p 按 PID（缺省）、-g 按进程组、",
			"     -u 按用户（客体单用户：只有声明身份是真实取值）。",
			"     新值影响就绪队列排序（同 nice）；不抢占正在执行的任务。",
		},
	},
	{
		Name: "at", Section: "1", Summary: "execute commands at a later time",
		Body: []string{
			"SYNOPSIS",
			"     at [-m] [-f file] [-q queue] time...",
			"     at -r | -d job...",
			"     at -c job...",
			"     at -l [job...]",
			"",
			"DESCRIPTION",
			"     把作业排队到指定时刻。作业写入 /var/spool/at（文件名即作业号）；",
			"     执行由常驻调度检查点完成（时钟推进与命令边界），到期作业以提交",
			"     时刻的 nice 优先级运行，输出（或 -m 声明）投递到 /var/mail/<user>。",
			"     时间取客体逻辑时钟：HH:MM、YYYY-MM-DD [HH:MM[:SS]]、now、",
			"     now + N unit（minutes/hours/days/weeks）。-q b 是 batch 队列。",
		},
	},
	{
		Name: "batch", Section: "1", Summary: "execute commands when the load permits",
		Body: []string{
			"SYNOPSIS",
			"     batch [-m] [-f file] [time]",
			"",
			"DESCRIPTION",
			"     与 at 唯一的区别是队列（b）：常驻调度器只在同一时刻没有到期的",
			"     at 作业时运行 batch 作业——这就是「负载允许」在单 CPU 串行客体",
			"     里的定义。排队、检查点执行与邮件投递与 at 同源。",
		},
	},
	{
		Name: "vi", Section: "1", Summary: "screen-oriented (visual) display editor",
		Body: []string{
			"SYNOPSIS",
			"     vi [file]",
			"",
			"DESCRIPTION",
			"     光标寻址的屏幕编辑器。渲染经终端的屏幕模型（光标定位/清行），",
			"     输入在非规范模式下按字节读取；命令子集：h j k l w b 0 $ ^ G、",
			"     x dd D i a o O p P u、/ ? n N、:w :q :wq :q! :n 与行号定位。",
			"     -r（恢复）、-t（tag）、-w（窗口）如实拒绝；标准流不是终端时失败。",
		},
	},
	{
		Name: "tabs", Section: "1", Summary: "set terminal tabs",
		Body: []string{
			"SYNOPSIS",
			"     tabs [-n]",
			"",
			"DESCRIPTION",
			"     设置终端制表位。-n 每 n 列一站（1..62）；无操作数恢复缺省",
			"     （每 8 列）。+m[n]（边距）与其它 type 名没有声明语义，如实拒绝。",
		},
	},
	{
		Name: "lp", Section: "1", Summary: "submit print requests",
		Body: []string{
			"SYNOPSIS",
			"     lp [-d destination] [-n copies] [-c] [-m] [-s] [-t title] [-w] [file]",
			"",
			"DESCRIPTION",
			"     提交打印请求。请求写入 /var/spool/lp/requests（请求号 lp0-<序号>），",
			"     排空在调度检查点完成：内容按份数追加到 /var/spool/lp/lp0（客体的",
			"     打印目的地没有真实设备，落盘文件就是全部事实）。非 -c 请求体是",
			"     文件引用，排空时再读。-m 邮件完成通知、-w 终端通知（退化为邮件）。",
		},
	},
	{
		Name: "talk", Section: "1", Summary: "talk to another user",
		Body: []string{
			"SYNOPSIS",
			"     talk user",
			"",
			"DESCRIPTION",
			"     与同一终端上的另一个 talk 会话对话。对话通道是声明文件",
			"     /var/spool/talk/channel（轮转缓冲）：本地输入行以 [me] 回显，",
			"     对端行以 [peer] 显示。user 只接受声明身份；真实 talk 的分屏",
			"     需要第二台终端，客体没有——这是声明的边界。EOF 退出。",
		},
	},
}

func lookup(name string) (entry, bool) {
	for _, page := range pages {
		if page.Name == name {
			return page, true
		}
	}
	return entry{}, false
}

func whatisEntries() []entry { return pages }

func (e entry) render(width int) string {
	var out strings.Builder
	title := e.Name + "(" + e.Section + ")"
	if pad := width - len(title) - 5; pad > 0 {
		out.WriteString(title)
		out.WriteString(strings.Repeat(" ", pad))
		out.WriteString("手册\n")
	} else {
		out.WriteString(title + " 手册\n")
	}
	for _, line := range e.Body {
		if strings.HasPrefix(line, "SYNOPSIS") || strings.HasPrefix(line, "DESCRIPTION") {
			out.WriteString(strings.ToUpper(line[:1]) + line[1:] + "\n")
			continue
		}
		out.WriteString(wrap(line, width) + "\n")
	}
	return out.String()
}

func wrap(line string, width int) string {
	if width < 20 || len(line) <= width {
		return line
	}
	var out strings.Builder
	column := 0
	for _, r := range line {
		size := 1
		if r > 0x7F {
			size = 2
		}
		if column+size > width {
			out.WriteString("\n     ")
			column = 5
		}
		out.WriteRune(r)
		column += size
	}
	return out.String()
}
