package scheduler

import (
	"testing"
)

func TestSequencerAssignsMonotonicOrder(t *testing.T) {
	s := New(16)
	seq := []uint64{
		s.Append(1, KindSpawn, "init"),
		s.Append(2, KindSpawn, "worker"),
		s.Append(2, KindExit, "code=0"),
		s.Append(1, KindReap, "2"),
	}
	for i, id := range seq {
		if id != uint64(i+1) {
			t.Fatalf("seq[%d]=%d", i, id)
		}
	}
	log := s.Log()
	if len(log) != 4 || log[0].Kind != KindSpawn || log[3].Kind != KindReap {
		t.Fatalf("log=%v", log)
	}
	if s.Full() {
		t.Fatal("未达容量不应报告截断")
	}
}

func TestSequencerStopsAtCapacity(t *testing.T) {
	s := New(2)
	if s.Append(1, KindSpawn, "a") == 0 {
		t.Fatal("首条事件不应被丢弃")
	}
	if s.Append(1, KindSpawn, "b") == 0 {
		t.Fatal("第二条事件不应被丢弃")
	}
	if s.Append(1, KindClose, "") != 0 {
		t.Fatal("超容量必须显式失败而不是截断")
	}
	if !s.Full() {
		t.Fatal("应报告已满")
	}
}

func TestIdenticalRequiresExactMatch(t *testing.T) {
	a := []Event{
		{Seq: 1, PID: 1, Kind: KindSpawn, Detail: "init"},
		{Seq: 2, PID: 2, Kind: KindExit, Detail: "code=0"},
	}
	b := []Event{
		{Seq: 1, PID: 1, Kind: KindSpawn, Detail: "init"},
		{Seq: 2, PID: 2, Kind: KindExit, Detail: "code=0"},
	}
	if !Identical(a, b) {
		t.Fatal("相同序列应判定一致")
	}
	c := []Event{{Seq: 1, PID: 1, Kind: KindSpawn, Detail: "init"}}
	if Identical(a, c) {
		t.Fatal("长度不同不应一致")
	}
	d := []Event{
		{Seq: 1, PID: 1, Kind: KindSpawn, Detail: "init"},
		{Seq: 2, PID: 2, Kind: KindExit, Detail: "code=7"},
	}
	if Identical(a, d) {
		t.Fatal("详情不同不应一致")
	}
	e := []Event{
		{Seq: 1, PID: 1, Kind: KindSpawn, Detail: "init"},
		{Seq: 2, PID: 3, Kind: KindExit, Detail: "code=0"},
	}
	if Identical(a, e) {
		t.Fatal("PID 不同不应一致")
	}
}
