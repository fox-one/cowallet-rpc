package cowallet

import (
	"bytes"

	"github.com/pandodao/mtg/mtgpack"
)

func buildIndexKey(prefix []byte, values ...any) []byte {
	enc := mtgpack.NewEncoder()
	if err := enc.EncodeValues(values...); err != nil {
		panic(err)
	}

	return append(prefix, enc.Bytes()...)
}

func decodeIndexKey(key, prefix []byte, values ...any) error {
	b := bytes.TrimPrefix(key, prefix)
	dec := mtgpack.NewDecoder(b)
	return dec.DecodeValues(values...)
}
