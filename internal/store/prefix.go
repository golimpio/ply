package store

// Byte-prefix lookups. Paths keep the raw bytes the OS reports (never
// normalised), so "starts with" is a byte comparison, not a character one: the
// substr(col, 1, n) = prefix idiom counts characters, and Go's rune count and
// SQLite's can disagree on malformed UTF-8 (SQLite decodes an overlong or
// surrogate sequence as one character; Go counts its bytes). A byte range
// [prefix, successor(prefix)) is exact, and seeks an index where substr scans.

// belowCond is the condition that col is a path inside dir: everything from
// dir || '/' up to dir || '0' ('/' + 1), which seeks an index on col. dirExpr is
// a SQL expression for the directory -- a bind placeholder, or a column of the
// outer query -- so one condition serves a bound directory and a correlated one.
func belowCond(col, dirExpr string) string {
	return col + " >= " + dirExpr + " || '/' AND " + col + " < " + dirExpr + " || '0'"
}

// prefixCond is the condition that col starts with prefix, as the byte range
// [prefix, prefix with its last byte incremented), with its arguments. The
// upper bound is dropped when nothing is above the prefix (all 0xFF bytes, or
// empty).
func prefixCond(col, prefix string) (string, []any) {
	hi := prefixSuccessor(prefix)
	if hi == "" {
		return col + " >= ?", []any{prefix}
	}
	return col + " >= ? AND " + col + " < ?", []any{prefix, hi}
}

// prefixSuccessor is the shortest string above every string that starts with
// prefix: prefix with its last byte incremented, carrying past 0xFF bytes.
func prefixSuccessor(prefix string) string {
	b := []byte(prefix)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xFF {
			b[i]++
			return string(b[:i+1])
		}
	}
	return ""
}