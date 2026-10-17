package sumcmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commandtest "github.com/Cosmic-Developers-Union/assistant-sandbox/internal/testutil"
)

func bsdReference(data []byte) (uint16, uint64) {
	var checksum uint16
	for _, value := range data {
		checksum = (checksum >> 1) + ((checksum & 1) << 15) + uint16(value)
	}
	return checksum, blockCount(len(data), 1024)
}

func sysvReference(data []byte) (uint16, uint64) {
	var checksum uint32
	for _, value := range data {
		checksum += uint32(value)
	}
	checksum = (checksum & 0xffff) + (checksum >> 16)
	checksum = (checksum & 0xffff) + (checksum >> 16)
	return uint16(checksum), blockCount(len(data), 512)
}

func blockCount(size, block int) uint64 {
	if size == 0 {
		return 0
	}
	return uint64((size + block - 1) / block)
}

func TestCommandContract(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello world\n")
	if err := os.WriteFile(filepath.Join(dir, "data"), data, 0600); err != nil {
		t.Fatal(err)
	}
	bsd, bsdBlocks := bsdReference(data)
	sysv, sysvBlocks := sysvReference(data)

	for _, test := range []struct {
		args  []string
		input string
		want  string
		code  int
	}{
		{[]string{"data"}, "", fmt.Sprintf("%05d %5d data\n", bsd, bsdBlocks), 0},
		{[]string{"-r", "data"}, "", fmt.Sprintf("%05d %5d data\n", bsd, bsdBlocks), 0},
		{[]string{"-s", "data"}, "", fmt.Sprintf("%d %d data\n", sysv, sysvBlocks), 0},
		{[]string{"--sysv", "data"}, "", fmt.Sprintf("%d %d data\n", sysv, sysvBlocks), 0},
		{[]string{"-rs", "data"}, "", fmt.Sprintf("%d %d data\n", sysv, sysvBlocks), 0},
		{[]string{"-sr", "data"}, "", fmt.Sprintf("%05d %5d data\n", bsd, bsdBlocks), 0},
		{[]string{"-s"}, string(data), fmt.Sprintf("%d %d\n", sysv, sysvBlocks), 0},
		{[]string{}, string(data), fmt.Sprintf("%05d %5d\n", bsd, bsdBlocks), 0},
		{[]string{"-"}, string(data), fmt.Sprintf("%05d %5d -\n", bsd, bsdBlocks), 0},
		{[]string{"--", "data"}, "", fmt.Sprintf("%05d %5d data\n", bsd, bsdBlocks), 0},
		{[]string{"-rs", "--", "data"}, "", fmt.Sprintf("%d %d data\n", sysv, sysvBlocks), 0},
		{[]string{"-rq", "data"}, "", "", 2},
		{[]string{"absent"}, "", "", 1},
		{[]string{"--bad"}, "", "", 2},
	} {
		result := commandtest.Run(t, Run, dir, test.args, test.input)
		if result.Code != test.code || result.Stdout != test.want {
			t.Fatalf("args=%v 得到 %+v，want code=%d stdout=%q", test.args, result, test.code, test.want)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	result := commandtest.Run(t, Run, dir, []string{"folder"}, "")
	if result.Code != 1 || !strings.Contains(result.Stderr, "Is a directory") {
		t.Fatalf("目录输入得到 %+v", result)
	}
	result = commandtest.Run(t, Run, dir, []string{"--help"}, "")
	if result.Code != 0 || !strings.Contains(result.Stdout, "Usage") {
		t.Fatalf("--help 得到 %+v", result)
	}
}
