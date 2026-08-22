package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses the ISO-8601 duration subset DMN uses for policy
// attributes: PnDTnHnMnS, optionally negated.
//
// Years and months are rejected. They are legal ISO-8601 and legal FEEL, but a
// latency or retry budget measured in calendar months has no fixed length and
// therefore no meaning as a timeout; accepting one would silently pick an
// arbitrary interpretation.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("duration must start with P")
	}
	body := s[1:]
	if strings.ContainsAny(body, "Yy") {
		return 0, fmt.Errorf("years are not a valid latency budget")
	}
	datePart, timePart, _ := strings.Cut(body, "T")
	if strings.Contains(datePart, "M") {
		return 0, fmt.Errorf("months are not a valid latency budget")
	}

	var total time.Duration
	consume := func(part string, units map[byte]time.Duration) error {
		num := ""
		for i := 0; i < len(part); i++ {
			ch := part[i]
			if (ch >= '0' && ch <= '9') || ch == '.' {
				num += string(ch)
				continue
			}
			unit, ok := units[ch]
			if !ok {
				return fmt.Errorf("unexpected unit %q", string(ch))
			}
			if num == "" {
				return fmt.Errorf("unit %q has no value", string(ch))
			}
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return fmt.Errorf("bad value %q: %w", num, err)
			}
			total += time.Duration(f * float64(unit))
			num = ""
		}
		if num != "" {
			return fmt.Errorf("trailing value %q has no unit", num)
		}
		return nil
	}
	if err := consume(datePart, map[byte]time.Duration{'D': 24 * time.Hour, 'W': 7 * 24 * time.Hour}); err != nil {
		return 0, err
	}
	if err := consume(timePart, map[byte]time.Duration{'H': time.Hour, 'M': time.Minute, 'S': time.Second}); err != nil {
		return 0, err
	}
	if neg {
		total = -total
	}
	return total, nil
}

// FormatDuration renders a duration in the ISO-8601 form DMN policy attributes
// use. It is the inverse of ParseDuration, so a policy round-trips through
// either serialisation unchanged.
func FormatDuration(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}
	neg := d < 0
	if neg {
		d = -d
	}
	days := int64(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int64(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int64(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := float64(d) / float64(time.Second)

	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteByte('P')
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}
	var t strings.Builder
	if hours > 0 {
		fmt.Fprintf(&t, "%dH", hours)
	}
	if minutes > 0 {
		fmt.Fprintf(&t, "%dM", minutes)
	}
	if seconds > 0 || (t.Len() == 0 && days == 0) {
		if seconds == float64(int64(seconds)) {
			fmt.Fprintf(&t, "%dS", int64(seconds))
		} else {
			fmt.Fprintf(&t, "%gS", seconds)
		}
	}
	if t.Len() > 0 {
		b.WriteByte('T')
		b.WriteString(t.String())
	}
	return b.String()
}
