// Adapted from andon internal/db/json.go (468474a).

package db

import "time"

// timeLayout is RFC 3339 in UTC with a fixed nine-digit fraction. A fixed
// width keeps the text sortable: RFC3339Nano drops trailing zeros, and then
// "…05Z" would sort after "…05.5Z", breaking ORDER BY and range queries.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// TimeStr formats t for storage in a TEXT timestamp column (B3).
func TimeStr(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// ParseTime parses a TEXT timestamp column back into UTC time.
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// NullTimeStr formats *t, or returns nil for a NULL column.
func NullTimeStr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return TimeStr(*t)
}
