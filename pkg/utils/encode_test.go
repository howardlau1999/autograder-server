package utils

import (
	"testing"
)

func TestBase30(t *testing.T) {
	id := uint64(20)
	encoded := Base30Encode(id)
	t.Log(encoded)
	decoded := Base30Decode(encoded)
	if decoded != id {
		t.Errorf("%d != %d", decoded, id)
	}
}
