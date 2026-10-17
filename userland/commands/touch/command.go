package touch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/commandio"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/userland/toolcompat/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

func Run(ctx context.Context, args []string) error {
	flags := commandio.Flags(ctx, "touch")
	access := flags.BoolP("access-time", "a", false, "只改访问时间")
	noCreate := flags.BoolP("no-create", "c", false, "文件不存在时不创建")
	stamp := flags.StringP("time", "t", "", "用 [[CC]YY]MMDDhhmm[.SS] 形式的时间")
	modify := flags.BoolP("modification-time", "m", false, "只改修改时间")
	reference := flags.StringP("reference", "r", "", "用参考文件的时间")
	if err := commandio.Parse(flags, args); err != nil {
		return err
	}
	if len(flags.Args()) == 0 {
		return fmt.Errorf("touch 缺少文件")
	}
	p, err := proc.FromContext(ctx)
	if err != nil {
		return err
	}
	system, err := p.Resources.Filesystem()
	if err != nil {
		return err
	}
	timestamps, ok := system.(vmfs.Timestamps)
	if !ok {
		return errno.NotImplemented
	}
	source, err := p.Resources.Time()
	if err != nil {
		return err
	}
	now, err := source.Wall(ctx)
	if err != nil {
		return err
	}

	atime, mtime := now, now
	switch {
	case *reference != "":

		ref := commandio.Path(commandio.Directory(ctx), *reference)
		info, err := system.Stat(ctx, ref)
		if err != nil {
			return err
		}
		if atime, err = readAccessTime(ctx, system, ref); err != nil {
			return err
		}
		mtime = info.ModTime().UnixNano()
	case *stamp != "":
		moment, err := parseTimestamp(*stamp, clock.Unix(0, now).UTC().Year())
		if err != nil {
			return err
		}
		atime, mtime = moment, moment
	}

	changeAccess := *access || (!*access && !*modify)
	changeModify := *modify || (!*access && !*modify)
	for _, name := range flags.Args() {
		target := commandio.Path(commandio.Directory(ctx), name)
		info, statErr := system.Stat(ctx, target)
		switch {
		case statErr == nil:
		case *noCreate && missing(statErr):

			continue
		case *noCreate:

			return statErr
		default:
			file, err := commandio.Open(ctx, target, vmfs.Create|vmfs.WriteOnly, 0666)
			if err != nil {
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			if info, statErr = system.Stat(ctx, target); statErr != nil {
				return statErr
			}
		}
		fileAccess, fileModify := atime, mtime
		if !changeModify {

			fileModify = info.ModTime().UnixNano()
		}
		if !changeAccess {

			if fileAccess, err = readAccessTime(ctx, system, target); err != nil {
				return err
			}
		}
		if err := timestamps.SetTimes(ctx, target, fileAccess, fileModify); err != nil {
			return err
		}
	}
	return nil
}

func missing(err error) bool {
	return errors.Is(err, errno.NotFound) || errors.Is(err, fs.ErrNotExist)
}

func readAccessTime(ctx context.Context, system vmfs.System, target string) (int64, error) {
	times, ok := system.(vmfs.AccessTimes)
	if !ok {
		return 0, errno.Wrap(errno.NotSupported, "客体文件系统不支持读取访问时间")
	}
	return times.AccessTime(ctx, target)
}

func parseTimestamp(text string, year int) (int64, error) {
	body, seconds := text, 0
	hasSeconds := false
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		body = text[:dot]
		fraction := text[dot+1:]
		if len(fraction) != 2 || !allDigits(fraction) {
			return 0, fmt.Errorf("时间格式无效: %s", text)
		}
		seconds, _ = atoiDigits(fraction)
		hasSeconds = true
	}
	if !allDigits(body) {
		return 0, fmt.Errorf("时间格式无效: %s", text)
	}
	switch len(body) {
	case 8:

	case 10:
		two, _ := atoiDigits(body[:2])
		if two >= 69 {
			year = 1900 + two
		} else {
			year = 2000 + two
		}
		body = body[2:]
	case 12:
		century, _ := atoiDigits(body[:2])
		two, _ := atoiDigits(body[2:4])
		year = century*100 + two
		body = body[4:]
	default:
		return 0, fmt.Errorf("时间格式无效: %s", text)
	}
	month, _ := atoiDigits(body[0:2])
	day, _ := atoiDigits(body[2:4])
	hour, _ := atoiDigits(body[4:6])
	minute, _ := atoiDigits(body[6:8])
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59 {
		return 0, fmt.Errorf("时间取值越界: %s", text)
	}
	if hasSeconds && seconds > 61 {
		return 0, fmt.Errorf("秒取值越界: %s", text)
	}
	moment := clock.Date(year, clock.Month(month), day, hour, minute, seconds, 0, clock.UTC)

	if gotYear, gotMonth, gotDay := moment.Year(), moment.Month(), moment.Day(); gotYear != year || int(gotMonth) != month || gotDay != day {
		return 0, fmt.Errorf("时间取值越界: %s", text)
	}
	nanoseconds := moment.UnixNano()

	if clock.Unix(0, nanoseconds) != moment {
		return 0, fmt.Errorf("时间超出可表示范围: %s", text)
	}
	return nanoseconds, nil
}

func allDigits(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return false
		}
	}
	return true
}

func atoiDigits(text string) (int, error) {
	value := 0
	for i := 0; i < len(text); i++ {
		value = value*10 + int(text[i]-'0')
	}
	return value, nil
}
