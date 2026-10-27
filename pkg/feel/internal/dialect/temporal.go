package dialect

// refer to https://kiegroup.github.io/dmn-feel-handbook/#date
// refer to https://docs.camunda.io/docs/components/modeler/feel/language-guide/feel-temporal-expressions/

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrParseTemporal = errors.New("fail to parse temporal value")

// ErrUnsupportedTimeZone reports a time or date-and-time string in the DMN
// lexical form whose IANA zone id (`@Europe/Paris`) the fork cannot resolve.
// VERDICT DIVERGENCE: kept distinct from ErrParseTemporal so a conversion
// built-in raises on it instead of yielding null — the string is inside the
// domain, the fork is what falls short.
var ErrUnsupportedTimeZone = errors.New("time zone id not supported")

// parseZoned tries each layout against s. VERDICT DIVERGENCE: a trailing
// lowercase `z` is read as UTC, as in DMN 1.5 Table 72's own examples
// (`time("23:59:00z")`), and a string whose local part parses but whose `@`
// zone id does not is ErrUnsupportedTimeZone rather than ErrParseTemporal.
func parseZoned(s string, layouts []string, local string) (time.Time, error) {
	if strings.HasSuffix(s, "z") {
		s = s[:len(s)-1] + "Z"
	}
	for _, pat := range layouts {
		if t, err := time.Parse(pat, s); err == nil {
			return t, nil
		}
	}
	if i := strings.LastIndex(s, "@"); i > 0 {
		if _, err := time.Parse(local, s[:i]); err == nil {
			return time.Time{}, fmt.Errorf("%w: %s", ErrUnsupportedTimeZone, s[i+1:])
		}
	}
	return time.Time{}, ErrParseTemporal
}

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

// VERDICT DIVERGENCE: Z07:00 (upstream -07:00) also admits the UTC
// designator `Z`, which DMN 1.5 §10.3.4.1 Table 72 uses in its examples.
var timePatterns = []string{
	"15:04:05",
	"15:04:05Z07:00",
	"15:04:05@MST",
}

func ParseTime(temporalStr string) (*FEELTime, error) {
	t, err := parseZoned(temporalStr, timePatterns, "15:04:05")
	if err != nil {
		return nil, err
	}
	return &FEELTime{t: t}, nil
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

// VERDICT DIVERGENCE: Z07:00 as in timePatterns.
var dateTimePatterns = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05@MST",
}

func ParseDatetime(temporalStr string) (*FEELDatetime, error) {
	t, err := parseZoned(temporalStr, dateTimePatterns, "2006-01-02T15:04:05")
	if err != nil {
		return nil, err
	}
	return &FEELDatetime{t: t}, nil
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

// NewFEELDuration carries a negative duration's sign in Neg and keeps every
// field non-negative, the form the parser produces for "-PT5H". Upstream left
// the sign in the fields (Hours: -5, Neg: false), so a negative Go duration or
// a negative date-and-time difference printed as "PT-5H" and survived abs and
// negation still negative.
func NewFEELDuration(dur time.Duration) *FEELDuration {
	d := &FEELDuration{}
	if dur < 0 {
		d.Neg = true
		dur = -dur
	}
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

	// VERDICT DIVERGENCE: xs:dayTimeDuration admits fractional seconds
	// (PT1.5S), which FEELDuration's whole-second fields cannot hold. Report
	// that as a limitation, not as a malformed string, so duration() raises
	// rather than yielding the null reserved for input outside its domain.
	if fractionalSecondsPattern.MatchString(temporalStr) {
		return nil, ErrFractionalSeconds
	}
	return nil, ErrParseTemporal
}

// fractionalSecondsPattern is timeDurationPattern with a fractional seconds
// component.
var fractionalSecondsPattern = regexp.MustCompile(`^(\-?)P((\d+)D)?T((\d+)H)?((\d+)M)?\d+\.\d+S$`)

// ErrFractionalSeconds reports a days and time duration string in the DMN
// lexical form whose seconds have a fractional part the fork cannot hold.
var ErrFractionalSeconds = errors.New("fractional seconds in a duration are not supported")

func ParseTemporalValue(temporalStr string) (interface{}, error) {
	v, dtErr := ParseDatetime(temporalStr)
	if dtErr == nil {
		return v, nil
	}

	t, tErr := ParseTime(temporalStr)
	if tErr == nil {
		return t, nil
	}
	// VERDICT DIVERGENCE: name the unsupported zone id rather than the
	// generic parse failure the duration fallback would report.
	for _, err := range []error{dtErr, tErr} {
		if errors.Is(err, ErrUnsupportedTimeZone) {
			return nil, err
		}
	}

	if v, err := ParseDate(temporalStr); err == nil {
		return v, nil
	}

	return ParseDuration(temporalStr)
}

// stringConversion builds a one-argument string → value conversion built-in
// that yields null outside its domain (DMN 1.5 §10.3.4, Table 72): a string
// the parser rejects as malformed (ErrParseTemporal), null, or an argument of
// any other type. Inputs inside the domain that the fork falls short on still
// raise, so they never read as malformed: a parser error other than
// ErrParseTemporal (an IANA zone id, a component over upstream's cap such as
// P26M or PT36H), and an argument for which unimplemented reports a spec
// overload the fork lacks (date(date and time), time(time | date and time)).
func stringConversion[T any](parse func(string) (T, error), unimplemented func(any) bool) *NativeFun {
	return NewNativeFunc(func(kwargs map[string]any) (any, error) {
		from := kwargs["from"]
		if s, ok := from.(string); ok {
			v, err := parse(s)
			if errors.Is(err, ErrParseTemporal) {
				return null, nil
			}
			if err != nil {
				return nil, err
			}
			return v, nil
		}
		if unimplemented != nil && unimplemented(from) {
			return nil, NewErrTypeMismatch("string")
		}
		return null, nil
	})
}

func isDatetime(v any) bool {
	switch v.(type) {
	case *FEELDatetime, FEELDatetime:
		return true
	}
	return false
}

func isTimeOrDatetime(v any) bool {
	switch v.(type) {
	case *FEELTime, FEELTime:
		return true
	}
	return isDatetime(v)
}

// installDatetimeFunctions builtin functions
func installDatetimeFunctions(prelude *Prelude) {
	// conversions
	//
	// VERDICT DIVERGENCE: the four string conversions yield null for a string
	// outside their lexical domain, and for an argument that is no overload's
	// domain, instead of failing the evaluation. DMN 1.5 §10.3.4: "Whenever a
	// parameter is outside its domain, the result of the built-in is null."
	// date(date and time) and time(time | date and time) still raise: they
	// are overloads the spec defines and the fork has not implemented, and
	// nulling them would hide that gap behind a wrong answer.
	prelude.Bind("date", stringConversion(ParseDate, isDatetime).Required("from"))
	prelude.Bind("time", stringConversion(ParseTime, isTimeOrDatetime).Required("from"))
	prelude.Bind("date and time", stringConversion(ParseDatetime, nil).Required("from"))
	prelude.Bind("duration", stringConversion(ParseDuration, nil).Required("from"))

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

	// abs dispatches on the argument type rather than decoding it into a
	// duration: upstream's mapstructure decode turned a number (a struct) or
	// null into a zero FEELDuration, so `abs(2)` was the empty duration "PT".
	// DMN 1.5 §10.3.4.5 defines abs(n) over a number, a days and time duration
	// or a years and months duration; anything else, null included, is outside
	// the domain and yields null (§10.3.4).
	prelude.Bind("abs", NewNativeFunc(func(kwargs map[string]any) (any, error) {
		switch n := kwargs["n"].(type) {
		case *Number:
			if n.v.Sign() < 0 {
				return NewNumberFromInt64(0).Sub(n), nil
			}
			return n, nil
		case *FEELDuration:
			abs := *n
			abs.Neg = false
			return &abs, nil
		case FEELDuration:
			n.Neg = false
			return &n, nil
		}
		return null, nil
	}).Required("n"))

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
