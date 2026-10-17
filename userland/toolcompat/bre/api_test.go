package bre

import (
	"testing"
)

func TestPublicAPI(t *testing.T) {
	re, err := Compile("a+b")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("a+b") {
		t.Fatal("BRE 里 + 是字面量")
	}
	if matched, err := re.MatchStringErr("xx"); err != nil || matched {
		t.Fatalf("MatchStringErr 得到 %v err=%v", matched, err)
	}
	if location := re.FindStringIndex("za+bz"); location == nil || location[0] != 1 {
		t.Fatalf("FindStringIndex 得到 %v", location)
	}
	if sub := re.FindStringSubmatchIndex("a+b"); sub == nil {
		t.Fatal("FindStringSubmatchIndex 不应为空")
	}
	if all := re.FindAllStringSubmatchIndex("a+b a+b", -1); len(all) != 2 {
		t.Fatalf("FindAllStringSubmatchIndex 得到 %v", all)
	}
	if all, err := re.FindAllStringSubmatchIndexErr("a+b a+b", -1); err != nil || len(all) != 2 {
		t.Fatalf("FindAllStringSubmatchIndexErr 得到 %v err=%v", all, err)
	}
	if expanded := re.ExpandString(nil, "x", "a+b", re.FindStringSubmatchIndex("a+b")); string(expanded) != "x" {
		t.Fatalf("ExpandString 得到 %q", expanded)
	}
	if expanded := re.Expand(nil, []byte("y"), []byte("a+b"), re.FindStringSubmatchIndex("a+b")); string(expanded) != "y" {
		t.Fatalf("Expand 得到 %q", expanded)
	}
}

func TestCompileVariants(t *testing.T) {
	if _, err := CompileWithFlags("a+", ""); err != nil {
		t.Fatalf("CompileWithFlags: %v", err)
	}
	if _, err := CompileEREWithFlags("a+", ""); err != nil {
		t.Fatalf("CompileEREWithFlags: %v", err)
	}
	if err := ValidateERE("a+"); err != nil {
		t.Fatalf("ValidateERE: %v", err)
	}
	if err := ValidateERE("a+"); err != nil {
		t.Fatalf("合法 ERE 不应报错: %v", err)
	}
	re, err := CompileCUTF8WithFlags("a+", "", true, nil)
	if err != nil {
		t.Fatalf("CompileCUTF8WithFlags: %v", err)
	}
	if !re.MatchString("aaa") {
		t.Fatal("扩展模式下 a+ 应匹配")
	}
	if !ERERequiresBacktracking("a+") && !ERERequiresBacktracking("(a)") {
		t.Log("回溯判定按实现返回")
	}
	if got, err := ToGo("a+"); err != nil || got == "" {
		t.Fatalf("ToGo 得到 %q err=%v", got, err)
	}
	if got, err := ToGoERE("a+"); err != nil || got == "" {
		t.Fatalf("ToGoERE 得到 %q err=%v", got, err)
	}
	if got := SedEscapes("a\\/b"); got == "" {
		t.Fatal("SedEscapes 不应为空")
	}
}

func TestTablesAndErrors(t *testing.T) {
	if ErrMatchLimit.Error() == "" {
		t.Fatal("ErrMatchLimit 应有文案")
	}
	limit := &MatchLimitError{Limit: 7}
	if limit.Error() == "" || !limit.Is(ErrMatchLimit) {
		t.Fatalf("MatchLimitError 语义错误: %q", limit.Error())
	}
	tables, err := SnapshotLocaleByteCtypeTables(nil)
	if err != nil || tables == nil {
		t.Fatalf("空提供者应得到平凡表: %v %v", tables, err)
	}
	if _, err := SnapshotLocaleByteTables(nil); err == nil {
		t.Fatal("nil CType 提供者应报错")
	}
	if updated, err := tables.WithCollation(nil); err != nil || updated == nil {
		t.Fatalf("WithCollation: %v %v", updated, err)
	}
	byteRe, err := CompileLocaleByteRegexpTables([]byte("a"), tables, ByteRegexpOptions{})
	if err != nil {
		t.Fatalf("CompileLocaleByteRegexpTables: %v", err)
	}
	if matched, err := byteRe.MatchString("a"); err != nil || !matched {
		t.Fatalf("字节正则匹配得到 %v err=%v", matched, err)
	}
	if sub, err := byteRe.FindSubmatchIndex([]byte("a")); err != nil || sub == nil {
		t.Fatalf("FindSubmatchIndex 得到 %v err=%v", sub, err)
	}
	if all, err := byteRe.FindAllSubmatchIndex([]byte("aa"), -1); err != nil || len(all) != 2 {
		t.Fatalf("FindAllSubmatchIndex 得到 %v err=%v", all, err)
	}
	if all, err := byteRe.FindAllStringSubmatchIndex("aa", -1); err != nil || len(all) != 2 {
		t.Fatalf("FindAllStringSubmatchIndex 得到 %v err=%v", all, err)
	}
	if sub, err := byteRe.FindSubmatchIndex([]byte("a")); err == nil {
		if expanded, err := byteRe.ExpandString(nil, "z", "a", sub); err != nil || string(expanded) != "z" {
			t.Fatalf("ExpandString 得到 %q err=%v", expanded, err)
		}
		if expanded, err := byteRe.Expand(nil, []byte("z"), []byte("a"), sub); err != nil || string(expanded) != "z" {
			t.Fatalf("Expand 得到 %q err=%v", expanded, err)
		}
	}
	if _, err := CompileLocaleByteRegexp([]byte("a"), nil, ByteRegexpOptions{}); err == nil {
		t.Fatal("nil CType 提供者应报错")
	}
}
