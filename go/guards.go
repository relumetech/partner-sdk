package relume

// Value checks for signed calls and callback results. Relume validates in
// JavaScript, so lengths count UTF-16 code units and numbers must be safe
// integers, as they are there.

import (
	"encoding/json"
	"math"
	"net/url"
	"regexp"
	"strconv"
)

const maxSafeInteger = 1<<53 - 1

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func isString(value any, min, max int) (string, bool) {
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	n := utf16Len(s)
	return s, n >= min && n <= max
}

// isWhole reads a json.Number (the decoder uses UseNumber) as a safe
// integer of at least min. 80000.0 counts, as it does in JavaScript.
func isWhole(value any, min int) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(number), 64)
	// math.MaxInt bounds it on 32-bit platforms.
	if err != nil || f != math.Trunc(f) || math.Abs(f) > maxSafeInteger || f > math.MaxInt || f < float64(min) {
		return 0, false
	}
	return int(f), true
}

func isBalance(balance int) bool {
	return balance >= 0 && int64(balance) <= maxSafeInteger
}

// Zod 4's default email pattern, which Relume validates user.get with
// (Relume backend: services/partners/partner-calls.ts). Change both together.
var emailPattern = regexp.MustCompile(`^(?:[A-Za-z0-9_'+\-]+\.)*[A-Za-z0-9_'+\-]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9\-]*\.)+[A-Za-z]{2,}$`)

func isEmail(value string) bool {
	return emailPattern.MatchString(value)
}

func isHTTPSURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && (u.Host != "" || u.Opaque != "")
}
