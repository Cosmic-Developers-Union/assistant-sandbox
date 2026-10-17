package clock

import (
	"errors"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/capability"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/clock"
	"github.com/Cosmic-Developers-Union/assistant-sandbox/vm/proc"
)

var instants = []int64{
	0,
	951782400 * 1e9,
	1114905600 * 1e9,
	1600000000 * 1e9,
	1709164800 * 1e9,
	1735689600*1e9 + 123456789,
	-2208988800 * 1e9,
	4102444800 * 1e9,
}

func TestCivilFieldsMatchStandardLibrary(t *testing.T) {
	for _, nanos := range instants {
		want := time.Unix(0, nanos).UTC()
		got := Unix(0, nanos)
		if got.Year() != want.Year() || got.Month() != Month(want.Month()) || got.Day() != want.Day() {
			t.Errorf("Unix(0,%d) 日期 = %d-%02d-%02d, want %v", nanos, got.Year(), got.Month(), got.Day(), want.Format(DateOnly))
		}
		if got.Hour() != want.Hour() || got.Minute() != want.Minute() || got.Second() != want.Second() || got.Nanosecond() != want.Nanosecond() {
			t.Errorf("Unix(0,%d) 时间 = %02d:%02d:%02d.%09d, want %v", nanos, got.Hour(), got.Minute(), got.Second(), got.Nanosecond(), want.Format(time.RFC3339Nano))
		}
		if int(got.Weekday()) != int(want.Weekday()) {
			t.Errorf("Unix(0,%d) 星期 = %v, want %v", nanos, got.Weekday(), want.Weekday())
		}
		if got.YearDay() != want.YearDay() {
			t.Errorf("Unix(0,%d) YearDay = %d, want %d", nanos, got.YearDay(), want.YearDay())
		}
		wantYear, wantWeek := want.ISOWeek()
		gotYear, gotWeek := got.ISOWeek()
		if gotYear != wantYear || gotWeek != wantWeek {
			t.Errorf("Unix(0,%d) ISOWeek = (%d,%d), want (%d,%d)", nanos, gotYear, gotWeek, wantYear, wantWeek)
		}
		if got.Unix() != want.Unix() || got.UnixNano() != want.UnixNano() {
			t.Errorf("Unix(0,%d) 时间戳 = (%d,%d), want (%d,%d)", nanos, got.Unix(), got.UnixNano(), want.Unix(), want.UnixNano())
		}
	}
}

func TestFormatMatchesStandardLibrary(t *testing.T) {
	layouts := []string{
		ANSIC, UnixDate, RubyDate, RFC822, RFC822Z, RFC850, RFC1123, RFC1123Z,
		RFC3339, RFC3339Nano, Kitchen, Stamp, StampMilli, DateTime, DateOnly,
		TimeOnly, "2006-01-02 15:04:05.000000000 -0700", "2006-01-02T15:04:05",
		"01/02/06", "Mon Jan _2 15:04:05 2006", "03:04:05 PM", "02.01.2006",
		"002 2006", "15:04:05 MST", "2006-01-02 -0700", "2006-01-02 Z07:00",
		"January 2, 2006", "1/2/2006", "3:04PM",
	}
	for _, nanos := range instants {
		want := time.Unix(0, nanos).UTC()
		got := Unix(0, nanos)
		for _, layout := range layouts {
			if got.Format(layout) != want.Format(layout) {
				t.Errorf("Unix(0,%d).Format(%q) = %q, want %q", nanos, layout, got.Format(layout), want.Format(layout))
			}
		}
	}
}

func TestFormatFractionTrimming(t *testing.T) {
	for _, tc := range []struct {
		nanos int64
		want  string
	}{
		{0, "1970-01-01T00:00:00Z"},
		{500000000, "1970-01-01T00:00:00.5Z"},
		{123456789, "1970-01-01T00:00:00.123456789Z"},
		{120000000, "1970-01-01T00:00:00.12Z"},
	} {
		got := Unix(0, tc.nanos).Format(RFC3339Nano)
		if got != tc.want {
			t.Errorf("Unix(0,%d).Format(RFC3339Nano) = %q, want %q", tc.nanos, got, tc.want)
		}
	}
}

func TestParseMatchesStandardLibrary(t *testing.T) {
	layouts := []string{
		RFC3339, RFC3339Nano, DateTime, DateOnly, TimeOnly, ANSIC, RFC1123Z,
		"2006-01-02T15:04:05", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04",
		"2006-01-02T15:04:05Z07:00", "02 Jan 06 15:04 -0700", "01/02/06",
	}
	values := []string{
		"2025-01-01T00:00:00Z",
		"2025-01-01T00:00:00.123456789Z",
		"2025-01-01 12:34:56",
		"2025-01-01",
		"12:34:56",
		"Mon Jan  2 15:04:05 2006",
		"Mon, 02 Jan 2006 15:04:05 +0800",
		"2025-01-01T00:00:00",
		"2025-01-01 12:34:56.9",
		"2025-01-01 12:34",
		"1995-01-02 03:04:05+00:00",
		"02 Jan 95 15:04 -0700",
		"01/02/06",
	}
	for _, layout := range layouts {
		for _, value := range values {
			want, wantErr := time.Parse(layout, value)
			got, gotErr := Parse(layout, value)
			if (wantErr != nil) != (gotErr != nil) {
				t.Errorf("Parse(%q, %q) 错误不一致: got %v, want %v", layout, value, gotErr, wantErr)
				continue
			}
			if wantErr != nil {
				continue
			}
			if got.Format(DateTime) != want.Format(DateTime) || got.Nanosecond() != want.Nanosecond() {
				t.Errorf("Parse(%q, %q) = %v, want %v", layout, value, got.Format(DateTime), want.Format(DateTime))
			}
		}
	}
}

func TestParseInLocationUsesGivenZone(t *testing.T) {
	zone := FixedZone("+08:00", 8*3600)
	got, err := ParseInLocation("2006-01-02 15:04:05", "2025-01-01 00:00:00", zone)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2025, 1, 1, 0, 0, 0, 0, time.FixedZone("+08:00", 8*3600))
	if got.Unix() != want.Unix() {
		t.Fatalf("ParseInLocation = %d, want %d", got.Unix(), want.Unix())
	}
	if got.Location().String() != "+08:00" || got.Format("-0700") != "+0800" {
		t.Fatalf("时区信息 = (%q, %q)", got.Location(), got.Format("-0700"))
	}

	got, err = ParseInLocation("2006-01-02T15:04:05Z07:00", "2025-01-01T00:00:00-05:00", zone)
	if err != nil {
		t.Fatal(err)
	}
	want = time.Date(2025, 1, 1, 0, 0, 0, 0, time.FixedZone("-05:00", -5*3600))
	if got.Unix() != want.Unix() {
		t.Fatalf("布局偏移被忽略: %d, want %d", got.Unix(), want.Unix())
	}
}

func TestParseRejectsMismatch(t *testing.T) {
	for _, tc := range []struct{ layout, value string }{
		{DateOnly, "2025-01-0x"},
		{DateOnly, "2025-01-01extra"},
		{DateOnly, "2025/01/01"},
		{TimeOnly, "12:34"},
		{RFC3339, "2025-01-01 00:00:00Z"},
	} {
		if _, err := Parse(tc.layout, tc.value); err == nil {
			t.Errorf("Parse(%q, %q) 应失败", tc.layout, tc.value)
		}
	}
}

func TestLoadLocationRejectsMissingDatabase(t *testing.T) {
	for _, name := range []string{"", "UTC", "Etc/UTC", "Local"} {
		if _, err := LoadLocation(name); err != nil {
			t.Errorf("LoadLocation(%q) = %v", name, err)
		}
	}
	for name, offset := range map[string]int{"+08:00": 8 * 3600, "UTC-05:00": -5 * 3600, "GMT+0130": 5400, "-07": -7 * 3600} {
		loc, err := LoadLocation(name)
		if err != nil {
			t.Fatalf("LoadLocation(%q) = %v", name, err)
		}
		if loc.offset != offset {
			t.Errorf("LoadLocation(%q) 偏移 = %d, want %d", name, loc.offset, offset)
		}
	}
	for _, name := range []string{"Asia/Shanghai", "America/New_York", "Europe/Berlin"} {
		if _, err := LoadLocation(name); !errors.Is(err, ErrNoTimeZoneData) {
			t.Errorf("LoadLocation(%q) 应报缺少时区数据库, 得到 %v", name, err)
		}
	}
}

func TestArithmeticMatchesStandardLibrary(t *testing.T) {
	base := Unix(1709164800, 0)
	wantBase := time.Unix(1709164800, 0).UTC()
	if !base.Add(90*Minute + 30*Second).Equal(Unix(0, wantBase.Add(90*time.Minute+30*time.Second).UnixNano())) {
		t.Error("Add 与标准库不一致")
	}
	if got, want := base.AddDate(1, -3, 10), wantBase.AddDate(1, -3, 10); got.Format(DateTime) != want.Format(DateTime) {
		t.Errorf("AddDate = %s, want %s", got.Format(DateTime), want.Format(DateTime))
	}
	if got, want := base.Sub(Unix(1709164700, 0)), wantBase.Sub(time.Unix(1709164700, 0)); got != Duration(want) {
		t.Errorf("Sub = %v, want %v", got, want)
	}
	if !base.After(Unix(1709164700, 0)) || !base.Before(Unix(1709164900, 0)) || base.Compare(base) != 0 {
		t.Error("比较运算不一致")
	}
	zero := Time{}
	if !zero.IsZero() || !zero.UTC().IsZero() {
		t.Error("零值语义与 time.Time 不一致")
	}
	if got, want := base.In(FixedZone("+08:00", 8*3600)).Format(DateTime), wantBase.In(time.FixedZone("+08:00", 8*3600)).Format(DateTime); got != want {
		t.Errorf("In = %s, want %s", got, want)
	}
}

func TestDurationFormattingMatchesStandardLibrary(t *testing.T) {
	for _, d := range []time.Duration{
		0, time.Nanosecond, 1500 * time.Millisecond, 90 * time.Second,
		3 * time.Hour, 2*time.Hour + 3*time.Minute + 4*time.Second + 5*time.Millisecond,
		-time.Second, time.Minute,
	} {
		if got, want := Duration(d).String(), d.String(); got != want {
			t.Errorf("Duration(%d).String() = %q, want %q", int64(d), got, want)
		}
	}
	if Second != Duration(time.Second) || Hour != Duration(time.Hour) || Millisecond != Duration(time.Millisecond) {
		t.Error("时长常量与 time 不一致")
	}
}

func TestNowSleepAndSinceUseGuestClock(t *testing.T) {
	virtual := &clock.Virtual{}
	ctx := proc.WithContext(t.Context(), proc.Proc{ID: 1, Resources: capability.Set{Clock: virtual}})
	start, err := Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sleep(ctx, 2*Second); err != nil {
		t.Fatal(err)
	}
	elapsed, err := Since(ctx, start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed != 2*Second {
		t.Fatalf("Since = %v, want 2s", elapsed)
	}
	if err := Sleep(ctx, 0); err != nil {
		t.Fatalf("零时长睡眠应直接成功: %v", err)
	}
}

func TestClockCapabilityMissingFailsLoudly(t *testing.T) {
	ctx := proc.WithContext(t.Context(), proc.Proc{ID: 1})
	if _, err := Now(ctx); err == nil {
		t.Fatal("没有时钟能力时应失败")
	}
	if err := Sleep(ctx, Second); err == nil {
		t.Fatal("没有时钟能力时 Sleep 应失败")
	}
	if _, err := Now(t.Context()); err == nil {
		t.Fatal("没有进程上下文时应失败")
	}
}

func TestDurationAccessorsMatchStandardLibrary(t *testing.T) {
	for _, d := range []time.Duration{0, time.Nanosecond, 1500 * time.Microsecond, 3 * time.Second, -250 * time.Millisecond, 90 * time.Minute} {
		got := Duration(d)
		if got.Nanoseconds() != d.Nanoseconds() || got.Microseconds() != d.Microseconds() || got.Milliseconds() != d.Milliseconds() {
			t.Errorf("Duration(%d) 整数单位不一致", int64(d))
		}
		if got.Seconds() != d.Seconds() || got.Minutes() != d.Minutes() || got.Hours() != d.Hours() {
			t.Errorf("Duration(%d) 浮点单位不一致", int64(d))
		}
	}
}

func TestTimeStringAndAccessors(t *testing.T) {
	for _, nanos := range []int64{0, 1735689600123456789, -2208988800 * 1e9} {
		want := time.Unix(0, nanos).UTC()
		got := Unix(0, nanos)
		if got.String() != want.String() {
			t.Errorf("Unix(0,%d).String() = %q, want %q", nanos, got.String(), want.String())
		}
		if got.UnixMilli() != want.UnixMilli() {
			t.Errorf("Unix(0,%d).UnixMilli() = %d, want %d", nanos, got.UnixMilli(), want.UnixMilli())
		}
		if got.Local().Location().String() != "UTC" {
			t.Errorf("Local() = %v, want UTC", got.Local().Location())
		}
		if got.Location() != UTC {
			t.Errorf("Location() = %v, want UTC", got.Location())
		}
	}

	if Month(0).String() != time.Month(0).String() || Month(13).String() != time.Month(13).String() {
		t.Errorf("Month.String 越界文本 = %q, want %q", Month(13).String(), time.Month(13).String())
	}
	if Weekday(7).String() != time.Weekday(7).String() {
		t.Errorf("Weekday.String 越界文本 = %q, want %q", Weekday(7).String(), time.Weekday(7).String())
	}

	var missing *Location
	if Unix(0, 0).In(missing).Location() != UTC {
		t.Error("nil Location 应按 UTC 处理")
	}
	if missing.String() != "UTC" {
		t.Error("nil Location 的名字应是 UTC")
	}
}

func TestAddDateRollsAcrossYearBoundary(t *testing.T) {
	base := Date(2025, January, 31, 0, 0, 0, 0, UTC)
	want := time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		years, months, days int
	}{
		{0, 13, 0}, {0, -13, 0}, {1, 0, 0}, {0, 0, 365}, {-2, 5, 3},
	} {
		got := base.AddDate(tc.years, tc.months, tc.days)
		if wantValue := want.AddDate(tc.years, tc.months, tc.days); got.Format(DateTime) != wantValue.Format(DateTime) {
			t.Errorf("AddDate(%d,%d,%d) = %s, want %s", tc.years, tc.months, tc.days, got.Format(DateTime), wantValue.Format(DateTime))
		}
	}
	if Unix(0, 0).Compare(Unix(0, 1)) != -1 || Unix(0, 1).Compare(Unix(0, 0)) != 1 {
		t.Error("Compare 不一致")
	}
}

func TestFormatTimeZoneVariants(t *testing.T) {
	zone := FixedZone("-05:00", -5*3600)
	got := Unix(0, 0).In(zone)
	want := time.Unix(0, 0).In(time.FixedZone("-05:00", -5*3600))
	for _, layout := range []string{"-0700", "-07:00", "Z0700", "Z07:00", "MST"} {
		if got.Format(layout) != want.Format(layout) {
			t.Errorf("Format(%q) = %q, want %q", layout, got.Format(layout), want.Format(layout))
		}
	}
	positive := FixedZone("+08:00", 8*3600)
	positiveWant := time.FixedZone("+08:00", 8*3600)
	if positive.String() != positiveWant.String() {
		t.Errorf("FixedZone 名字 = %q, want %q", positive.String(), positiveWant.String())
	}

	if Unix(0, 0).Format("Z07:00") != "Z" || Unix(0, 0).Format("-07:00") != "+00:00" {
		t.Errorf("零偏移渲染 = %q / %q", Unix(0, 0).Format("Z07:00"), Unix(0, 0).Format("-07:00"))
	}
}

func TestParseChunkRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct{ layout, value string }{
		{"January", "Xxx"},
		{"Monday", "Xxx"},
		{"PM", "XX"},
		{"MST", "XYZ"},
		{"Z0700", "X"},
		{"-0700", "+08"},
		{"-0700", "X0800"},
		{"15", "ab"},
		{"_2", "  "},
		{"January", "Ja"},
	} {
		if _, err := Parse(tc.layout, tc.value); err == nil {
			t.Errorf("Parse(%q, %q) 应失败", tc.layout, tc.value)
		}
	}

	got, err := Parse("2006-01-02 15:04:05,999", "2025-01-01 00:00:00,5")
	if err != nil {
		t.Fatal(err)
	}
	want, err := time.Parse("2006-01-02 15:04:05,999", "2025-01-01 00:00:00,5")
	if err != nil {
		t.Fatal(err)
	}
	if got.Nanosecond() != want.Nanosecond() {
		t.Fatalf("逗号亚秒 = %d, want %d", got.Nanosecond(), want.Nanosecond())
	}
}
