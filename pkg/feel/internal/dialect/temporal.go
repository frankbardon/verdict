package dialect

// refer to https://kiegroup.github.io/dmn-feel-handbook/#date
// refer to https://docs.camunda.io/docs/components/modeler/feel/language-guide/feel-temporal-expressions/

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"

	// VERDICT DIVERGENCE: the IANA time zone database is embedded, so an
	// `@Europe/Paris` zone id resolves in a scratch container with no
	// /usr/share/zoneinfo, and resolves alike on every host.
	_ "time/tzdata"
)

var ErrParseTemporal = errors.New("fail to parse temporal value")

// ErrUnsupportedTimeZone reports a time or date-and-time string in the DMN
// lexical form whose IANA zone id (`@Europe/Paris`) the fork cannot resolve.
// VERDICT DIVERGENCE: kept distinct from ErrParseTemporal so a conversion
// built-in raises on it instead of yielding null — the string is inside the
// domain, the fork is what falls short.
var ErrUnsupportedTimeZone = errors.New("time zone id not supported")

// ErrTimeZoneIDOnTime reports a time string whose `@` zone id has more than
// one offset in its history, so a time of day alone cannot say which applies.
// It wraps ErrParseTemporal: time(from) yields null on it, and an at-literal
// naming it is a load error that says why.
//
// VERDICT DIVERGENCE: DMN 1.5 does not say how a time, which has no date,
// takes its offset from a zone id whose rules depend on the date
// (§10.3.2.3.4). Resolving it at the evaluation's date would make one literal
// mean different instants in summer and winter, so the fork rejects it. A
// zone id with a single fixed offset (`UTC`, `Etc/GMT+5`) is unambiguous and
// accepted.
var ErrTimeZoneIDOnTime = fmt.Errorf("%w: a time cannot take a zone id whose offset varies by date; use an offset", ErrParseTemporal)

// zoneKind is how a time or date and time string placed itself relative to
// UTC: not at all (a local value), by a time offset (`Z`, `+02:00`), or by an
// IANA zone id (`@Europe/Paris`).
type zoneKind int

const (
	zoneNone zoneKind = iota
	zoneOffset
	zoneID
)

// parseZoned tries each layout against s: the first is the zoneless local
// form, the others carry a time offset. Failing those, `local@zone-id` is the
// local form placed in the IANA zone, whose rules give the offset at that
// wall clock (DMN 1.5 §10.3.2.3.6). VERDICT DIVERGENCE: a trailing lowercase
// `z` is read as UTC, as in DMN 1.5 Table 72's own examples
// (`time("23:59:00z")`); a string whose local part parses but whose `@` zone
// id the time zone database does not know is ErrUnsupportedTimeZone rather
// than ErrParseTemporal; and upstream discarded the zone kind, so `10:00:00`
// and `10:00:00Z` were one value (see FEELTime) and a zone id went through
// Go's `MST` abbreviation layout, which read `@CET` as UTC and could not read
// `@Europe/Paris` at all.
//
// An offset value is held in a location named only by its offset, never the
// process's local zone (time.Parse picks Local when the offsets agree), so
// nothing about it depends on where the engine runs.
func parseZoned(s string, layouts []string, local string) (t time.Time, zone zoneKind, err error) {
	if strings.HasSuffix(s, "z") {
		s = s[:len(s)-1] + "Z"
	}
	for i, pat := range layouts {
		if t, err := time.Parse(pat, s); err == nil {
			if i == 0 {
				return t, zoneNone, nil
			}
			return atFixedOffset(t), zoneOffset, nil
		}
	}
	i := strings.LastIndex(s, "@")
	if i <= 0 {
		return time.Time{}, zoneNone, ErrParseTemporal
	}
	wall, err := time.Parse(local, s[:i])
	if err != nil {
		return time.Time{}, zoneNone, ErrParseTemporal
	}
	id := s[i+1:]
	if id == "" {
		return time.Time{}, zoneNone, ErrParseTemporal
	}
	loc, err := loadZone(id)
	if err != nil {
		return time.Time{}, zoneNone, fmt.Errorf("%w: %s", ErrUnsupportedTimeZone, id)
	}
	return time.Date(wall.Year(), wall.Month(), wall.Day(),
		wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc), zoneID, nil
}

// loadZone resolves an IANA zone id. `Local` names the process's zone, which
// a decision must not depend on, so it is not a zone id here.
func loadZone(id string) (*time.Location, error) {
	if id == "Local" {
		return nil, ErrUnsupportedTimeZone
	}
	return time.LoadLocation(id)
}

// atFixedOffset is t in a location that is its offset and nothing else: UTC
// for a zero offset, otherwise an unnamed fixed zone.
func atFixedOffset(t time.Time) time.Time {
	_, offset := t.Zone()
	if offset == 0 {
		return t.In(time.UTC)
	}
	return t.In(time.FixedZone("", offset))
}

// hasFixedOffset reports whether loc has had one offset for all time, so that
// a time of day in it has an unambiguous UTC form.
func hasFixedOffset(loc *time.Location) bool {
	start, end := time.Date(2000, 1, 1, 0, 0, 0, 0, loc).ZoneBounds()
	return start.IsZero() && end.IsZero()
}

// formatZoned is t in DMN's lexical form (§10.3.4.1, Table 72): the local
// layout, then nothing for a local value, `@zone-id` for one placed by an IANA
// zone id, and otherwise its offset — `Z` for UTC, `+05:00` for any other — so
// that parsing the string gives the value back.
//
// VERDICT DIVERGENCE: callers pass a layout ending in `05.999999999`, so a
// fraction of a second prints only when it is non-zero, without trailing zeros
// (`10:00:00.5`, never `10:00:00.500` or `10:00:00.0`). Upstream printed whole
// seconds only, so `string(time("10:00:00.5"))` was `10:00:00`, which parses
// back to a different time.
func formatZoned(t time.Time, layout string, hasOffset, zoneID bool) string {
	switch {
	case !hasOffset:
		return t.Format(layout)
	case zoneID:
		return t.Format(layout) + "@" + t.Location().String()
	}
	return t.Format(layout + "Z07:00")
}

type HasTime interface {
	Time() time.Time
}

type HasDate interface {
	Date() time.Time
}

// FEELTime is a FEEL time. A zoneless (local) time keeps its wall clock in t
// with the UTC location; hasOffset tells it apart from the same wall clock
// written with `Z`.
//
// VERDICT DIVERGENCE: upstream had no hasOffset and read a zoneless time as
// UTC, so `time("10:00:00") = time("10:00:00Z")` was true and the two printed
// alike. DMN 1.5 §10.3.2.3.4: a time with an offset, 00:00 included, has a UTC
// form; one without is a local time of day whose relation to UTC is unknown.
// The operators use the flag as Tables 53, 54 and 57 require; see
// mixedZones in eval_binop.go.
//
// zoneID is set when the offset came from an IANA zone id rather than being
// written out: the value then prints as `@zone-id` and has a `timezone`.
type FEELTime struct {
	t         time.Time
	hasOffset bool
	zoneID    bool
}

func (t FEELTime) Time() time.Time {
	return t.t
}

// HasOffset reports whether the time has a time offset or zone, rather than
// being a local time of day.
func (t FEELTime) HasOffset() bool {
	return t.hasOffset
}

// VERDICT DIVERGENCE: Z07:00 (upstream -07:00) also admits the UTC
// designator `Z`, which DMN 1.5 §10.3.4.1 Table 72 uses in its examples. The
// upstream `@MST` layout is gone: parseZoned reads a zone id itself.
var timePatterns = []string{
	"15:04:05",
	"15:04:05Z07:00",
}

// ParseTime parses a time string. A zone id whose offset varies by date is
// ErrTimeZoneIDOnTime.
func ParseTime(temporalStr string) (*FEELTime, error) {
	t, zone, err := parseZoned(temporalStr, timePatterns, "15:04:05")
	if err != nil {
		return nil, err
	}
	if zone == zoneID && !hasFixedOffset(t.Location()) {
		return nil, ErrTimeZoneIDOnTime
	}
	return &FEELTime{t: t, hasOffset: zone != zoneNone, zoneID: zone == zoneID}, nil
}

// GetAttr is the time row of DMN 1.5 §10.3.2.15 Table 65: `hour`,
// `minute`, `second`, `time offset` and `timezone` (see zoneAttr).
// VERDICT DIVERGENCE: `second` is a decimal (Table 66, "[0..60)"), so
// `time("10:00:00.5").second` is 0.5; upstream dropped the fraction.
func (t FEELTime) GetAttr(name string) (interface{}, bool) {
	switch name {
	case "hour":
		return t.t.Hour(), true
	case "minute":
		return t.t.Minute(), true
	case "second":
		return secondAttr(t.t), true
	}
	return zoneAttr(t.t, t.hasOffset, t.zoneID, name)
}

// secondAttr is t's second of the minute with its fraction (DMN 1.5 Table 66:
// "a decimal in the interval [0..60)"), a whole number when there is none.
func secondAttr(t time.Time) any {
	if t.Nanosecond() == 0 {
		return t.Second()
	}
	frac := strings.TrimRight(fmt.Sprintf("%09d", t.Nanosecond()), "0")
	return NewNumber(fmt.Sprintf("%d.%s", t.Second(), frac))
}

// dateAttr is the date components of DMN 1.5 Table 65 that a date and a date
// and time share, read at t's own wall clock. VERDICT DIVERGENCE: `month` is
// a number (Table 66), where upstream returned Go's time.Month, which became
// the string "January"; and `weekday` exists, the ISO 8601 day of the week
// Table 66 names, 1 for Monday to 7 for Sunday — upstream had none.
func dateAttr(t time.Time, name string) (any, bool) {
	switch name {
	case "year":
		return t.Year(), true
	case "month":
		return int(t.Month()), true
	case "day":
		return t.Day(), true
	case "weekday":
		if wd := t.Weekday(); wd != time.Sunday {
			return int(wd), true
		}
		return 7, true
	}
	return nil, false
}

// zoneAttr answers the zone properties of a time or a date and time (DMN 1.5
// §10.3.2.3, the temporal property table). `time offset` is the offset as a
// days and time duration. VERDICT DIVERGENCE: all three are null for a
// zoneless value — "returns null when the object does not have a time offset
// set" — where upstream answered "UTC" and 0, which a zoneless value does not
// have. Upstream had no `time offset` at all. `timezone` is the IANA zone id,
// and null for a value written with an offset, which names no zone: upstream
// answered Go's name for the offset's location, `""` or the process zone's
// abbreviation.
func zoneAttr(t time.Time, hasOffset, zoneID bool, name string) (any, bool) {
	switch name {
	case "time offset", "timezone", "timezone offset":
	default:
		return nil, false
	}
	if !hasOffset {
		return null, true
	}
	_, offset := t.Zone()
	switch name {
	case "time offset":
		return NewFEELDuration(time.Duration(offset) * time.Second), true
	case "timezone":
		if !zoneID {
			return null, true
		}
		return t.Location().String(), true
	}
	return offset, true
}

func (t FEELTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// String prints the time in its lexical form (see formatZoned): `10:00:00`,
// `10:00:00Z`, `10:00:00+02:00` or `10:00:00@Etc/UTC`, so that
// time(string(t)) is t again.
func (t FEELTime) String() string {
	return formatZoned(t.t, "15:04:05.999999999", t.hasOffset, t.zoneID)
}

type FEELDate struct {
	t time.Time
}

func (d FEELDate) Date() time.Time {
	return d.t
}

// GetAttr is the date row of DMN 1.5 §10.3.2.15 Table 65: `year`, `month`,
// `day` and `weekday` (see dateAttr).
func (d FEELDate) GetAttr(name string) (interface{}, bool) {
	return dateAttr(d.t, name)
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

// FEELDatetime is a FEEL date and time. As for FEELTime, a zoneless value
// keeps its wall clock in t with the UTC location and hasOffset is false.
//
// VERDICT DIVERGENCE: upstream read a zoneless date and time as UTC. DMN 1.5
// §10.3.2.3.6: one without an offset is a local date and time, which has no
// UTC form; see FEELTime.
//
// zoneID is as for FEELTime: t is then in the IANA location, whose rules give
// the offset at that date.
type FEELDatetime struct {
	t         time.Time
	hasOffset bool
	zoneID    bool
}

func (dt FEELDatetime) Time() time.Time {
	return dt.t
}

// HasOffset reports whether the date and time has a time offset or zone.
func (dt FEELDatetime) HasOffset() bool {
	return dt.hasOffset
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

// GetAttr is the date and time row of DMN 1.5 §10.3.2.15 Table 65: the date
// components (dateAttr), `hour`, `minute`, `second` (a decimal, as for
// FEELTime), `time offset` and `timezone` (zoneAttr).
func (dt FEELDatetime) GetAttr(name string) (interface{}, bool) {
	if v, ok := dateAttr(dt.t, name); ok {
		return v, true
	}
	switch name {
	case "hour":
		return dt.t.Hour(), true
	case "minute":
		return dt.t.Minute(), true
	case "second":
		return secondAttr(dt.t), true
	}
	return zoneAttr(dt.t, dt.hasOffset, dt.zoneID, name)
}

func (dt FEELDatetime) MarshalJSON() ([]byte, error) {
	return json.Marshal(dt.String())
}

// String prints the date and time in its lexical form (see formatZoned):
// `2024-01-01T10:00:00`, `...Z`, `...+05:00` or `...@Europe/Paris`, so that
// date and time(string(dt)) is dt again.
//
// VERDICT DIVERGENCE: upstream printed every offset through Go's `MST`
// layout, so `+05:00` printed as `@+0500` and `Z` as `@UTC`, neither of which
// is DMN's lexical form.
func (dt FEELDatetime) String() string {
	return formatZoned(dt.t, "2006-01-02T15:04:05.999999999", dt.hasOffset, dt.zoneID)
}

// Add is date and time ± duration (DMN 1.5 §10.3.2.15, Table 57); a negative
// duration subtracts. A years and months duration moves the calendar month and
// keeps the time of day and the location. Upstream built the result with
// time.Date, which normalises an out-of-range day forwards, so 2026-01-31 +
// P1M was 2026-03-03. The day is now pinned to the last day of the resulting
// month (2026-02-28), as XPath op:add-yearMonthDuration-to-dateTime does by the
// XSD 1.1 Part 2 Appendix E algorithm §9.4 names; see NOTICE.md. A days and
// time duration adds its exact length. Either way the result keeps the
// operand's offset, or its lack of one (Table 57 builds it from e1).
func (dt *FEELDatetime) Add(dur *FEELDuration) *FEELDatetime {
	if dur.isYearsMonths() {
		months := dur.signedMonths()
		total := int64(dt.t.Year())*12 + int64(dt.t.Month()-1) + months
		year, month := floorDiv(total, 12), total-floorDiv(total, 12)*12
		day := dt.t.Day()
		if last := daysIn(int(year), time.Month(month+1)); day > last {
			day = last
		}
		return &FEELDatetime{
			t: time.Date(
				int(year), time.Month(month+1), day,
				dt.t.Hour(), dt.t.Minute(), dt.t.Second(), dt.t.Nanosecond(),
				dt.t.Location()),
			hasOffset: dt.hasOffset,
			zoneID:    dt.zoneID,
		}
	}
	return &FEELDatetime{t: dt.t.Add(dur.Duration()), hasOffset: dt.hasOffset, zoneID: dt.zoneID}
}

// floorDiv is a / b rounded towards negative infinity, for b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b < 0 {
		q--
	}
	return q
}

// daysIn is the number of days in the month of the year.
func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// subInstants is the days and time duration a - b, the valuedtd⁻¹ of the
// difference of two instants (DMN 1.5 §10.3.2.15, Table 57). A difference
// beyond time.Duration's range (about 292 years) raises ErrDurationRange:
// upstream's time.Time.Sub saturated it to the range's bound without a word.
func subInstants(a, b time.Time) (*FEELDuration, error) {
	delta := a.Sub(b)
	if !b.Add(delta).Equal(a) {
		return nil, ErrDurationRange
	}
	return NewFEELDuration(delta), nil
}

// VERDICT DIVERGENCE: Z07:00 and no `@MST`, as in timePatterns.
var dateTimePatterns = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05Z07:00",
}

// ParseDatetime parses a date and time string. An IANA zone id is resolved
// by its rules at that date and time, so `2026-01-01T10:00:00@Europe/Paris`
// is the instant `2026-01-01T09:00:00Z` (DMN 1.5 §10.3.2.3.6).
func ParseDatetime(temporalStr string) (*FEELDatetime, error) {
	t, zone, err := parseZoned(temporalStr, dateTimePatterns, "2006-01-02T15:04:05")
	if err != nil {
		return nil, err
	}
	return &FEELDatetime{t: t, hasOffset: zone != zoneNone, zoneID: zone == zoneID}, nil
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
	// Nanoseconds is the fraction of a second, 0 to 999999999.
	//
	// VERDICT DIVERGENCE: upstream held whole seconds only, so PT1.5S could
	// not be represented. A days and time duration's seconds are an
	// xs:decimal; the fork keeps them to the nanosecond, the resolution of
	// time.Duration and of every date and time it is added to.
	Nanoseconds int

	// yearsMonths records the duration's kind: true for a years and months
	// duration, false for a days and time one.
	//
	// VERDICT DIVERGENCE: upstream inferred the kind from the fields, so a
	// zero years and months duration (P1Y - P1Y) was indistinguishable from
	// PT0S and printed as such. DMN 1.5 §10.3.2.3.7 and §10.3.2.3.8 make the
	// two kinds distinct types, zero included; NewYearsMonthsDuration sets it.
	yearsMonths bool
}

// NewFEELDuration carries a negative duration's sign in Neg and keeps every
// field non-negative, the form the parser produces for "-PT5H". Upstream left
// the sign in the fields (Hours: -5, Neg: false), so a negative Go duration or
// a negative date-and-time difference printed as "PT-5H" and survived abs and
// negation still negative.
//
// The result is normalised (DMN 1.5 §10.3.2.3.7): days, hours under 24,
// minutes and seconds under 60, and the sub-second remainder, which upstream
// dropped. A zero duration is never negative.
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
	d.Nanoseconds = remain - nsecs*int(time.Second)
	return d
}

// NewYearsMonthsDuration is the normalised years and months duration of a
// signed month count (DMN 1.5 §10.3.2.3.8): P26M is P2Y2M, and zero months is
// P0M, a years and months duration, not PT0S.
func NewYearsMonthsDuration(months int64) *FEELDuration {
	d := &FEELDuration{yearsMonths: true}
	if months < 0 {
		d.Neg = true
		months = -months
	}
	d.Years = int(months / 12)
	d.Months = int(months % 12)
	return d
}

// ErrDurationRange reports a duration string in the DMN lexical form whose
// value the fork cannot hold: a days and time duration longer than
// time.Duration's range (about 292 years), or a years and months duration
// over the int64 range of months.
var ErrDurationRange = errors.New("duration exceeds the supported range")

// ErrDurationPrecision reports a days and time duration string in the DMN
// lexical form whose seconds are finer than a nanosecond.
var ErrDurationPrecision = errors.New("duration seconds finer than a nanosecond are not supported")

// addDurations is duration + duration (DMN 1.5 §10.3.2.15, Table 57): two
// years and months durations add their months, two days and time durations
// their lengths. Any other mixture is outside the operator's domain: ok is
// false. That includes a zero of the other kind: P0M + PT1H is null, as
// P1M + PT1H is, since a duration keeps its kind when it is zero.
func addDurations(a, b *FEELDuration) (sum *FEELDuration, ok bool, err error) {
	if a.isYearsMonths() != b.isYearsMonths() {
		return nil, false, nil
	}
	if a.isYearsMonths() {
		return NewYearsMonthsDuration(a.signedMonths() + b.signedMonths()), true, nil
	}
	x, y := a.Duration(), b.Duration()
	s := x + y
	if (x > 0 && y > 0 && s < 0) || (x < 0 && y < 0 && s >= 0) {
		return nil, true, ErrDurationRange
	}
	return NewFEELDuration(s), true, nil
}

func (duration FEELDuration) isZero() bool {
	return duration.Years == 0 && duration.Months == 0 && duration.Days == 0 &&
		duration.Hours == 0 && duration.Minutes == 0 && duration.Seconds == 0 &&
		duration.Nanoseconds == 0
}

func (duration FEELDuration) signedMonths() int64 {
	m := int64(duration.Years)*12 + int64(duration.Months)
	if duration.Neg {
		m = -m
	}
	return m
}

// secondsString is the seconds component as an exact decimal, without
// trailing zeros: "1.5", "0.25", "3".
func (duration FEELDuration) secondsString() string {
	if duration.Nanoseconds == 0 {
		return strconv.Itoa(duration.Seconds)
	}
	frac := strings.TrimRight(fmt.Sprintf("%09d", duration.Nanoseconds), "0")
	return fmt.Sprintf("%d.%s", duration.Seconds, frac)
}

// GetAttr is the two duration rows of DMN 1.5 §10.3.2.15 Table 65: `years`
// and `months` for a years and months duration, `days`, `hours`, `minutes` and
// `seconds` for a days and time one, each the normalised component.
//
// VERDICT DIVERGENCE: a property of the other kind is null, as Table 66 says
// of each ("returns null when invoked on a days and time duration value"),
// where upstream answered 0. It is not found either, since Table 65 does not
// list it for that kind, so `is defined(duration("P1D").years)` is false. **Interpretation:** a component carries the
// duration's sign (`duration("-P1Y2M").years` is -1, `.months` -2), as XPath
// F&O 3.1 §8.2 fn:years-from-duration and its siblings do; Table 66's
// intervals are silent on sign, and upstream returned the magnitude.
func (duration FEELDuration) GetAttr(name string) (interface{}, bool) {
	var v int
	switch name {
	case "years", "months":
		if !duration.isYearsMonths() {
			return nil, false
		}
		v = duration.Years
		if name == "months" {
			v = duration.Months
		}
	case "days", "hours", "minutes", "seconds":
		if duration.isYearsMonths() {
			return nil, false
		}
		switch name {
		case "days":
			v = duration.Days
		case "hours":
			v = duration.Hours
		case "minutes":
			v = duration.Minutes
		default:
			if duration.Nanoseconds != 0 {
				sign := ""
				if duration.Neg {
					sign = "-"
				}
				return NewNumber(sign + duration.secondsString()), true
			}
			v = duration.Seconds
		}
	default:
		return nil, false
	}
	if duration.Neg {
		v = -v
	}
	return v, true
}

func (duration FEELDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(duration.String())
}

func (duration FEELDuration) Duration() time.Duration {
	// duration.Year and duration.Month
	dv := time.Duration(duration.Days*24+duration.Hours)*time.Hour +
		time.Duration(duration.Minutes)*time.Minute +
		time.Duration(duration.Seconds)*time.Second +
		time.Duration(duration.Nanoseconds)
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
	if duration.Neg && !duration.isZero() {
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
	if duration.Seconds != 0 || duration.Nanoseconds != 0 {
		sSecond = duration.secondsString() + "S"
	}
	if duration.isYearsMonths() {
		// A zero years and months duration is P0M, the canonical
		// xs:yearMonthDuration zero, never PT0S.
		if sYear == "" && sMonth == "" {
			sMonth = "0M"
		}
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
// rather than a days-and-time one. The kind is the explicit yearsMonths flag;
// a value built with Years or Months set but not the flag (a struct literal)
// still reads as years and months.
func (duration FEELDuration) isYearsMonths() bool {
	return duration.yearsMonths || duration.Years != 0 || duration.Months != 0
}

// IsYearsMonths reports whether the duration is a years and months duration
// (DMN 1.5 §10.3.2.3.8) rather than a days and time one (§10.3.2.3.7). A zero
// keeps the kind it was built with.
func (duration FEELDuration) IsYearsMonths() bool { return duration.isYearsMonths() }

// scaleDuration is duration * number, number * duration and duration / number
// (DMN 1.5 §10.3.2.15, Table 59). The result keeps the duration's kind.
//
// VERDICT DIVERGENCE: upstream had no duration case in `*` or `/`, so every
// scaling was null. The arithmetic is exact decimal: the duration's length
// (months for a years and months duration, nanoseconds for a days and time
// one) is multiplied or divided by the number as a rational, then truncated
// toward zero to the kind's unit, since neither kind can hold a fraction of
// it: P1M / 3 is P0M, P1Y * 1.5 is P1Y6M, PT1S / 3 is PT0.333333333S and
// -P1M / 3 is P0M. The spec is silent on the fractional remainder; truncation
// matches xs:yearMonthDuration's integer months and keeps both kinds alike.
// A zero divisor is null (ok=false), as for numbers (Table 59: division by
// zero is null). A result outside the kind's range is ErrDurationRange.
func scaleDuration(d *FEELDuration, factor *Number, divide bool) (res *FEELDuration, ok bool, err error) {
	f := factor.rat()
	if divide {
		if f.Sign() == 0 {
			return nil, false, nil
		}
		f.Inv(f)
	}
	var length *big.Rat
	if d.isYearsMonths() {
		length = new(big.Rat).SetInt64(d.signedMonths())
	} else {
		length = new(big.Rat).SetInt64(int64(d.Duration()))
	}
	length.Mul(length, f)
	q := new(big.Int).Quo(length.Num(), length.Denom())
	if !q.IsInt64() {
		return nil, true, ErrDurationRange
	}
	if d.isYearsMonths() {
		return NewYearsMonthsDuration(q.Int64()), true, nil
	}
	return NewFEELDuration(time.Duration(q.Int64())), true, nil
}

// divideDurations is duration / duration (DMN 1.5 §10.3.2.15, Table 59): for
// two years and months durations valueymd(e1) / valueymd(e2), the ratio of
// their total months; for two days and time durations valuedtd(e1) /
// valuedtd(e2), the ratio of their lengths. The result is a number. ok is false
// — the operator's null — when the divisor is zero or the kinds differ: Table
// 59 has no cell for a mixed pair, and one kind is never coerced to the other.
//
// VERDICT DIVERGENCE: upstream had no duration case in `/`.
func divideDurations(a, b *FEELDuration) (*Number, bool) {
	if a.isYearsMonths() != b.isYearsMonths() {
		return nil, false
	}
	var x, y int64
	if a.isYearsMonths() {
		x, y = a.signedMonths(), b.signedMonths()
	} else {
		x, y = int64(a.Duration()), int64(b.Duration())
	}
	if y == 0 {
		return nil, false
	}
	return NewNumberFromInt64(x).Div(NewNumberFromInt64(y)), true
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

// AddDuration is date + duration. DMN 1.5 §10.3.2.15 (Table 57, date with a
// days and time or a years and months duration) evaluates it as the date of
// date and time(date, midnight) + duration, so a days-and-time duration moves
// whole days and drops any remainder of a day, and a years-and-months duration
// moves calendar months, pinning the day to the end of a shorter month.
func (d *FEELDate) AddDuration(dur *FEELDuration) *FEELDate {
	dt := (&FEELDatetime{t: d.t}).Add(dur).t
	return &FEELDate{t: time.Date(dt.Year(), dt.Month(), dt.Day(), 0, 0, 0, 0, d.t.Location())}
}

// utcMidnight is the date as a date and time at UTC midnight, the implicit
// conversion DMN 1.5 §10.3.2.3.5 applies to a date on either side of a
// subtraction of dates and date and times (§10.3.2.15, Table 57). The
// calendar date is kept whatever location the date was built in, so
// today() - a parsed date counts whole days.
func (d *FEELDate) utcMidnight() time.Time {
	return time.Date(d.t.Year(), d.t.Month(), d.t.Day(), 0, 0, 0, 0, time.UTC)
}

// AddDuration is time ± days and time duration: valuet⁻¹(valuet(t) +
// valuedtd(dur)) (DMN 1.5 §10.3.2.15, Table 57), where valuet⁻¹ applies its
// argument modulo 86400 seconds (§10.3.2.3.4), so the result wraps within the
// day — 23:00:00 + PT2H is 01:00:00 — and keeps the time's offset, or its
// lack of one. The caller
// rules out a years and months duration, which Table 57 does not pair with a
// time.
func (tm *FEELTime) AddDuration(dur *FEELDuration) *FEELTime {
	const day = int64(24 * time.Hour)
	t := tm.t
	sinceMidnight := int64(t.Hour())*int64(time.Hour) + int64(t.Minute())*int64(time.Minute) +
		int64(t.Second())*int64(time.Second) + int64(t.Nanosecond())
	x := (sinceMidnight + int64(dur.Duration())%day) % day
	if x < 0 {
		x += day
	}
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return &FEELTime{t: midnight.Add(time.Duration(x)), hasOffset: tm.hasOffset, zoneID: tm.zoneID}
}

var yearmonthDurationPattern = regexp.MustCompile(`^(\-?)P((\d+)Y)?((\d+)M)?$`)

// timeDurationPattern is the xs:dayTimeDuration lexical form, PnDTnHnMnS.
//
// Upstream required the T designator, so a duration with only a day part
// (P1D, -P2D) failed to parse; and it accepted a bare T (PT). xs:duration makes
// T present exactly when an hour, minute or second follows it, and requires at
// least one component overall. Both rules are checked in ParseDuration, since
// Go's regexp has no lookahead.
//
// The seconds are an unsigned decimal (XSD 1.1 duSecondFrag): 1, 1.5, 1. or
// .5. Upstream admitted whole seconds only.
var timeDurationPattern = regexp.MustCompile(`^(\-?)P((\d+)D)?(T((\d+)H)?((\d+)M)?(((\d+)(\.(\d*))?|\.(\d+))S)?)?$`)

// ParseDuration parses a days and time or a years and months duration string
// into its normalised value.
//
// VERDICT DIVERGENCE: upstream capped each component (months at 12, hours at
// 24, minutes and seconds at 60) and raised over the cap, though
// xs:yearMonthDuration and xs:dayTimeDuration bound no component: P26M,
// PT36H and PT90M are valid. DMN 1.5 §10.3.2.3.7 and §10.3.2.3.8 normalise a
// duration so the sum of its components is minimised, so P26M is P2Y2M,
// PT36H is P1DT12H and P0DT25H is P1DT1H.
func ParseDuration(temporalStr string) (*FEELDuration, error) {
	// parse year month duration
	if submatches := yearmonthDurationPattern.FindStringSubmatch(temporalStr); submatches != nil {
		if submatches[2] == "" && submatches[4] == "" {
			// VERDICT DIVERGENCE: the bare P (and -P) has no component, which
			// xs:yearMonthDuration and xs:duration both require; upstream read
			// it as a zero duration.
			return nil, ErrParseTemporal
		}
		total := new(big.Int)
		if submatches[2] != "" {
			total.SetString(submatches[3], 10)
			total.Mul(total, big.NewInt(12))
		}
		if submatches[4] != "" {
			m, _ := new(big.Int).SetString(submatches[5], 10)
			total.Add(total, m)
		}
		if !total.IsInt64() {
			return nil, ErrDurationRange
		}
		months := total.Int64()
		if submatches[1] != "" {
			months = -months
		}
		return NewYearsMonthsDuration(months), nil
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
		// The total length in nanoseconds, accumulated exactly so that no
		// component, however large, wraps around.
		total := new(big.Int)
		component := func(digits string, unit int64) {
			if digits == "" {
				return
			}
			v, _ := new(big.Int).SetString(digits, 10)
			total.Add(total, v.Mul(v, big.NewInt(unit)))
		}
		component(submatches[3], int64(24*time.Hour))
		component(submatches[6], int64(time.Hour))
		component(submatches[8], int64(time.Minute))
		if submatches[9] != "" {
			// Whole seconds (11) with an optional fraction (13), or a bare
			// fraction (14).
			component(submatches[11], int64(time.Second))
			frac := submatches[13] + submatches[14]
			if len(frac) > 9 {
				if strings.TrimRight(frac[9:], "0") != "" {
					return nil, ErrDurationPrecision
				}
				frac = frac[:9]
			}
			if frac != "" {
				component(frac+strings.Repeat("0", 9-len(frac)), 1)
			}
		}
		if !total.IsInt64() {
			return nil, ErrDurationRange
		}
		ns := time.Duration(total.Int64())
		if submatches[1] != "" {
			ns = -ns
		}
		return NewFEELDuration(ns), nil
	}
	return nil, ErrParseTemporal
}

func ParseTemporalValue(temporalStr string) (interface{}, error) {
	v, dtErr := ParseDatetime(temporalStr)
	if dtErr == nil {
		return v, nil
	}

	t, tErr := ParseTime(temporalStr)
	if tErr == nil {
		return t, nil
	}
	// VERDICT DIVERGENCE: name the unsupported zone id, or the zone id a
	// time cannot take, rather than the generic parse failure the duration
	// fallback would report.
	for _, err := range []error{dtErr, tErr} {
		if errors.Is(err, ErrUnsupportedTimeZone) || errors.Is(err, ErrTimeZoneIDOnTime) {
			return nil, err
		}
	}

	if v, err := ParseDate(temporalStr); err == nil {
		return v, nil
	}

	return ParseDuration(temporalStr)
}

// fromString is a string → value conversion that yields null outside its
// domain (DMN 1.5 §10.3.4, Table 72): a string the parser rejects as
// malformed (ErrParseTemporal). Inputs inside the domain that the fork falls
// short on still raise, so they never read as malformed: a parser error other
// than ErrParseTemporal (an IANA zone id, a duration beyond the fork's range
// or precision).
func fromString[T any](parse func(string) (T, error), s string) (any, error) {
	v, err := parse(s)
	if errors.Is(err, ErrParseTemporal) {
		return null, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// stringConversion builds a one-argument string → value conversion built-in;
// anything but a string, null included, is outside its domain and null.
func stringConversion[T any](parse func(string) (T, error)) *NativeFun {
	return NewNativeFunc(func(kwargs map[string]any) (any, error) {
		if s, ok := kwargs["from"].(string); ok {
			return fromString(parse, s)
		}
		return null, nil
	})
}

// ErrTimePrecision reports a time() second finer than a nanosecond: inside
// the built-in's domain, beyond what the fork can hold.
var ErrTimePrecision = errors.New("time seconds finer than a nanosecond are not supported")

// intComponent reads a date or time component: a number with an integer
// value that fits an int. Anything else is outside the domain.
func intComponent(v any) (int, bool) {
	n, ok := v.(*Number)
	if !ok || !n.v.IsInt() {
		return 0, false
	}
	i, acc := n.v.Int64()
	if acc != big.Exact || i < math.MinInt32 || i > math.MaxInt32 {
		return 0, false
	}
	return int(i), true
}

func asDatetime(v any) (*FEELDatetime, bool) {
	switch dt := v.(type) {
	case *FEELDatetime:
		return dt, true
	case FEELDatetime:
		return &dt, true
	}
	return nil, false
}

func asTime(v any) (*FEELTime, bool) {
	switch tm := v.(type) {
	case *FEELTime:
		return tm, true
	case FEELTime:
		return &tm, true
	}
	return nil, false
}

// dateBuiltin is date(from) and date(year, month, day), DMN 1.5 §10.3.4.1
// Table 72. from is a date string or a date and time, whose calendar date
// (in its own offset or zone) is the result. The components must be integers
// naming a real date — month 13 or 30 February is null.
func dateBuiltin(kwargs map[string]any) (any, error) {
	if from, ok := kwargs["from"]; ok {
		if v, ok := from.(string); ok {
			return fromString(ParseDate, v)
		}
		if dt, ok := asDatetime(from); ok {
			return &FEELDate{t: time.Date(dt.t.Year(), dt.t.Month(), dt.t.Day(), 0, 0, 0, 0, time.UTC)}, nil
		}
		return null, nil
	}
	y, okY := intComponent(kwargs["year"])
	m, okM := intComponent(kwargs["month"])
	d, okD := intComponent(kwargs["day"])
	if !okY || !okM || !okD || y < -999999999 || y > 999999999 {
		return null, nil
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return null, nil
	}
	return &FEELDate{t: t}, nil
}

// timeBuiltin is time(from) and time(hour, minute, second, offset?), DMN 1.5
// §10.3.4.1 Table 72. from is a time string, a time (returned as is) or a
// date and time, whose time of day keeps its offset or its lack of one. hour
// and minute are integers in range, second a number in [0, 60), and offset a
// days and time duration within ±14 hours (xs:time's offset range) or null,
// which leaves the time zoneless.
func timeBuiltin(kwargs map[string]any) (any, error) {
	if from, ok := kwargs["from"]; ok {
		if s, ok := from.(string); ok {
			return fromString(ParseTime, s)
		}
		if tm, ok := asTime(from); ok {
			return tm, nil
		}
		if dt, ok := asDatetime(from); ok {
			return timeOfDay(dt), nil
		}
		return null, nil
	}
	h, okH := intComponent(kwargs["hour"])
	m, okM := intComponent(kwargs["minute"])
	sec, okS := kwargs["second"].(*Number)
	if !okH || !okM || !okS || h < 0 || h > 23 || m < 0 || m > 59 {
		return null, nil
	}
	nanos := new(big.Rat).Mul(sec.rat(), big.NewRat(int64(time.Second), 1))
	if nanos.Sign() < 0 || nanos.Cmp(big.NewRat(int64(time.Minute), 1)) >= 0 {
		return null, nil
	}
	if !nanos.IsInt() {
		return nil, ErrTimePrecision
	}
	ns := nanos.Num().Int64()
	loc, hasOffset := time.UTC, false
	if offset, ok := kwargs["offset"]; ok {
		if o, ok := offset.(FEELDuration); ok {
			offset = &o
		}
		switch o := offset.(type) {
		case *NullValue, nil:
		case *FEELDuration:
			d := o.Duration()
			if o.isYearsMonths() || d%time.Second != 0 || d > 14*time.Hour || d < -14*time.Hour {
				return null, nil
			}
			hasOffset = true
			if d != 0 {
				loc = time.FixedZone("", int(d/time.Second))
			}
		default:
			return null, nil
		}
	}
	t := time.Date(0, 1, 1, h, m, 0, int(ns), loc)
	return &FEELTime{t: t, hasOffset: hasOffset}, nil
}

// timeOfDay is a date and time's time of day on time's reference date
// (0000-01-01), keeping its offset or its lack of one. A zone id whose offset
// is fixed is kept; one whose offset varies by date (which a time cannot
// carry, see ErrTimeZoneIDOnTime) becomes the offset in force at that date and
// time.
func timeOfDay(dt *FEELDatetime) *FEELTime {
	loc, zoneID := dt.t.Location(), dt.zoneID
	if zoneID && !hasFixedOffset(loc) {
		_, offset := dt.t.Zone()
		loc, zoneID = time.UTC, false
		if offset != 0 {
			loc = time.FixedZone("", offset)
		}
	}
	t := time.Date(0, 1, 1, dt.t.Hour(), dt.t.Minute(), dt.t.Second(), dt.t.Nanosecond(), loc)
	return &FEELTime{t: t, hasOffset: dt.hasOffset, zoneID: zoneID}
}

// datetimeBuiltin is date and time(from) and date and time(date, time), DMN
// 1.5 §10.3.4.1 Table 72. date is a date or a date and time (whose calendar
// date is used); the result has time's time of day and its offset, zone or
// lack of one.
func datetimeBuiltin(kwargs map[string]any) (any, error) {
	if from, ok := kwargs["from"]; ok {
		if s, ok := from.(string); ok {
			return fromString(ParseDatetime, s)
		}
		return null, nil
	}
	var day time.Time
	switch d := kwargs["date"].(type) {
	case *FEELDate:
		day = d.t
	case FEELDate:
		day = d.t
	default:
		dt, ok := asDatetime(d)
		if !ok {
			return null, nil
		}
		day = dt.t
	}
	tm, ok := asTime(kwargs["time"])
	if !ok {
		return null, nil
	}
	t := time.Date(day.Year(), day.Month(), day.Day(),
		tm.t.Hour(), tm.t.Minute(), tm.t.Second(), tm.t.Nanosecond(), tm.t.Location())
	return &FEELDatetime{t: t, hasOffset: tm.hasOffset, zoneID: tm.zoneID}, nil
}

// installDatetimeFunctions builtin functions
func installDatetimeFunctions(prelude *Prelude) {
	// conversions
	//
	// VERDICT DIVERGENCE: the four conversions yield null for a string
	// outside their lexical domain, and for an argument that is no overload's
	// domain, instead of failing the evaluation. DMN 1.5 §10.3.4: "Whenever a
	// parameter is outside its domain, the result of the built-in is null."
	//
	// VERDICT DIVERGENCE: date, time and date and time carry every overload
	// of DMN 1.5 §10.3.4.1 Table 72, positionally or by the spec's parameter
	// names; upstream had only the string form.
	prelude.Bind("date", NewNativeFunc(dateBuiltin).Overloads(
		[]string{"from"}, []string{"year", "month", "day"}))
	prelude.Bind("time", NewNativeFunc(timeBuiltin).Overloads(
		[]string{"from"}, []string{"hour", "minute", "second"}, []string{"hour", "minute", "second", "offset"}))
	prelude.Bind("date and time", NewNativeFunc(datetimeBuiltin).Overloads(
		[]string{"from"}, []string{"date", "time"}))
	prelude.Bind("duration", stringConversion(ParseDuration).Required("from"))

	// temporal functions
	prelude.Bind("now", wrapTyped(func() (interface{}, error) {
		return &FEELDatetime{t: time.Now(), hasOffset: true}, nil
	}))

	// VERDICT DIVERGENCE: today() is the current date in UTC, where every
	// parsed date lives. Upstream took the date in the process's local zone
	// and kept that location, so the same model answered differently on a
	// host east or west of UTC. pkg/feel's injected clock overrides this.
	prelude.Bind("today", wrapTyped(func() (interface{}, error) {
		now := time.Now().UTC()
		return &FEELDate{t: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)}, nil
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
