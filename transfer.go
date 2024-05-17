package cowallet

import (
	"context"
	"encoding/hex"

	"github.com/fox-one/mixin-sdk-go/v2"
)

func (s *Server) handleOutput(ctx context.Context, output *mixin.SafeUtxo) error {
	if output.OutputIndex > 0 {
		return nil
	}

	b, _ := hex.DecodeString(output.Extra)

}
