package checker

import "strconv"

func autoIntLabel(value int, auto bool) string {
	if auto {
		return "auto"
	}
	return strconv.Itoa(value)
}
