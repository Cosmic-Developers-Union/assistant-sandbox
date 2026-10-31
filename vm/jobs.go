package vm

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"sort"
	"strings"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/errno"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fd"
	vmfs "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/fs"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

const (
	AtSpoolDir = "/var/spool/at"

	PrintRequestDir = "/var/spool/lp/requests"

	PrintSink = "/var/spool/lp/lp0"

	MailDir = "/var/mail"

	DefaultPrintDestination = "lp0"
)

const jobOutputLimit = 256 << 10

const (
	headerDeadline = "deadline"
	headerNice     = "nice"
	headerDir      = "dir"
	headerMail     = "mail"
	headerNotify   = "notify"
	headerCopies   = "copies"
	headerTitle    = "title"
	headerSource   = "source"
)

func (k *Kernel) QueueJob(ctx context.Context, spec proc.JobSpec) (string, error) {
	if spec.Queue != 'a' && spec.Queue != 'b' {
		return "", fmt.Errorf("作业队列只能是 a 或 b: %w", errno.Invalid)
	}
	if spec.Dir == "" || spec.Dir[0] != '/' {
		return "", fmt.Errorf("作业需要客体绝对目录: %w", errno.Invalid)
	}
	if spec.Nice < proc.NiceLow || spec.Nice > proc.NiceHigh {
		return "", fmt.Errorf("作业优先级 %d 越界: %w", spec.Nice, errno.Invalid)
	}
	system, err := k.services.Filesystem()
	if err != nil {
		return "", err
	}
	seq, err := nextSpoolSeq(ctx, system, AtSpoolDir, string(spec.Queue))
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s%04d", string(spec.Queue), seq)
	var header strings.Builder
	fmt.Fprintf(&header, "#! deadline: %d\n", spec.Deadline)
	fmt.Fprintf(&header, "#! nice: %d\n", spec.Nice)
	fmt.Fprintf(&header, "#! dir: %s\n", spec.Dir)
	if spec.Mail {
		header.WriteString("#! mail: yes\n")
	}
	for _, key := range sortedKeys(spec.Env) {
		fmt.Fprintf(&header, "#! env-%s: %s\n", key, sanitizeHeaderValue(spec.Env[key]))
	}
	if err := writeSpoolFile(ctx, system, AtSpoolDir, name, []byte(header.String()+spec.Script)); err != nil {
		return "", err
	}
	return name, nil
}

func (k *Kernel) ListJobs(ctx context.Context) ([]string, error) {
	system, err := k.services.Filesystem()
	if err != nil {
		return nil, err
	}
	return spoolNames(ctx, system, AtSpoolDir)
}

func (k *Kernel) RemoveJob(ctx context.Context, id string) (bool, error) {
	system, err := k.services.Filesystem()
	if err != nil {
		return false, err
	}
	if !validJobName(id) {
		return false, fmt.Errorf("作业号 %q 不是客体作业名: %w", id, errno.Invalid)
	}
	if err := system.Remove(ctx, AtSpoolDir+"/"+id); err != nil {

		if errors.Is(err, errno.NotFound) || errors.Is(err, stdfs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (k *Kernel) QueuePrint(ctx context.Context, spec proc.PrintSpec) (string, error) {
	if spec.Destination != DefaultPrintDestination {
		return "", fmt.Errorf("没有打印目的地 %q（客体只声明 %s）: %w", spec.Destination, DefaultPrintDestination, errno.NotFound)
	}
	if spec.Copies < 1 {
		return "", fmt.Errorf("份数必须为正数: %w", errno.Invalid)
	}
	system, err := k.services.Filesystem()
	if err != nil {
		return "", err
	}
	seq, err := nextSpoolSeq(ctx, system, PrintRequestDir, spec.Destination+"-")
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%04d", spec.Destination, seq)
	var header strings.Builder
	fmt.Fprintf(&header, "#! copies: %d\n", spec.Copies)
	fmt.Fprintf(&header, "#! source: %s\n", map[bool]string{true: "copy", false: "reference"}[spec.Copy])
	if spec.Title != "" {
		fmt.Fprintf(&header, "#! title: %s\n", sanitizeHeaderValue(spec.Title))
	}
	if spec.Notify {
		header.WriteString("#! mail: yes\n")
	}
	if spec.Write {
		header.WriteString("#! notify: yes\n")
	}
	if err := writeSpoolFile(ctx, system, PrintRequestDir, name, []byte(header.String()+spec.Body)); err != nil {
		return "", err
	}
	return name, nil
}

func (k *Kernel) ReadJob(ctx context.Context, id string) (string, error) {
	system, err := k.services.Filesystem()
	if err != nil {
		return "", err
	}
	if !validJobName(id) {
		return "", fmt.Errorf("作业号 %q 不是客体作业名: %w", id, errno.Invalid)
	}
	file, err := system.Open(ctx, AtSpoolDir+"/"+id, vmfs.ReadOnly, 0)
	if err != nil {
		return "", err
	}
	data, err := readAllBounded(ctx, file, jobOutputLimit)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", errors.Join(err, closeErr)
	}
	return string(data), nil
}

func (k *Kernel) ListPrintRequests(ctx context.Context) ([]string, error) {
	system, err := k.services.Filesystem()
	if err != nil {
		return nil, err
	}
	return spoolNames(ctx, system, PrintRequestDir)
}

func (k *Kernel) RunDueJobs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	k.jobsMu.Lock()
	if k.jobsRunning || k.closed {
		k.jobsMu.Unlock()
		return nil
	}
	k.jobsRunning = true
	k.jobsMu.Unlock()
	defer func() {
		k.jobsMu.Lock()
		k.jobsRunning = false
		k.jobsMu.Unlock()
	}()
	if err := k.runDueAtJobs(ctx); err != nil {
		return err
	}
	return k.drainPrintQueue(ctx)
}

func (k *Kernel) runDueAtJobs(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		system, err := k.services.Filesystem()
		if err != nil {
			return err
		}
		now, err := k.nowNanoseconds(ctx)
		if err != nil {
			return err
		}
		names, err := spoolNames(ctx, system, AtSpoolDir)
		if err != nil {
			return err
		}
		var due, batch []pendingJob
		for _, name := range names {
			job, err := readAtJob(ctx, system, AtSpoolDir, name)
			if err != nil {

				continue
			}
			if job.deadline > now {
				continue
			}
			if name[0] == 'a' {
				due = append(due, job)
			} else {
				batch = append(batch, job)
			}
		}

		if len(due) == 0 {
			due = batch
		}
		if len(due) == 0 {
			return nil
		}
		sort.SliceStable(due, func(i, j int) bool {
			if due[i].nice != due[j].nice {
				return due[i].nice < due[j].nice
			}
			return due[i].name < due[j].name
		})
		if err := k.runAtJob(ctx, due[0]); err != nil {
			return err
		}
	}
}

type pendingJob struct {
	name     string
	deadline uint64
	nice     int
	dir      string
	env      map[string]string
	mail     bool
	script   string
}

func splitSpoolHeader(data string) (headers map[string]string, body string) {
	headers = map[string]string{}
	offset := 0
	for offset < len(data) {
		lineStart := offset
		end := strings.IndexByte(data[offset:], '\n')
		if end < 0 {
			offset = len(data)
		} else {
			offset += end + 1
		}
		line := data[lineStart:offset]
		line = strings.TrimSuffix(line, "\n")
		if strings.HasPrefix(line, "#! ") {
			if key, value, found := strings.Cut(strings.TrimPrefix(line, "#! "), ": "); found {
				headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
			continue
		}
		return headers, data[lineStart:]
	}
	return headers, ""
}

func readAtJob(ctx context.Context, system vmfs.System, dir, name string) (pendingJob, error) {
	file, err := system.Open(ctx, dir+"/"+name, vmfs.ReadOnly, 0)
	if err != nil {
		return pendingJob{}, err
	}
	data, readErr := readAllBounded(ctx, file, jobOutputLimit)
	closeErr := file.Close()
	if readErr != nil {
		return pendingJob{}, readErr
	}
	if closeErr != nil {
		return pendingJob{}, closeErr
	}
	headers, body := splitSpoolHeader(string(data))
	job := pendingJob{name: name, env: map[string]string{}, dir: "/"}
	parsed, err := parseUint(headers[headerDeadline])
	if err != nil {
		return pendingJob{}, fmt.Errorf("作业 %s 到期时刻无效", name)
	}
	job.deadline = parsed
	nice, err := parseNice(headers[headerNice])
	if err != nil {
		return pendingJob{}, fmt.Errorf("作业 %s 优先级无效", name)
	}
	job.nice = nice
	job.dir = headers[headerDir]
	job.mail = headers[headerMail] == "yes"
	for key, value := range headers {
		if envKey, ok := strings.CutPrefix(key, "env-"); ok && envKey != "" {
			job.env[envKey] = value
		}
	}
	if job.dir == "" || job.dir[0] != '/' {
		return pendingJob{}, fmt.Errorf("作业 %s 缺少有效目录", name)
	}
	job.script = body
	return job, nil
}

func parseNice(value string) (int, error) {
	negative := strings.HasPrefix(value, "-")
	digits := strings.TrimPrefix(value, "-")
	parsed, err := parseUint(digits)
	if err != nil {
		return 0, err
	}
	if negative {
		if parsed > uint64(-proc.NiceLow) {
			return 0, errno.Invalid
		}
		return -int(parsed), nil
	}
	if parsed > uint64(proc.NiceHigh) {
		return 0, errno.Invalid
	}
	return int(parsed), nil
}

func (k *Kernel) runAtJob(ctx context.Context, job pendingJob) error {
	claimed, err := k.RemoveJob(ctx, job.name)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	output, exitCode := k.runBoundedScript(ctx, job.dir, job.env, job.nice, job.script)
	if job.mail || len(output) > 0 {
		subject := fmt.Sprintf("客体作业 %s 的输出", job.name)
		if exitCode != 0 {
			subject = fmt.Sprintf("客体作业 %s 以退出码 %d 结束", job.name, exitCode)
		}
		body := string(output)
		if len(output) >= jobOutputLimit {
			body += "\n[输出超出收集上限，已截断]\n"
		}
		if err := k.deliverMail(ctx, subject, body); err != nil {

			_ = k.logNotice(ctx, "mail", fmt.Sprintf("作业 %s 的邮件投递失败: %v", job.name, err))
		}
	}
	return nil
}

func (k *Kernel) runBoundedScript(ctx context.Context, dir string, env map[string]string, nice int, script string) ([]byte, uint8) {
	buffer := &boundedBuffer{limit: jobOutputLimit}
	child, err := k.Begin(0, "bash", []string{"-c", script}, dir, env, fd.Streams{
		Input:  strings.NewReader(""),
		Output: buffer,
	})
	if err != nil {
		return []byte(fmt.Sprintf("作业无法启动: %v\n", err)), 127
	}
	k.mu.Lock()
	_ = k.table.SetNice(child.ID, nice)
	k.mu.Unlock()
	status, runErr := k.RunRegistered(ctx, child)
	if status.Signal == 0 && status.Code == 0 && runErr != nil {
		status.Code = 1
	}
	if finishErr := k.Finish(0, child.ID, status); finishErr != nil {
		_ = k.logNotice(ctx, "jobs", fmt.Sprintf("作业进程 %d 回收失败: %v", child.ID, finishErr))
	} else {
		_, _, _ = k.WaitChild(context.Background(), 0, child.ID, true)
	}
	return buffer.data, status.Code
}

type boundedBuffer struct {
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.data = append(b.data, p...)
	}
	return len(p), nil
}

func (k *Kernel) drainPrintQueue(ctx context.Context) error {
	system, err := k.services.Filesystem()
	if err != nil {
		return err
	}
	names, err := spoolNames(ctx, system, PrintRequestDir)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := k.printOne(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) printOne(ctx context.Context, name string) error {
	system, err := k.services.Filesystem()
	if err != nil {
		return err
	}
	file, err := system.Open(ctx, PrintRequestDir+"/"+name, vmfs.ReadOnly, 0)
	if err != nil {
		if errors.Is(err, errno.NotFound) || errors.Is(err, stdfs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, readErr := readAllBounded(ctx, file, jobOutputLimit)
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := system.Remove(ctx, PrintRequestDir+"/"+name); err != nil {
		return err
	}
	request, body, err := parsePrintRequest(string(data))
	if err != nil {
		_ = k.deliverMail(ctx, "打印请求 "+name+" 无法解析", err.Error())
		return nil
	}
	content, err := k.printSource(ctx, request, body)
	if err != nil {
		_ = k.deliverMail(ctx, "打印请求 "+name+" 失败", err.Error())
		return nil
	}
	for copy := 0; copy < request.copies; copy++ {
		if copy > 0 {

			if _, err := appendSpool(ctx, system, PrintSink, []byte("\f")); err != nil {
				_ = k.deliverMail(ctx, "打印请求 "+name+" 写入目的地失败", err.Error())
				return nil
			}
		}
		if _, err := appendSpool(ctx, system, PrintSink, content); err != nil {
			_ = k.deliverMail(ctx, "打印请求 "+name+" 写入目的地失败", err.Error())
			return nil
		}
	}
	if request.mail || request.write {
		subject := "打印请求 " + name + " 已完成"
		switch {
		case request.write:
			if err := k.notifyTerminal(ctx, subject+"\n"); err != nil {

				_ = k.deliverMail(ctx, subject, "")
			}
		case request.mail:
			_ = k.deliverMail(ctx, subject, "")
		}
	}
	return nil
}

func (k *Kernel) printSource(ctx context.Context, request printRequest, body string) ([]byte, error) {
	if request.copy {
		return []byte(body), nil
	}
	system, err := k.services.Filesystem()
	if err != nil {
		return nil, err
	}
	file, err := system.Open(ctx, body, vmfs.ReadOnly, 0)
	if err != nil {
		return nil, fmt.Errorf("引用的文件 %s 读不到: %w", body, err)
	}
	data, err := readAllBounded(ctx, file, jobOutputLimit)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	return data, nil
}

type printRequest struct {
	copies int
	title  string
	copy   bool
	mail   bool
	write  bool
}

func parsePrintRequest(data string) (printRequest, string, error) {
	request := printRequest{copies: 1}
	headers, body := splitSpoolHeader(data)
	if value := headers[headerCopies]; value != "" {
		parsed, err := parseUint(value)
		if err != nil || parsed < 1 || parsed > uint64(^uint(0)>>1) {
			return request, "", fmt.Errorf("份数无效")
		}
		request.copies = int(parsed)
	}
	request.title = headers[headerTitle]
	request.copy = headers[headerSource] == "copy"
	request.mail = headers[headerMail] == "yes"
	request.write = headers[headerNotify] == "yes"
	return request, body, nil
}

func (k *Kernel) notifyTerminal(ctx context.Context, text string) error {
	k.mu.Lock()
	fds, init := k.fds, k.init
	k.mu.Unlock()
	if fds == nil {
		return errno.NotImplemented
	}
	info, err := fds.Terminal(ctx, uint64(init), 1)
	if err != nil || !info.TTY {
		return errno.NotSupported
	}
	stream, err := fds.Get(ctx, uint64(init), 1)
	if err != nil {
		return err
	}
	_, err = stream.Write([]byte(text))
	return err
}

func (k *Kernel) deliverMail(ctx context.Context, subject, body string) error {
	user := "sandbox"
	if source, err := k.services.Credentials(); err == nil {
		if account, err := source.Lookup(ctx, 0); err == nil && account.Name != "" {
			user = account.Name
		}
	}
	now, err := k.nowNanoseconds(ctx)
	if err != nil {
		return err
	}
	var message strings.Builder
	fmt.Fprintf(&message, "From sandbox@%s %s\n", user, mboxTimestamp(now))
	fmt.Fprintf(&message, "From: 客体调度器 <sandbox>\n")
	fmt.Fprintf(&message, "To: %s\n", user)
	fmt.Fprintf(&message, "Subject: %s\n", sanitizeHeaderValue(subject))
	message.WriteString("\n")
	if body != "" {

		message.WriteString(strings.ReplaceAll(body, "\nFrom ", "\n>From "))
		if !strings.HasSuffix(body, "\n") {
			message.WriteString("\n")
		}
	}
	system, err := k.services.Filesystem()
	if err != nil {
		return err
	}
	_, err = appendSpool(ctx, system, MailDir+"/"+user, []byte(message.String()))
	return err
}

func (k *Kernel) logNotice(ctx context.Context, tag, message string) error {
	system, err := k.services.Filesystem()
	if err != nil {
		return err
	}
	now, err := k.nowNanoseconds(ctx)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("%s %s: %s\n", utcTimestamp(now), tag, message)
	_, err = appendSpool(ctx, system, "/var/log/messages", []byte(line))
	return err
}

func (k *Kernel) nowNanoseconds(ctx context.Context) (uint64, error) {
	if clk, err := k.services.Time(); err == nil {
		return clk.Monotonic(ctx)
	}
	return 0, nil
}

func spoolNames(ctx context.Context, system vmfs.System, dir string) ([]string, error) {
	entries, err := system.ReadDir(ctx, dir)
	if err != nil {

		if errors.Is(err, errno.NotFound) || errors.Is(err, stdfs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func nextSpoolSeq(ctx context.Context, system vmfs.System, dir, prefix string) (uint64, error) {
	names, err := spoolNames(ctx, system, dir)
	if err != nil {
		return 0, err
	}
	next := uint64(1)
	for _, name := range names {
		digits, ok := strings.CutPrefix(name, prefix)
		if !ok {
			continue
		}
		value, err := parseUint(digits)
		if err != nil {
			continue
		}
		if value >= next {
			next = value + 1
		}
	}
	return next, nil
}

func writeSpoolFile(ctx context.Context, system vmfs.System, dir, name string, data []byte) error {
	if err := mkdirAll(ctx, system, dir); err != nil {
		return err
	}
	file, err := system.Open(ctx, dir+"/"+name, vmfs.WriteOnly|vmfs.Create|vmfs.Exclusive, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func appendSpool(ctx context.Context, system vmfs.System, name string, data []byte) (int, error) {
	if err := mkdirAll(ctx, system, path.Dir(name)); err != nil {
		return 0, err
	}
	file, err := system.Open(ctx, name, vmfs.WriteOnly|vmfs.Create|vmfs.Append, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := file.Write(data)
	closeErr := file.Close()
	if err != nil {
		return n, err
	}
	return n, closeErr
}

func readAllBounded(ctx context.Context, file vmfs.File, limit int) ([]byte, error) {
	data := make([]byte, 0, 512)
	buffer := make([]byte, 4096)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			if room := limit - len(data); room > 0 {
				if n > room {
					n = room
				}
				data = append(data, buffer[:n]...)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return data, nil
			}
			return data, err
		}
	}
}

func mkdirAll(ctx context.Context, system vmfs.System, name string) error {
	_, statErr := system.Stat(ctx, name)
	if statErr == nil {
		return nil
	}

	if !errors.Is(statErr, errno.NotFound) && !errors.Is(statErr, stdfs.ErrNotExist) {
		return statErr
	}
	if parent := path.Dir(name); parent != name {
		if err := mkdirAll(ctx, system, parent); err != nil {
			return err
		}
	}
	if err := system.Mkdir(ctx, name, 0o755); err != nil {

		if _, statErr := system.Stat(ctx, name); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func validJobName(name string) bool {
	if len(name) < 5 || (name[0] != 'a' && name[0] != 'b') {
		return false
	}
	for i := 1; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sanitizeHeaderValue(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, "\n", " ")), " ")
}

func parseUint(text string) (uint64, error) {
	if text == "" {
		return 0, errno.Invalid
	}
	var value uint64
	for i := 0; i < len(text); i++ {
		digit := text[i]
		if digit < '0' || digit > '9' {
			return 0, errno.Invalid
		}
		if value > (1<<63-1)/10 {
			return 0, errno.Invalid
		}
		value = value*10 + uint64(digit-'0')
	}
	return value, nil
}

func utcTimestamp(nanoseconds uint64) string {
	days, rem := nanoseconds/86400_000_000_000, nanoseconds%86400_000_000_000
	hour := rem / 3600_000_000_000
	minute := rem % 3600_000_000_000 / 60_000_000_000
	second := rem % 60_000_000_000 / 1_000_000_000
	year, month, day := civilFromDays(int64(days))
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d UTC", year, month, day, hour, minute, second)
}

func mboxTimestamp(nanoseconds uint64) string {
	const weekdayNames = "MonTueWedThuFriSatSun"
	const monthNames = "JanFebMarAprMayJunJulAugSepOctNovDec"
	days, rem := nanoseconds/86400_000_000_000, nanoseconds%86400_000_000_000
	hour := rem / 3600_000_000_000
	minute := rem % 3600_000_000_000 / 60_000_000_000
	second := rem % 60_000_000_000 / 1_000_000_000
	year, month, day := civilFromDays(int64(days))
	weekday := int(days % 7)
	return fmt.Sprintf("%.3s %.3s %2d %02d:%02d:%02d %04d",
		weekdayNames[((weekday+3)%7)*3:], monthNames[(month-1)*3:], day, hour, minute, second, year)
}

func civilFromDays(days int64) (int64, int, int) {
	z := days + 719468
	era := z / 146097
	if z < 0 && z%146097 != 0 {
		era--
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if mp >= 10 {
		m -= 12
		y++
	}
	return y, int(m), int(d)
}

type yieldClock struct {
	inner clock.Source
	k     *Kernel
}

func (y yieldClock) Wall(ctx context.Context) (int64, error) { return y.inner.Wall(ctx) }

func (y yieldClock) Monotonic(ctx context.Context) (uint64, error) {
	return y.inner.Monotonic(ctx)
}

func (y yieldClock) Sleep(ctx context.Context, nanoseconds uint64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now, err := y.inner.Monotonic(ctx)
		if err != nil {
			return err
		}
		target := now + nanoseconds
		if target < now {
			return errno.Overflow
		}
		next, err := y.nextJobDeadline(ctx, now)
		if err != nil {
			return err
		}
		if next != nil && *next < target {
			if err := y.inner.Sleep(ctx, *next-now); err != nil {
				return err
			}
			if err := y.k.RunDueJobs(ctx); err != nil {
				return err
			}
			elapsed, err := y.inner.Monotonic(ctx)
			if err != nil {
				return err
			}
			if elapsed >= target {
				return nil
			}
			nanoseconds = target - elapsed
			continue
		}
		if err := y.inner.Sleep(ctx, nanoseconds); err != nil {
			return err
		}
		return y.k.RunDueJobs(ctx)
	}
}

func (y yieldClock) nextJobDeadline(ctx context.Context, now uint64) (*uint64, error) {
	system, err := y.k.services.Filesystem()
	if err != nil {
		return nil, err
	}
	names, err := spoolNames(ctx, system, AtSpoolDir)
	if err != nil {
		return nil, err
	}
	var best *uint64
	for _, name := range names {
		job, err := readAtJob(ctx, system, AtSpoolDir, name)
		if err != nil || job.deadline <= now {
			continue
		}
		if best == nil || job.deadline < *best {
			value := job.deadline
			best = &value
		}
	}
	return best, nil
}
