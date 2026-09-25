package reported

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Tolerance is how far two figures may differ and still be the same figure.
//
// A figure is equal within Abs, or within Rel of the larger magnitude,
// whichever is looser. Abs is what keeps a zero from differing from
// 1e-17 by an infinite relative amount; Rel is what keeps a revenue of four
// hundred million from differing from itself by a float's last bit.
type Tolerance struct {
	Abs float64
	Rel float64
}

// DefaultTolerance is a billionth, both ways.
//
// The same ratio arrives from PostgreSQL as twenty-digit NUMERIC text and from
// SQLite as a float64, and the two disagree around the sixteenth significant
// digit. No KPI anybody reports means anything at the ninth, so a billionth
// is loose enough never to call rounding a change and tight enough never to
// call a change rounding.
var DefaultTolerance = Tolerance{Abs: 1e-9, Rel: 1e-9}

// number reads a cell as an exact rational, if it is a number at all.
//
// Postgres NUMERIC arrives as a string, and so does anything a driver does not
// map; a decoded report holds json.Number. Comparing any of them as text would
// call "1.50" and "1.5" different figures, and comparing them as float64 would
// throw away digits before the subtraction that asks whether they moved. So
// the value is read as a rational and the subtraction is exact; only the
// tolerance test is done in floating point.
//
// A string is only a number if it looks like a decimal. big.Rat also accepts
// "1/3", and a label spelled that way is a label.
func number(v any) (*big.Rat, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return nil, false
		}
		// The shortest decimal that reads back as this float, not the
		// float's exact binary value: that decimal is what encoding/json
		// wrote into the report, so 0.1 today and 0.1 on the 14th are the
		// same rational rather than two that differ in the fiftieth digit.
		return decimal(strconv.FormatFloat(t, 'g', -1, 64))
	case float32:
		return number(float64(t))
	case int:
		return new(big.Rat).SetInt64(int64(t)), true
	case int32:
		return new(big.Rat).SetInt64(int64(t)), true
	case int64:
		return new(big.Rat).SetInt64(t), true
	case uint64:
		return new(big.Rat).SetUint64(t), true
	case json.Number:
		return decimal(string(t))
	case string:
		return decimal(t)
	case []byte:
		return decimal(string(t))
	}
	return nil, false
}

func decimal(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, "/_") {
		return nil, false
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// same compares two metric cells. For two numbers it also returns now - then.
func (tol Tolerance) same(then, now any) (equal bool, delta *float64) {
	a, aok := number(then)
	b, bok := number(now)
	if aok && bok {
		d, _ := new(big.Rat).Sub(b, a).Float64()
		af, _ := a.Float64()
		bf, _ := b.Float64()
		scale := math.Max(math.Abs(af), math.Abs(bf))
		return math.Abs(d) <= tol.Abs || math.Abs(d) <= tol.Rel*scale, &d
	}
	return text(then) == text(now), nil
}

// text is one cell as a key: the same value spelled the same way whether it
// came from a driver today or from a report decoded from JSON.
//
// Numbers go through their rational so that int64(5), json.Number("5") and
// "5.0" key alike. Times go through RFC 3339 with nanoseconds, which is
// exactly how encoding/json wrote them into the report. A nil is spelled so
// that it cannot collide with an empty string: a group whose dimension is
// NULL and one whose dimension is ” are different groups.
func text(v any) string {
	switch t := v.(type) {
	case nil:
		return "\x00null"
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case []byte:
		return string(t)
	case string:
		return t
	}
	if r, ok := number(v); ok {
		return r.RatString()
	}
	return fmt.Sprint(v)
}
