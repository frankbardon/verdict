package dialect

// refer to https://kiegroup.github.io/dmn-feel-handbook/#date
// refer to https://docs.camunda.io/docs/components/modeler/feel/language-guide/feel-temporal-expressions/

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var ErrParseTemporal = errors.New("fail to parse temporal value")

type HasTime interface {
	Time() time.Time
}

type HasDate interface {
	Date() time.Time
}

type FEELTime struct {
	t time.Time
}

func (t FEELTime) Time() time.Time {
	return t.t
}

var timePatterns = []string{
	"15:04:05",
	"15:04:05-07:00",
	"15:04:05@MST",
}

func ParseTime(temporalStr string) (*FEELTime, error) {
	for _, pat := range timePatterns {
		if t, err := time.Parse(pat, temporalStr); err == nil {
			return &FEELTime{t: t}, nil
		}
	}
	return nil, ErrParseTemporal
}

func (t FEELTime) GetAttr(name string) (interface{}, bool) {
	switch name {
	case "hour":
		return t.t.Hour(), true
	case "minute":
		return t.t.Minute(), true
	case "second":
		return t.t.Second(), true
	case "timezone":
		zoneName, _ := t.t.Zone()
		return zoneName, true
	case "timezone offset":
		_, offset := t.t.Zone()
		return offset, true
	}
	return nil, false
}

func (t FEELTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

func (t FEELTime) String() string {
	return t.t.Format("15:04:05-07:00")
}

type FEELDate struct {
	t time.Time
}

func (d FEELDate) Date() time.Time {
	return d.t
}

func (d FEELDate) GetAttr(name string) (interface{}, bool) {
	switch name {
	case "year":
		return d.t.Year(), true
	case "month":
		return d.t.Month(), true
	case "day":
		return d.t.Day(), true
	}
	return nil, false
}

func (d FEELDate) String() string {
	return d.t.Format("2006-01-02")
}

func (d FEELDate) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

var datePatterns = []string{
	"2006-01-02",
}

func ParseDate(timeStr string) (*FEELDate, error) {
	for _, pat := range datePatterns {
		if t, err := time.Parse(pat, timeStr); err == nil {
			return &FEELDate{t: t}, nil
		}
	}
	return nil, ErrParseTemporal
}

type FEELDatetime struct {
	t time.Time
}

func (dt FEELDatetime) Time() time.Time {
	return dt.t
}

func (dt FEELDatetime) Date() time.Time {
	return dt.t
}

func (dt FEELDatetime) Equal(other FEELDatetime) bool {
	return dt.t.Equal(other.t)
}

func (dt FEELDatetime) Compare(other FEELDatetime) int {
	if dt.t.Equal(other.t) {
		return 0
	} else if dt.t.Before(other.t) {
		return -1
	} else {
		return 1
	}
}

func (dt FEELDatetime) GetAttr(name string) (interface{}, bool) {
	switch name {
	case "year":
		return dt.t.Year(), true
	case "month":
		return dt.t.Month(), true
	case "day":
		return dt.t.Day(), true
	case "hour":
		return dt.t.Hour(), true
	case "minute":
		return dt.t.Minute(), true
	case "second":
		return dt.t.Second(), true
	case "timezone":
		zoneName, _ := dt.t.Zone()
		return zoneName, true
	case "timezone offset":
		_, offset := dt.t.Zone()
		return offset, true
	}
	return nil, false
}

func (dt FEELDatetime) MarshalJSON() ([]byte, error) {
	return json.Marshal(dt.String())
}

func (dt FEELDatetime) String() string {
	return dt.t.Format("2006-01-02T15:04:05@MST")
}

func (dt *FEELDatetime) Add(dur *FEELDuration) *FEELDatetime {
	if dur.Years > 0 || dur.Months > 0 {
		durMonths := dur.Years*12 + dur.Months
		timeMonths := dt.t.Year()*12 + int(dt.t.Month()-1)

		newTimeMonths := timeMonths + durMonths
		if dur.Neg {
			newTimeMonths = timeMonths - durMonths
		}
		return &FEELDatetime{
			t: time.Date(
				newTimeMonths/12, time.Month(newTimeMonths%12+1),
				dt.t.Day(), dt.t.Hour(), dt.t.Minute(),
				dt.t.Second(), dt.t.Nanosecond(),
				dt.t.Location()),
		}
	}
	return &FEELDatetime{t: dt.t.Add(dur.Duration())}
}

func (dt *FEELDatetime) Sub(v HasTime) *FEELDuration {
	delta := dt.t.Sub(v.Time())
	return NewFEELDuration(delta)
}

var dateTimePatterns = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05-07:00",
	"2006-01-02T15:04:05@MST",
}

func ParseDatetime(temporalStr string) (*FEELDatetime, error) {
	for _, pat := range dateTimePatterns {
		if t, err := time.Parse(pat, temporalStr); err == nil {
			return &FEELDatetime{t: t}, nil
		}
	}
	return nil, ErrParseTemporal
}

func MustParseDatetime(temporalStr string) *FEELDatetime {
	t, err := ParseDatetime(temporalStr)
	if err != nil {
		panic(err)
	}
	return t
}

type FEELDuration struct {
	Neg     bool
	Years   int
	Months  int
	Days    int
	Hours   int
	Minutes int
	Seconds int
}

func NewFEELDuration(dur time.Duration) *FEELDuration {
	d := &FEELDuration{}
	ndur := int(dur)
	nhours := ndur / int(time.Hour)
	remain := ndur - nhours*int(time.Hour)
	nmins := remain / int(time.Minute)

	remain -= nmins * int(time.Minute)
	nsecs := remain / int(time.Second)

	d.Days = nhours / 24
	d.Hours = nhours - d.Days*24
	d.Minutes = nmins
	d.Seconds = nsecs
	return d
}

func (duration FEELDuration) GetAttr(name string) (interface{}, bool) {
	switch name {
	case "years":
		return duration.Years, true
	case "months":
		return duration.Months, true
	case "days":
		return duration.Days, true
	case "hours":
		return duration.Hours, true
	case "minutes":
		return duration.Minutes, true
	case "seconds":
		return duration.Seconds, true
	}
	return nil, false
}

func (duration FEELDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(duration.String())
}

func (duration FEELDuration) Duration() time.Duration {
	// duration.Year and duration.Month
	dv := time.Duration(duration.Days*24+duration.Hours)*time.Hour +
		time.Duration(duration.Minutes)*time.Minute +
		time.Duration(duration.Seconds)*time.Second
	if duration.Neg {
		dv = -dv
	}
	return dv
}

func (duration FEELDuration) Negative() *FEELDuration {
	neg := duration
	neg.Neg = !duration.Neg
	return &neg
}

func (duration FEELDuration) String() string {
	sYear, sMonth, sDay, sHour, sMinute, sSecond := "", "", "", "", "", ""
	sNeg := ""
	if duration.Neg {
		sNeg = "-"
	}
	if duration.Years != 0 {
		sYear = fmt.Sprintf("%dY", duration.Years)
	}
	if duration.Months != 0 {
		sMonth = fmt.Sprintf("%dM", duration.Months)
	}
	if duration.Days != 0 {
		sDay = fmt.Sprintf("%dD", duration.Days)
	}

	if duration.Hours != 0 {
		sHour = fmt.Sprintf("%dH", duration.Hours)
	}
	if duration.Minutes != 0 {
		sMinute = fmt.Sprintf("%dM", duration.Minutes)
	}
	if duration.Seconds != 0 {
		sSecond = fmt.Sprintf("%dS", duration.Seconds)
	}
	if sYear != "" || sMonth != "" {
		return fmt.Sprintf("%sP%s%s", sNeg, sYear, sMonth)
	}
	// A day-only duration is P1D, not P1DT: T appears only before a time
	// component, and a zero duration is PT0S rather than the empty PT.
	sTime := sHour + sMinute + sSecond
	if sTime != "" {
		sTime = "T" + sTime
	} else if sDay == "" {
		sTime = "T0S"
	}
	return fmt.Sprintf("%sP%s%s", sNeg, sDay, sTime)
}

// isYearsMonths reports whether the duration is a years-and-months duration
// rather than a days-and-time one. A zero duration counts as days-and-time.
func (duration FEELDuration) isYearsMonths() bool {
	return duration.Years != 0 || duration.Months != 0
}

// compareDurations orders two durations of the same kind: years-and-months by
// total months, days-and-time by total length, so P1D = PT24H. The two kinds
// are not comparable (DMN 1.5 §10.3.2.3.7 keeps them distinct types), which
// ok=false reports.
func compareDurations(a, b *FEELDuration) (r int, ok bool) {
	if a.isYearsMonths() != b.isYearsMonths() {
		return 0, false
	}
	var x, y int64
	if a.isYearsMonths() {
		x, y = int64(a.Years*12+a.Months), int64(b.Years*12+b.Months)
		if a.Neg {
			x = -x
		}
		if b.Neg {
			y = -y
		}
	} else {
		x, y = int64(a.Duration()), int64(b.Duration())
	}
	switch {
	case x < y:
		return -1, true
	case x > y:
		return 1, true
	}
	return 0, true
}

// AddDuration is date + duration. DMN 1.5 §10.3.2.15 (Table 56, addition of a date and a
// duration) evaluates it as the date of date and time(date, midnight) +
// duration, so a days-and-time duration moves whole days and drops any
// remainder of a day, and a years-and-months duration moves calendar months.
func (d *FEELDate) AddDuration(dur *FEELDuration) *FEELDate {
	dt := (&FEELDatetime{t: d.t}).Add(dur).t
	return &FEELDate{t: time.Date(dt.Year(), dt.Month(), dt.Day(), 0, 0, 0, 0, d.t.Location())}
}

var yearmonthDurationPattern = regexp.MustCompile(`^(\-?)P((\d+)Y)?((\d+)M)?$`)

// timeDurationPattern is the xs:dayTimeDuration lexical form, PnDTnHnMnS.
//
// Upstream required the T designator, so a duration with only a day part
// (P1D, -P2D) failed to parse; and it accepted a bare T (PT). xs:duration makes
// T present exactly when an hour, minute or second follows it, and requires at
// least one component overall. Both rules are checked in ParseDuration, since
// Go's regexp has no lookahead.
var timeDurationPattern = regexp.MustCompile(`^(\-?)P((\d+)D)?(T((\d+)H)?((\d+)M)?((\d+)S)?)?$`)

func ParseDuration(temporalStr string) (*FEELDuration, error) {
	// parse year month duration
	if submatches := yearmonthDurationPattern.FindStringSubmatch(temporalStr); submatches != nil {
		dur := &FEELDuration{}
		if submatches[1] != "" {
			dur.Neg = true
		}

		if submatches[2] != "" {
			y, err := strconv.ParseInt(submatches[3], 10, 64)
			if err != nil {
				return nil, err
			}
			dur.Years = int(y)
		}
		if submatches[4] != "" {
			m, err := strconv.ParseInt(submatches[5], 10, 64)
			if err != nil {
				return nil, err
			}
			if m > 12 {
				return nil, errors.New("months cannot exceed 12")
			}
			dur.Months = int(m)
		}
		return dur, nil
	}

	// parse day time duration
	if submatches := timeDurationPattern.FindStringSubmatch(temporalStr); submatches != nil {
		hasTime := submatches[5] != "" || submatches[7] != "" || submatches[9] != ""
		if submatches[4] != "" && !hasTime {
			// A T designator with nothing after it (PT, P1DT).
			return nil, ErrParseTemporal
		}
		if submatches[2] == "" && !hasTime {
			// No component at all (P is the years-and-months branch above).
			return nil, ErrParseTemporal
		}
		dur := &FEELDuration{}
		if submatches[1] != "" {
			dur.Neg = true
		}
		if submatches[2] != "" {
			v, err := strconv.ParseInt(submatches[3], 10, 64)
			if err != nil {
				return nil, err
			}
			dur.Days = int(v)
		}
		if submatches[5] != "" {
			v, err := strconv.ParseInt(submatches[6], 10, 64)
			if err != nil {
				return nil, err
			}
			if v > 24 {
				return nil, errors.New("hours cannot exceed 24")
			}
			dur.Hours = int(v)
		}
		if submatches[7] != "" {
			v, err := strconv.ParseInt(submatches[8], 10, 64)
			if err != nil {
				return nil, err
			}
			if v > 60 {
				return nil, errors.New("minutes cannot exceed 60")
			}
			dur.Minutes = int(v)
		}
		if submatches[9] != "" {
			v, err := strconv.ParseInt(submatches[10], 10, 64)
			if err != nil {
				return nil, err
			}
			if v > 60 {
				return nil, errors.New("seconds cannot exceed 60")
			}
			dur.Seconds = int(v)
		}
		return dur, nil
	}

	return nil, ErrParseTemporal
}

func ParseTemporalValue(temporalStr string) (interface{}, error) {
	if v, err := ParseDatetime(temporalStr); err == nil {
		return v, nil
	}

	if v, err := ParseTime(temporalStr); err == nil {
		return v, nil
	}

	if v, err := ParseDate(temporalStr); err == nil {
		return v, nil
	}

	return ParseDuration(temporalStr)
}

// installDatetimeFunctions builtin functions
func installDatetimeFunctions(prelude *Prelude) {
	// conversions
	prelude.Bind("date", wrapTyped(func(frm string) (interface{}, error) {
		return ParseDate(frm)
	}).Required("from"))

	prelude.Bind("time", wrapTyped(func(frm string) (interface{}, error) {
		return ParseTime(frm)
	}).Required("from"))

	prelude.Bind("date and time", wrapTyped(func(frm string) (interface{}, error) {
		return ParseDatetime(frm)
	}).Required("from"))

	prelude.Bind("duration", wrapTyped(func(frm string) (interface{}, error) {
		return ParseDuration(frm)
	}).Required("from"))

	// temporal functions
	prelude.Bind("now", wrapTyped(func() (interface{}, error) {
		return &FEELDatetime{t: time.Now()}, nil
	}))

	prelude.Bind("today", wrapTyped(func() (interface{}, error) {
		now := time.Now()
		date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		return &FEELDate{t: date}, nil
	}))

	prelude.Bind("day of week", wrapTyped(func(v HasDate) (interface{}, error) {
		return v.Date().Weekday(), nil
	}).Required("date"))

	prelude.Bind("day of year", wrapTyped(func(v HasDate) (interface{}, error) {
		return v.Date().YearDay(), nil
	}).Required("date"))

	prelude.Bind("week of year", wrapTyped(func(v HasDate) (interface{}, error) {
		_, week := v.Date().ISOWeek()
		return week, nil
	}).Required("date"))

	prelude.Bind("month of year", wrapTyped(func(v HasDate) (interface{}, error) {
		return v.Date().Month(), nil
	}).Required("date"))

	prelude.Bind("abs", wrapTyped(func(dur *FEELDuration) (interface{}, error) {
		newDur := *dur
		newDur.Neg = false
		return newDur, nil
	}).Required("dur"))

	// refs https://docs.camunda.io/docs/components/modeler/feel/builtin-functions/feel-built-in-functions-temporal/#last-day-of-monthdate
	prelude.Bind("last day of month", wrapTyped(func(v HasDate) (interface{}, error) {
		month := v.Date().Month()
		year := v.Date().Year()
		if month == 12 {
			year++
			month = 1
		} else {
			month++
		}
		nextFirstDay := time.Date(year, month, 1, 0, 0, 0, 0, v.Date().Location())
		lastDay := nextFirstDay.Add(-24 * time.Hour) // 1 day before
		return lastDay.Day(), nil
	}).Required("date"))
}
