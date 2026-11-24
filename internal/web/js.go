package web

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// obj is a decoded JSON request body. Its methods coerce values the way the JS server did
// (String(b.x || ”), Number(b.x), !!b.x), so the same inputs are accepted and rejected.
type obj map[string]any

func (b obj) str(k string) string { return b.strOr(k, "") } // String(b.k || '')

func (b obj) strOr(k, def string) string { // String(b.k || def)
	if truthy(b[k]) {
		return jsString(b[k])
	}
	return def
}

// String is JS String(b.k): "undefined" for a missing key.
func (b obj) String(k string) string {
	v, ok := b[k]
	if !ok {
		return "undefined"
	}
	return jsString(v)
}

func (b obj) truthy(k string) bool { return truthy(b[k]) }

func (b obj) num(k string) float64 { // Number(b.k)
	v, ok := b[k]
	if !ok {
		return math.NaN()
	}
	return jsNumber(v)
}

func (b obj) obj(k string) obj { m, _ := b[k].(map[string]any); return m }

func (b obj) has(k string) bool { _, ok := b[k]; return ok }

// toObj turns a struct into its JSON object form, for JS-style {...view(old), ...body} merges.
func toObj(v any) obj {
	data, _ := json.Marshal(v)
	m := obj{}
	_ = json.Unmarshal(data, &m)
	return m
}

func merge(base, over obj) obj {
	for k, v := range over {
		base[k] = v
	}
	return base
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case int64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return jsNumString(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			if e != nil {
				parts[i] = jsString(e)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

func jsNumString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case math.Abs(f) < 1e21 && (f == 0 || math.Abs(f) >= 1e-6):
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case int64:
		return float64(x)
	case string:
		switch s := strings.TrimSpace(x); s {
		case "":
			return 0
		case "Infinity", "+Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		default:
			if strings.ContainsAny(s, "_xXpPiInN") { // Go-only syntaxes (digit separators, hex floats, inf, nan)
				return math.NaN()
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil && !errors.Is(err, strconv.ErrRange) {
				return math.NaN()
			}
			return f
		}
	case []any:
		return jsNumber(jsString(x))
	}
	return math.NaN()
}

func isInteger(f float64) bool { return !math.IsInf(f, 0) && f == math.Trunc(f) }

// jsInt is n when Number.isInteger(n) holds (and it fits), else 0, which every int check downstream rejects.
func jsInt(f float64) int {
	if !isInteger(f) || math.Abs(f) > math.MaxInt32 {
		return 0
	}
	return int(f)
}

// jsLen is the JS string length (UTF-16 code units).
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// jsSlice is s.slice(0, n), cut at a rune boundary.
func jsSlice(s string, n int) string {
	if jsLen(s) <= n {
		return s
	}
	out, l := []rune{}, 0
	for _, r := range s {
		if l += utf16.RuneLen(r); l > n {
			break
		}
		out = append(out, r)
	}
	return string(out)
}
