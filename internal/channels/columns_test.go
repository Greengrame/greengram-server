package channels

import "testing"

func topLevelFields(cols string) int {
	n, depth := 1, 0
	for _, r := range cols {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

// mCols is msgCols with a table prefix, for joins. It has been left behind twice,
// and every search then fails on scanMsg's destinations.
func TestColumnListsMatch(t *testing.T) {
	if a, b := topLevelFields(msgCols), topLevelFields(mCols); a != b {
		t.Fatalf("msgCols has %d fields, mCols %d", a, b)
	}
}
