package repository

import (
	"encoding/binary"
	"github.com/cockroachdb/pebble"
)

func KeyUpperBound(b []byte) []byte {
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

func PrefixIterOptions(prefix []byte) *pebble.IterOptions {
	return &pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: KeyUpperBound(prefix),
	}
}

func Uint32ToBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func Uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func ScanIds(db *pebble.DB, prefix []byte) []uint64 {
	prefixLen := len(prefix)
	iter := db.NewIter(PrefixIterOptions(prefix))
	var ids []uint64
	for iter.First(); iter.Valid(); iter.Next() {
		ids = append(ids, binary.BigEndian.Uint64(iter.Key()[prefixLen:]))
	}
	iter.Close()
	return ids
}
