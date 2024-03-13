package cowallet

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/yiplee/go-cache"
	"golang.org/x/sync/singleflight"
)

func extractBearerToken(r *http.Request) string {
	token := r.Header.Get("Authorization")
	return strings.TrimPrefix(token, "Bearer ")
}

func handleAuth() func(next http.Handler) http.Handler {
	var (
		users = cache.New[string, *User]()
		sf    singleflight.Group
	)

	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			token := extractBearerToken(r)

			var key mixin.OauthKeystore
			if err := json.NewDecoder(strings.NewReader(token)).Decode(&key); err != nil {
				next.ServeHTTP(w, r)
				return
			}

			client, err := mixin.NewFromOauthKeystore(&key)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			user, err, _ := sf.Do(token, func() (interface{}, error) {
				if u, ok := users.Get(token); ok {
					return u, nil
				}

				u, err := client.UserMe(ctx)
				if err != nil {
					return nil, err
				}

				user := &User{
					MixinID: u.UserID,
					Key:     key,
				}

				users.Set(token, user)
				return user, nil
			})

			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUser(ctx, user.(*User))))
		}

		return http.HandlerFunc(fn)
	}
}
