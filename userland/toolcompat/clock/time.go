package clock

import (
	"fmt"
	"strconv"
	"strings"
)

type Duration int64

const (
	Nanosecond  Duration = 1
	Microsecond          = 1000 * Nanosecond
	Millisecond          = 1000 * Microsecond
	Second               = 1000 * Millisecond
	Minute               = 60 * Second
	Hour                 = 60 * Minute
)

func (d Duration) Nanoseconds() int64  { return int64(d) }
func (d Duration) Microseconds() int64 { return int64(d) / 1000 }
func (d Duration) Milliseconds() int64 { return int64(d) / 1_000_000 }
func (d Duration) Seconds() float64    { return float64(d) / 1e9 }
func (d Duration) Minutes() float64    { return float64(d) / 6e10 }
func (d Duration) Hours() float64      { return float64(d) / 3.6e12 }

func (d Duration) String() string {
	var buf [32]byte
	w := len(buf)
	u := uint64(d)
	negative := d < 0
	if negative {
		u = -u
	}
	if u < uint64(Second) {

		var precision int
		w--
		buf[w] = 's'
		w--
		switch {
		case u == 0:
			return "0s"
		case u < uint64(Microsecond):
			precision = 0
			buf[w] = 'n'
		case u < uint64(Millisecond):
			precision = 3

			w--
			copy(buf[w:], "µ")
		default:
			precision = 6
			buf[w] = 'm'
		}
		w, u = formatFraction(buf[:w], u, precision)
		w = formatInt(buf[:w], u)
	} else {
		w--
		buf[w] = 's'
		w, u = formatFraction(buf[:w], u, 9)
		w = formatInt(buf[:w], u%60)
		u /= 60
		if u > 0 {
			w--
			buf[w] = 'm'
			w = formatInt(buf[:w], u%60)
			u /= 60
			if u > 0 {
				w--
				buf[w] = 'h'
				w = formatInt(buf[:w], u)
			}
		}
	}
	if negative {
		w--
		buf[w] = '-'
	}
	return string(buf[w:])
}

func formatFraction(buf []byte, v uint64, precision int) (int, uint64) {
	w := len(buf)
	print := false
	for i := 0; i < precision; i++ {
		digit := v % 10
		print = print || digit != 0
		if print {
			w--
			buf[w] = byte(digit) + '0'
		}
		v /= 10
	}
	if print {
		w--
		buf[w] = '.'
	}
	return w, v
}

func formatInt(buf []byte, v uint64) int {
	w := len(buf)
	if v == 0 {
		w--
		buf[w] = '0'
		return w
	}
	for v > 0 {
		w--
		buf[w] = byte(v%10) + '0'
		v /= 10
	}
	return w
}

type Month int

const (
	January Month = 1 + iota
	February
	March
	April
	May
	June
	July
	August
	September
	October
	November
	December
)

var monthNames = [...]string{
	"January", "February", "March", "April", "May", "June",
	"July", "August", "September", "October", "November", "December",
}

func (m Month) String() string {
	if m < January || m > December {
		return fmt.Sprintf("%%!Month(%d)", int(m))
	}
	return monthNames[m-1]
}

type Weekday int

const (
	Sunday Weekday = iota
	Monday
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
)

var weekdayNames = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

func (d Weekday) String() string {
	if d < Sunday || d > Saturday {
		return fmt.Sprintf("%%!Weekday(%d)", int(d))
	}
	return weekdayNames[d]
}

type Location struct {
	name   string
	offset int
}

var (
	UTC   = &Location{name: "UTC"}
	Local = UTC
)

func (l *Location) String() string {
	if l == nil {
		return "UTC"
	}
	return l.name
}

func FixedZone(name string, offsetSeconds int) *Location {
	return &Location{name: name, offset: offsetSeconds}
}

func LoadLocation(name string) (*Location, error) {
	switch name {
	case "", "UTC", "Etc/UTC", "Universal", "Z":
		return UTC, nil
	case "Local":
		return Local, nil
	}
	if offset, ok := parseZoneOffset(name); ok {
		return FixedZone(name, offset), nil
	}
	return nil, fmt.Errorf("%w: %q", ErrNoTimeZoneData, name)
}

func parseZoneOffset(name string) (int, bool) {
	body := strings.TrimPrefix(strings.TrimPrefix(name, "UTC"), "GMT")
	if body == "" || (body[0] != '+' && body[0] != '-') {
		return 0, false
	}
	sign := 1
	if body[0] == '-' {
		sign = -1
	}
	body = body[1:]
	hours, rest, hasColon := strings.Cut(body, ":")
	if !hasColon {
		if len(body) == 4 {
			hours, rest = body[:2], body[2:]
		} else {
			rest = "00"
		}
	}
	hourValue, err := strconv.Atoi(hours)
	if err != nil {
		return 0, false
	}
	minuteValue := 0
	if rest != "" {
		minuteValue, err = strconv.Atoi(rest)
		if err != nil {
			return 0, false
		}
	}
	return sign * (hourValue*3600 + minuteValue*60), true
}

type Time struct {
	sec  int64
	nsec int32
	loc  *Location
}

func Unix(sec, nsec int64) Time {
	return Time{sec: sec + floorDiv(nsec, 1e9), nsec: int32(floorMod(nsec, 1e9)), loc: UTC}
}

func timeFromUnixNano(nanoseconds int64) Time {
	return Unix(floorDiv(nanoseconds, 1e9), floorMod(nanoseconds, 1e9))
}

func date(year int, month Month, day, hour, min, sec, nsec int, loc *Location) Time {
	if loc == nil {
		loc = UTC
	}
	days := daysFromCivil(year, int(month), day)
	seconds := days*86400 + int64(hour)*3600 + int64(min)*60 + int64(sec) - int64(loc.offset)
	return Unix(seconds, int64(nsec)).In(loc)
}

func Date(year int, month Month, day, hour, min, sec, nsec int, loc *Location) Time {
	return date(year, month, day, hour, min, sec, nsec, loc)
}

func (t Time) UnixNano() int64 { return t.sec*1e9 + int64(t.nsec) }

func (t Time) Unix() int64 { return t.sec }

func (t Time) UnixMilli() int64 { return t.sec*1000 + int64(t.nsec)/1e6 }

func (t Time) IsZero() bool { return t.sec == 0 && t.nsec == 0 }

func (t Time) Location() *Location {
	if t.loc == nil {
		return UTC
	}
	return t.loc
}

func (t Time) In(loc *Location) Time {
	if loc == nil {
		loc = UTC
	}
	t.loc = loc
	return t
}

func (t Time) UTC() Time { return t.In(UTC) }

func (t Time) Local() Time { return t.In(Local) }

func (t Time) localSeconds() int64 { return t.sec + int64(t.Location().offset) }

func (t Time) civil() (year, month, day, hour, min, sec, nsec int) {
	seconds := t.localSeconds()
	days := floorDiv(seconds, 86400)
	remainder := floorMod(seconds, 86400)
	year, month, day = civilFromDays(days)
	hour = int(remainder / 3600)
	min = int(remainder % 3600 / 60)
	sec = int(remainder % 60)
	return year, month, day, hour, min, sec, int(t.nsec)
}

func (t Time) Year() int    { year, _, _, _, _, _, _ := t.civil(); return year }
func (t Time) Month() Month { _, month, _, _, _, _, _ := t.civil(); return Month(month) }
func (t Time) Day() int     { _, _, day, _, _, _, _ := t.civil(); return day }
func (t Time) Hour() int    { _, _, _, hour, _, _, _ := t.civil(); return hour }
func (t Time) Minute() int  { _, _, _, _, min, _, _ := t.civil(); return min }
func (t Time) Second() int  { _, _, _, _, _, sec, _ := t.civil(); return sec }
func (t Time) Nanosecond() int {
	return int(t.nsec)
}

func (t Time) Weekday() Weekday {
	days := floorDiv(t.localSeconds(), 86400)
	return Weekday(floorMod(days+4, 7))
}

func (t Time) YearDay() int {
	year, _, day, _, _, _, _ := t.civil()
	return daysInMonthsBefore(year, t.Month()) + day
}

func daysInMonthsBefore(year int, month Month) int {
	total := 0
	for m := January; m < month; m++ {
		total += daysInMonth(year, m)
	}
	return total
}

func (t Time) ISOWeek() (year, week int) {
	year, _, _, _, _, _, _ = t.civil()
	yearDay := t.YearDay()
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	week = (yearDay - weekday + 10) / 7
	switch {
	case week < 1:
		year--
		week = weeksInYear(year)
	case week > weeksInYear(year):
		year++
		week = 1
	}
	return year, week
}

func weeksInYear(year int) int {
	first := Weekday(floorMod(daysFromCivil(year, 1, 1)+4, 7))
	if first == Thursday || (first == Wednesday && isLeap(year)) {
		return 53
	}
	return 52
}

func (t Time) Add(d Duration) Time {
	return Time{sec: t.sec + int64(d)/1e9, nsec: t.nsec + int32(int64(d)%1e9), loc: t.Location()}.normalize()
}

func (t Time) AddDate(years, months, days int) Time {
	year, monthValue, day, hour, min, sec, nsec := t.civil()
	year += years
	month := Month(monthValue) + Month(months)
	for month < January {
		month += 12
		year--
	}
	for month > December {
		month -= 12
		year++
	}
	day += days
	return Date(year, month, day, hour, min, sec, nsec, t.Location())
}

func (t Time) Sub(u Time) Duration {
	return Duration((t.sec-u.sec)*1e9 + int64(t.nsec-u.nsec))
}

func (t Time) Before(u Time) bool { return t.sec < u.sec || (t.sec == u.sec && t.nsec < u.nsec) }
func (t Time) After(u Time) bool  { return u.Before(t) }
func (t Time) Equal(u Time) bool  { return t.sec == u.sec && t.nsec == u.nsec }

func (t Time) Compare(u Time) int {
	switch {
	case t.Before(u):
		return -1
	case t.After(u):
		return 1
	default:
		return 0
	}
}

func (t Time) normalize() Time {
	t.sec += floorDiv(int64(t.nsec), 1e9)
	t.nsec = int32(floorMod(int64(t.nsec), 1e9))
	return t
}

func (t Time) String() string {
	layout := "2006-01-02 15:04:05.999999999 -0700 MST"
	if t.nsec == 0 {
		layout = "2006-01-02 15:04:05 -0700 MST"
	}
	return t.Format(layout)
}

func daysInMonth(year int, month Month) int {
	switch month {
	case February:
		if isLeap(year) {
			return 29
		}
		return 28
	case April, June, September, November:
		return 30
	default:
		return 31
	}
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func floorMod(a, b int64) int64 { return a - floorDiv(a, b)*b }

func daysFromCivil(year, month, day int) int64 {
	y := int64(year)
	m := int64(month)
	if m <= 2 {
		y--

		m += 9
	} else {
		m -= 3
	}
	era := floorDiv(y, 400)
	yearOfEra := y - era*400
	dayOfYear := (153*m+2)/5 + int64(day) - 1
	dayOfEra := yearOfEra*365 + yearOfEra/4 - yearOfEra/100 + dayOfYear
	return era*146097 + dayOfEra - 719468
}

func civilFromDays(days int64) (year, month, day int) {
	z := days + 719468
	era := floorDiv(z, 146097)
	dayOfEra := z - era*146097
	yearOfEra := (dayOfEra - dayOfEra/1460 + dayOfEra/36524 - dayOfEra/146096) / 365
	y := yearOfEra + era*400
	dayOfYear := dayOfEra - (365*yearOfEra + yearOfEra/4 - yearOfEra/100)
	mp := (5*dayOfYear + 2) / 153
	day = int(dayOfYear - (153*mp+2)/5 + 1)
	if mp < 10 {
		month = int(mp + 3)
	} else {
		month = int(mp - 9)
	}
	if month <= 2 {
		y++
	}
	return int(y), month, day
}
