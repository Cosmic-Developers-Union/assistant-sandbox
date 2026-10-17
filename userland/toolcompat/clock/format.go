package clock

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	Layout      = "01/02 03:04:05PM '06 -0700"
	ANSIC       = "Mon Jan _2 15:04:05 2006"
	UnixDate    = "Mon Jan _2 15:04:05 MST 2006"
	RubyDate    = "Mon Jan 02 15:04:05 -0700 2006"
	RFC822      = "02 Jan 06 15:04 MST"
	RFC822Z     = "02 Jan 06 15:04 -0700"
	RFC850      = "Monday, 02-Jan-06 15:04:05 MST"
	RFC1123     = "Mon, 02 Jan 2006 15:04:05 MST"
	RFC1123Z    = "Mon, 02 Jan 2006 15:04:05 -0700"
	RFC3339     = "2006-01-02T15:04:05Z07:00"
	RFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
	Kitchen     = "3:04PM"
	Stamp       = "Jan _2 15:04:05"
	StampMilli  = "Jan _2 15:04:05.000"
	DateTime    = "2006-01-02 15:04:05"
	DateOnly    = "2006-01-02"
	TimeOnly    = "15:04:05"
)

var formatChunks = []string{
	"2006", "January", "Monday", ".999999999", ".000000000", "-07:00", "Z07:00",
	"Jan", "Mon", ".000", ".999", "-0700", "Z0700", "002", "_2", "01", "02",
	"15", "03", "04", "05", "06", "PM", "pm", "MST", "1", "2", "3",
}

func (t Time) Format(layout string) string {
	var b strings.Builder
	for i := 0; i < len(layout); {
		chunk := matchChunk(layout[i:])
		if chunk == "" {
			b.WriteByte(layout[i])
			i++
			continue
		}
		b.WriteString(t.formatChunk(chunk))
		i += len(chunk)
	}
	return b.String()
}

func matchChunk(rest string) string {
	if fraction := matchFraction(rest); fraction != "" {
		return fraction
	}
	for _, chunk := range formatChunks {
		if strings.HasPrefix(rest, chunk) {
			return chunk
		}
	}
	return ""
}

func matchFraction(rest string) string {
	if len(rest) < 2 || (rest[0] != '.' && rest[0] != ',') {
		return ""
	}
	digit := rest[1]
	if digit != '9' && digit != '0' {
		return ""
	}
	count := 0
	for count < 9 && 1+count < len(rest) && rest[1+count] == digit {
		count++
	}
	if 1+count < len(rest) && rest[1+count] >= '0' && rest[1+count] <= '9' {
		return ""
	}
	return rest[:1+count]
}

func (t Time) formatChunk(chunk string) string {
	year, month, day, hour, min, sec, nsec := t.civil()
	switch chunk {
	case "2006":
		return pad(year, 4)
	case "06":
		return pad(int(floorMod(int64(year), 100)), 2)
	case "January":
		return Month(month).String()
	case "Jan":
		return Month(month).String()[:3]
	case "01":
		return pad(month, 2)
	case "1":
		return strconv.Itoa(month)
	case "02":
		return pad(day, 2)
	case "2":
		return strconv.Itoa(day)
	case "_2":
		return fmt.Sprintf("%2d", day)
	case "002":
		return pad(t.YearDay(), 3)
	case "Monday":
		return t.Weekday().String()
	case "Mon":
		return t.Weekday().String()[:3]
	case "15":
		return pad(hour, 2)
	case "03":
		return pad(hour12(hour), 2)
	case "3":
		return strconv.Itoa(hour12(hour))
	case "04":
		return pad(min, 2)
	case "05":
		return pad(sec, 2)
	case "PM":
		return ampm(hour)
	case "pm":
		return strings.ToLower(ampm(hour))
	case "MST":
		return t.Location().String()
	case "-0700":
		return zoneText(t.Location().offset, "", false)
	case "-07:00":
		return zoneText(t.Location().offset, "", true)
	case "Z0700":
		return zoneText(t.Location().offset, "Z", false)
	case "Z07:00":
		return zoneText(t.Location().offset, "Z", true)
	}
	if len(chunk) > 1 && chunk[0] == '.' {
		if chunk[1] == '9' {
			return trimFraction(nsec, len(chunk)-1)
		}
		return "." + pad(nsec/fractionScale(len(chunk)-1), len(chunk)-1)
	}
	return ""
}

func fractionScale(digits int) int {
	scale := 1
	for i := 0; i < 9-digits; i++ {
		scale *= 10
	}
	return scale
}

func hour12(hour int) int {
	value := hour % 12
	if value == 0 {
		value = 12
	}
	return value
}

func ampm(hour int) string {
	if hour < 12 {
		return "AM"
	}
	return "PM"
}

func zoneText(offset int, zero string, colon bool) string {
	if offset == 0 && zero != "" {
		return zero
	}
	sign := "+"
	if offset < 0 {
		sign = "-"
		absolute := offset
		if absolute < 0 {
			absolute = -absolute
		}
		if colon {
			return fmt.Sprintf("%s%02d:%02d", sign, absolute/3600, absolute%3600/60)
		}
		return fmt.Sprintf("%s%02d%02d", sign, absolute/3600, absolute%3600/60)
	}
	if colon {
		return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60)
	}
	return fmt.Sprintf("%s%02d%02d", sign, offset/3600, offset%3600/60)
}

func trimFraction(nsec, digits int) string {
	value := pad(nsec/fractionScale(digits), digits)
	value = strings.TrimRight(value, "0")
	if value == "" {
		return ""
	}
	return "." + value
}

func pad(value, width int) string {
	text := strconv.Itoa(value)
	for len(text) < width {
		text = "0" + text
	}
	return text
}

func Parse(layout, value string) (Time, error) {
	return ParseInLocation(layout, value, UTC)
}

func ParseInLocation(layout, value string, loc *Location) (Time, error) {
	if loc == nil {
		loc = UTC
	}
	year, month, day := 0, January, 1
	hour, min, sec, nsec := 0, 0, 0, 0
	pm, sawPM := false, false
	offset := loc.offset
	haveOffset := false
	pos := 0
	for i := 0; i < len(layout); {
		chunk := matchChunk(layout[i:])
		if chunk == "" {
			if pos >= len(value) || value[pos] != layout[i] {
				return Time{}, parseError(layout, value)
			}
			pos++
			i++
			continue
		}
		parsed, consumed, err := parseChunk(chunk, value[pos:])
		if err != nil {
			return Time{}, parseError(layout, value)
		}
		pos += consumed
		switch {
		case chunk == "2006":
			year = parsed
		case chunk == "06":

			if parsed < 69 {
				year = 2000 + parsed
			} else {
				year = 1900 + parsed
			}
		case chunk == "01" || chunk == "1" || chunk == "Jan" || chunk == "January":
			month = Month(parsed)
		case chunk == "02" || chunk == "2" || chunk == "_2" || chunk == "002":
			day = parsed
		case chunk == "15" || chunk == "03" || chunk == "3":
			hour = parsed
		case chunk == "04":
			min = parsed
		case chunk == "05":
			sec = parsed

			if fraction, consumed, err := trailingFraction(value[pos:]); err == nil && consumed > 0 {
				nsec = fraction
				pos += consumed
			}
		case chunk == "PM" || chunk == "pm":
			pm, sawPM = parsed == 1, true
		case strings.HasPrefix(chunk, "."):

			if consumed > 0 {
				nsec = parsed
			}
		case chunk == "-0700" || chunk == "-07:00" || chunk == "Z0700" || chunk == "Z07:00":
			offset, haveOffset = parsed, true
		case chunk == "MST":

			if parsed != 0 {
				return Time{}, fmt.Errorf("%w: %q", ErrNoTimeZoneData, value)
			}
			offset, haveOffset = 0, true
		}
		i += len(chunk)
	}
	if pos != len(value) {
		return Time{}, parseError(layout, value)
	}
	if sawPM {
		if pm && hour < 12 {
			hour += 12
		}
		if !pm && hour == 12 {
			hour = 0
		}
	}
	if !haveOffset {
		offset = loc.offset
	}
	return date(year, month, day, hour, min, sec, nsec, FixedZone(loc.name, offset)), nil
}

func parseChunk(chunk, s string) (int, int, error) {
	switch chunk {
	case "January", "Jan":
		for index, name := range monthNames {
			candidate := name
			if chunk == "Jan" {
				candidate = name[:3]
			}
			if len(s) >= len(candidate) && strings.EqualFold(s[:len(candidate)], candidate) {
				return index + 1, len(candidate), nil
			}
		}
		return 0, 0, parseError(chunk, s)
	case "Monday", "Mon":
		for index, name := range weekdayNames {
			candidate := name
			if chunk == "Mon" {
				candidate = name[:3]
			}
			if len(s) >= len(candidate) && strings.EqualFold(s[:len(candidate)], candidate) {
				return index, len(candidate), nil
			}
		}
		return 0, 0, parseError(chunk, s)
	case "PM", "pm":
		if len(s) >= 2 && strings.EqualFold(s[:2], "AM") {
			return 0, 2, nil
		}
		if len(s) >= 2 && strings.EqualFold(s[:2], "PM") {
			return 1, 2, nil
		}
		return 0, 0, parseError(chunk, s)
	case "MST":
		if strings.HasPrefix(s, "UTC") {
			return 0, 3, nil
		}
		if strings.HasPrefix(s, "GMT") {
			return 0, 3, nil
		}
		return 0, 0, parseError(chunk, s)
	case "-0700", "-07:00", "Z0700", "Z07:00":
		colon := chunk == "-07:00" || chunk == "Z07:00"
		width := 5
		if colon {
			width = 6
		}
		if len(s) == 0 {
			return 0, 0, parseError(chunk, s)
		}
		if (s[0] == 'Z' || s[0] == 'z') && (chunk == "Z0700" || chunk == "Z07:00") {
			return 0, 1, nil
		}
		if len(s) < width || (s[0] != '+' && s[0] != '-') {
			return 0, 0, parseError(chunk, s)
		}
		body := strings.Replace(s[1:width], ":", "", 1)
		value, err := strconv.Atoi(body)
		if err != nil {
			return 0, 0, parseError(chunk, s)
		}
		offset := (value/100)*3600 + (value%100)*60
		if s[0] == '-' {
			offset = -offset
		}
		return offset, width, nil
	}
	if strings.HasPrefix(chunk, ".") || strings.HasPrefix(chunk, ",") {
		return parseFraction(chunk, s)
	}
	switch chunk {
	case "_2":
		spaces := 0
		for spaces < len(s) && s[spaces] == ' ' {
			spaces++
		}
		if spaces > 0 {
			value, consumed, err := parseNumber(s[spaces:], 1)
			return value, consumed + spaces, err
		}
		return parseNumber(s, 2)
	case "2006":
		return parseNumber(s, 4)
	case "06", "01", "02", "15", "04", "05", "002":
		return parseNumber(s, 2)
	case "1", "2", "3":
		return parseNumber(s, 2)
	default:
		return 0, 0, parseError(chunk, s)
	}
}

func trailingFraction(s string) (int, int, error) {
	if len(s) < 2 || (s[0] != '.' && s[0] != ',') || s[1] < '0' || s[1] > '9' {
		return 0, 0, nil
	}
	return parseFraction(".999999999", s)
}

func parseFraction(chunk, s string) (int, int, error) {
	if len(s) == 0 || (s[0] != '.' && s[0] != ',') {

		return 0, 0, nil
	}
	digits := len(chunk) - 1
	maximum := digits
	if maximum > len(s)-1 {
		maximum = len(s) - 1
	}
	count := 0
	for count < maximum && s[1+count] >= '0' && s[1+count] <= '9' {
		count++
	}
	if count == 0 {
		return 0, 0, parseError(chunk, s)
	}
	value, err := strconv.Atoi(s[1 : 1+count])
	if err != nil {
		return 0, 0, parseError(chunk, s)
	}
	for i := count; i < digits; i++ {
		value *= 10
	}
	return value, 1 + count, nil
}

func parseNumber(s string, width int) (int, int, error) {
	count := 0
	for count < width && count < len(s) && s[count] >= '0' && s[count] <= '9' {
		count++
	}
	if count == 0 {
		return 0, 0, parseError("number", s)
	}
	value, err := strconv.Atoi(s[:count])
	if err != nil {
		return 0, 0, parseError("number", s)
	}
	return value, count, nil
}

func parseError(layout, value string) error {
	return fmt.Errorf("无法用布局 %q 解析 %q", layout, value)
}
