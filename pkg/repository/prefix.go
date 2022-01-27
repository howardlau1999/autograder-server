package repository

import "github.com/cockroachdb/pebble"

var keyUpperBound func(b []byte) []byte
var prefixIterOptions func(prefix []byte) *pebble.IterOptions

func init() {
	keyUpperBound = func(b []byte) []byte {
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

	prefixIterOptions = func(prefix []byte) *pebble.IterOptions {
		return &pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: keyUpperBound(prefix),
		}
	}
}
