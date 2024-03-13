package cowallet

import (
	"github.com/fox-one/mixin-sdk-go/v2"
)

type User struct {
	MixinID string              `json:"mixin_id"`
	Key     mixin.OauthKeystore `json:"key"`
}
