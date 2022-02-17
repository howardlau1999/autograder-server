package utils

import (
	"math"
	"math/rand"
	"strings"
)

var dictionary = "23456789ABCDEFGHJKLMNPQRSTUVWX"
var complement = "YZ"
var dictLen = uint64(len(dictionary))
var reverseDict map[byte]uint64

func init() {
	reverseDict = map[byte]uint64{}
	for i := uint64(0); i < dictLen; i++ {
		reverseDict[dictionary[int(i)]] = i
	}
}

func Base30Encode(id uint64) string {
	code := ""
	const codeLen = 8
	for id/dictLen != 0 {
		idx := (id % dictLen) % dictLen
		code += string(dictionary[idx])
		id = id / dictLen
	}

	idx := (id % dictLen) % dictLen
	code += string(dictionary[idx])

	if len(code) < codeLen {
		code += string(complement[rand.Intn(len(complement))])
		padding := codeLen - len(code)
		for i := 0; i < padding; i++ {
			idx := rand.Uint64() % dictLen
			code += string(dictionary[idx])
		}
	}
	return code
}

func Base30Decode(code string) uint64 {
	l := len(code)
	for i := 0; i < len(complement); i++ {
		idx := strings.IndexByte(code, complement[i])
		if idx >= 0 {
			l = idx
			break
		}
	}
	id := uint64(0)
	for i := 0; i < l; i++ {
		ch := reverseDict[code[i]]
		id += uint64(math.Pow(float64(dictLen), float64(i))) * ch
	}
	return id
}
