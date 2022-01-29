package repository

import "github.com/cockroachdb/pebble"

var KeyUpperBound func(b []byte) []byte
var PrefixIterOptions func(prefix []byte) *pebble.IterOptions

func init() {
	KeyUpperBound = func(b []byte) []byte {
		end := make([]byte, len(b))
		copy(end, b)
		for i := len(end) - 1; i >= 0; i-- {
			end[i] = end[i] + 1
			if end[i] != 0 {
				return end[:i+1]
			}
		}
		return nil // no upper-bound
	}

	PrefixIterOptions = func(prefix []byte) *pebble.IterOptions {
		return &pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: KeyUpperBound(prefix),
		}
	}
}
