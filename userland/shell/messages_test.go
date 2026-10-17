package shell

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/internal/protocol"
)

func TestMessageInjectionRefreshAndHistory(t *testing.T) {
	message := "汉字'\n$(touch injected); `touch injected2`"
	records := []jsontext.Value{jsontext.Value(`{"role":"user","content":[{"type":"text","text":"任务"}]}`), jsontext.Value(`{"role":"assistant","content":[{"type":"text","text":"旧稿"}]}`), jsontext.Value(`{"role":"assistant","content":[{"type":"text","text":"新稿"}]}`)}
	requests := []protocol.Request{
		{ID: 1, Message: message, Messages: records, Command: `VALUE=kept; printf '%s' "$MESSAGE"; test ! -e injected; test ! -e injected2`, OutputLimit: 4096},
		{ID: 2, Message: "第二条", Messages: records, Command: `printf '%s' "$MESSAGE"; printf '%s' "$VALUE"; messages --last 1 | wc -m`, OutputLimit: 4096},
		{ID: 3, Messages: records, Command: `printf '<%s>' "$MESSAGE"; messages --role user; messages --role all --format jsonl`, OutputLimit: 4096},
		{ID: 4, Messages: records, Command: `messages --role assistant`, OutputLimit: 4096},
		{ID: 5, Command: `messages --role invalid`, OutputLimit: 4096},
		{ID: 6, Command: `messages --last -1`, OutputLimit: 4096},
		{ID: 7, Command: `messages --help`, OutputLimit: 4096},
		{ID: 8, Message: "保留", Command: `readonly MESSAGE`, OutputLimit: 4096},
		{ID: 9, Message: "刷新", Command: `printf stale`, OutputLimit: 4096},
	}
	var input, output bytes.Buffer
	for _, r := range requests {
		if err := json.MarshalWrite(&input, r); err != nil {
			t.Fatal(err)
		}
		input.WriteByte('\n')
	}
	if err := loop(t.Context(), &input, &output, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	var responses []protocol.Response
	for line := range strings.SplitSeq(strings.TrimSpace(output.String()), "\n") {
		var r protocol.Response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, r)
	}
	if len(responses) != 9 || responses[0].Code != 0 || responses[0].Stdout != message || responses[1].Stdout != "第二条kept2\n" {
		t.Fatal(responses)
	}
	if !strings.HasPrefix(responses[2].Stdout, "<>任务{") || !strings.Contains(responses[2].Stdout, `"role":"assistant"`) || responses[3].Stdout != "旧稿\n\n新稿" {
		t.Fatal(responses)
	}
	if responses[4].Code == 0 || responses[5].Code == 0 || responses[6].Code != 0 || responses[8].Code == 0 || !responses[8].Closed || strings.Contains(responses[8].Stdout, "stale") {
		t.Fatal(responses)
	}
}
func TestUnicodeCharacterCount(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), `printf '汉字😀a\n' | wc -m; printf '汉字😀a\n' | wc -c`, t.TempDir(), nil, strings.NewReader(""), &output, &output); err != nil || output.String() != "5\n12\n" {
		t.Fatal(err, output.String())
	}
}
