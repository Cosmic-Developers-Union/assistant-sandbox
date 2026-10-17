package mailx

import (
	"testing"
)

func TestParseMailbox(t *testing.T) {
	text := "From alice@example.org Thu Jan  1 00:00:00 1970\n" +
		"From: alice\n" +
		"Subject: hello\n" +
		"\n" +
		"body one\n" +
		">From the void\n" +
		"From bob@example.org Fri Jan  2 00:00:00 1970\n" +
		"Subject: lunch\n" +
		"\n" +
		"body two\n"
	messages, skipped := parseMailbox(text)
	if skipped != 0 || len(messages) != 2 {
		t.Fatalf("分帧: %d 条, 跳过 %d", len(messages), skipped)
	}
	if messages[0].sender() != "alice@example.org" || messages[0].subject() != "hello" {
		t.Fatalf("第一封: %+v", messages[0])
	}

	if messages[0].body != "body one\n>From the void" {
		t.Fatalf("第一封体: %q", messages[0].body)
	}
	if messages[1].sender() != "bob@example.org" || messages[1].body != "body two\n" {
		t.Fatalf("第二封: %+v", messages[1])
	}

	round, skipped := parseMailbox(messages[0].mboxText() + messages[1].mboxText())
	if skipped != 0 || len(round) != 2 || round[1].subject() != "lunch" {
		t.Fatalf("重建: %d 条, 跳过 %d", len(round), skipped)
	}

	bad, skipped := parseMailbox("散行\nFrom x Thu Jan 1 00:00:00 1970\n没有头\n")
	if len(bad) != 0 || skipped != 2 {
		t.Fatalf("坏帧: %d 条, 跳过 %d", len(bad), skipped)
	}

	slim, skipped := parseMailbox("From x Thu Jan 1 00:00:00 1970\nSubject: s\n\n")
	if skipped != 0 || len(slim) != 1 || slim[0].body != "" {
		t.Fatalf("无体: %+v 跳过 %d", slim, skipped)
	}

	if got := slim[0].bodyText(); got != "\n" {
		t.Fatalf("bodyText: %q", got)
	}
}

func TestParseMailboxEdges(t *testing.T) {

	messages, skipped := parseMailbox("From \nFrom: x\n\nbody\n")
	if skipped != 0 || len(messages) != 1 || messages[0].sender() != "" {
		t.Fatalf("空发送者: %+v 跳过 %d", messages, skipped)
	}

	if messages[0].subject() != "" {
		t.Fatalf("无主题: %q", messages[0].subject())
	}

	flat, skipped := parseMailbox("From x Thu Jan 1 00:00:00 1970\nSubject: s\n")
	if skipped != 0 || len(flat) != 1 || flat[0].head != "Subject: s" || flat[0].body != "" {
		t.Fatalf("无空行帧: %+v 跳过 %d", flat, skipped)
	}

	empty, skipped := parseMailbox("From x Thu Jan 1 00:00:00 1970\n")
	if len(empty) != 0 || skipped != 1 {
		t.Fatalf("空帧: %+v 跳过 %d", empty, skipped)
	}

	messages, _ = parseMailbox("From x Thu Jan 1 00:00:00 1970\nFrom: x\n\nno newline")
	if got := messages[0].text(); got != "From: x\n\nno newline\n" {
		t.Fatalf("补齐换行: %q", got)
	}
}
