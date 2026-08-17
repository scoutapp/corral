package dindcache

import (
	"strconv"
	"strings"
)

// parseHumanSize converts docker's human-readable size strings ("1.5GB",
// "234.5MB", "0B", "12kB") into bytes. It is deliberately lenient: any
// unparseable input yields 0 (the size is advisory UI only). docker uses SI-ish
// units (kB=1000) in `system df`; we match that so the number lines up with what
// docker itself prints.
func parseHumanSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	// Split the trailing unit from the leading number.
	i := 0
	for i < len(s) && (s[i] == '.' || s[i] == '-' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	numStr, unit := s[:i], strings.TrimSpace(s[i:])
	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	var mult float64
	switch strings.ToUpper(unit) {
	case "B", "":
		mult = 1
	case "KB":
		mult = 1e3
	case "MB":
		mult = 1e6
	case "GB":
		mult = 1e9
	case "TB":
		mult = 1e12
	default:
		return 0
	}
	return int64(num * mult)
}
