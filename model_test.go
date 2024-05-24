package cowallet

import (
	"encoding/hex"
	"testing"

	"github.com/fox-one/mixin-sdk-go/v2"
)

func TestMixAddress(t *testing.T) {
	members := []string{
		"f25b410b-2ab3-4fb3-bd0f-1d5d280c529d",
		"8017d200-7870-4b82-b53f-74bae1d2dad7",
	}

	addr, err := mixin.NewMixAddress(members, 1)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(addr.String())
}

func TestDecodeMixAddress(t *testing.T) {
	addr, err := mixin.MixAddressFromString("MIX2JtYcHzHeVUocQQwX3sEoH3AMNFUeqNxqzGaRDCU9drszpaa2MudF")
	if err != nil {
		t.Fatal(err)
	}

	t.Log(addr.Members(), addr.Threshold)
}

func TestDecodeExtra(t *testing.T) {
	extra := "383038302f7661756c74732f4d4958324a745a43587044325378674831426d5a3174696934776132347143767469726f5739414e4151334b6866637368436a4e67726346"
	b, err := hex.DecodeString(extra)
	if err != nil {
		t.Fatal(err)
	}

	t.Log(string(b))

	addr, err := mixin.MixAddressFromString(string(b))
	if err != nil {
		t.Fatal(err)
	}

	t.Log(addr.Members(), addr.Threshold)
}
