package cowallet

import "time"

func maxDate(a time.Time, b ...time.Time) time.Time {
	for _, v := range b {
		if v.After(a) {
			a = v
		}
	}

	return a
}
