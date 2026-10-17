package ps

import (
	"cmp"
	"context"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

var defaultColumns = []string{"pid", "state", "comm"}

var fullColumns = []string{"pid", "ppid", "pgid", "sid", "state", "comm"}

func Run(ctx context.Context, args []string) error {
	h := commandio.Context(ctx)
	flags := commandio.Flags(ctx, "ps")
	var all, full bool
	flags.BoolVarP(&all, "all", "e", false, "列出全部进程")
	flags.BoolVarP(&all, "all-A", "A", false, "列出全部进程")
	flags.BoolVarP(&full, "full", "f", false, "完整列集合（POSIX -f 的子集）")
	pids := flags.StringArrayP("pid", "p", nil, "按 pid 选择（可重复，逗号分隔）")
	columns := flags.StringP("format", "o", "", "列集合（逗号分隔）")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	self, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	control, ok := self.Control.(proc.TableControl)
	if !ok {
		return fmt.Errorf("ps: 客体没有进程表端口")
	}
	selected := defaultColumns
	if full {
		selected = fullColumns
	}

	if flags.Changed("format") {
		selected, err = parseColumns(*columns)
		if err != nil {
			return err
		}
	}
	wanted, err := parsePIDs(*pids)
	if err != nil {
		return err
	}
	processes := control.Processes()
	slices.SortStableFunc(processes, func(left, right proc.Proc) int {
		return cmp.Compare(left.ID, right.ID)
	})
	rows := make([][]string, 0, len(processes))
	for _, process := range processes {
		if len(wanted) > 0 {
			if !wanted[process.ID] {
				continue
			}
		} else if !all && process.SID != self.SID {

			continue
		}
		row := make([]string, 0, len(selected))
		for _, column := range selected {
			row = append(row, columnValue(process, column))
		}
		rows = append(rows, row)
	}
	header := make([]string, 0, len(selected))
	for _, column := range selected {
		header = append(header, strings.ToUpper(column))
	}
	if err := printRow(h.Stdout, header); err != nil {
		return err
	}
	for _, row := range rows {
		if err := printRow(h.Stdout, row); err != nil {
			return err
		}
	}
	return nil
}

func parseColumns(text string) ([]string, error) {
	columns := make([]string, 0, 4)
	for _, piece := range strings.Split(text, ",") {
		column := strings.TrimSpace(strings.ToLower(piece))
		if column == "" {
			continue
		}
		if !knownColumns[column] {
			return nil, fmt.Errorf("ps: 未知列 %s（可用：%s）", piece, strings.Join(columnNames(), ", "))
		}
		columns = append(columns, column)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("ps: -o 需要至少一个列名")
	}
	return columns, nil
}

var knownColumns = map[string]bool{
	"pid": true, "ppid": true, "pgid": true, "sid": true, "nice": true,
	"state": true, "comm": true, "args": true, "dir": true, "umask": true,
}

func columnNames() []string {
	names := make([]string, 0, len(knownColumns))
	for name := range knownColumns {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func columnValue(process proc.Proc, column string) string {
	switch column {
	case "pid":
		return strconv.FormatUint(uint64(process.ID), 10)
	case "ppid":
		return strconv.FormatUint(uint64(process.Parent), 10)
	case "pgid":
		return strconv.FormatUint(uint64(process.PGID), 10)
	case "sid":
		return strconv.FormatUint(uint64(process.SID), 10)
	case "nice":
		return strconv.Itoa(process.Nice)
	case "state":
		return process.State.String()
	case "comm":
		return path.Base(process.Program)
	case "args":
		return strings.Join(append([]string{process.Program}, process.Args...), " ")
	case "dir":
		return process.Dir
	case "umask":
		return fmt.Sprintf("%04o", uint32(process.Umask))
	default:
		return ""
	}
}

func parsePIDs(values []string) (map[proc.PID]bool, error) {
	wanted := map[proc.PID]bool{}
	for _, value := range values {
		for _, piece := range strings.Split(value, ",") {
			piece = strings.TrimSpace(piece)
			if piece == "" {
				continue
			}
			number, err := strconv.ParseUint(piece, 10, 32)
			if err != nil || number == 0 {
				return nil, fmt.Errorf("ps: 无效的 pid: %s", piece)
			}
			wanted[proc.PID(number)] = true
		}
	}
	return wanted, nil
}

func printRow(writer interface{ Write([]byte) (int, error) }, row []string) error {
	line := strings.Join(row, " ")
	if _, err := writer.Write([]byte(line + "\n")); err != nil {
		return err
	}
	return nil
}
