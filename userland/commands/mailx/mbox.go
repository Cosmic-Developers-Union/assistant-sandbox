package mailx

import "strings"

type message struct {
	fromLine string

	head string
	body string

	deleted bool
}

func (m *message) sender() string {
	fields := strings.Fields(m.fromLine)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func (m *message) subject() string {
	for _, line := range strings.Split(m.head, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(name, "subject") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m *message) text() string {
	var b strings.Builder
	if m.head != "" {
		b.WriteString(m.head)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.body)
	if !strings.HasSuffix(m.body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func (m *message) mboxText() string {
	return "From " + m.fromLine + "\n" + m.text()
}

func (m *message) bodyText() string {
	if strings.HasSuffix(m.body, "\n") {
		return m.body
	}
	return m.body + "\n"
}

func parseMailbox(text string) ([]*message, int) {
	var msgs []*message
	skipped := 0
	orphan := false
	var chunk []string
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		msg, ok := parseChunk(chunk)
		if !ok {
			skipped++
		} else {
			msgs = append(msgs, msg)
		}
		chunk = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "From ") {
			flush()
			chunk = append(chunk, line)
			continue
		}
		if len(chunk) > 0 {
			chunk = append(chunk, line)
		} else if line != "" && !orphan {

			skipped++
			orphan = true
		}
	}
	flush()
	return msgs, skipped
}

func parseChunk(lines []string) (*message, bool) {
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "From ") {
		return nil, false
	}
	msg := &message{fromLine: strings.TrimPrefix(lines[0], "From ")}
	rest := lines[1:]
	split := len(rest)
	for i, line := range rest {
		if line == "" {
			split = i
			break
		}
	}
	if split > len(rest) {
		split = len(rest)
	}
	msg.head = strings.Join(rest[:split], "\n")
	if split < len(rest) {
		msg.body = strings.Join(rest[split+1:], "\n")
	}

	if !hasHeader(msg.head) {
		return nil, false
	}
	return msg, true
}

func hasHeader(head string) bool {
	if head == "" {
		return false
	}
	for _, line := range strings.Split(head, "\n") {
		if _, _, ok := strings.Cut(line, ":"); ok {
			return true
		}
	}
	return false
}
