package cowallet

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

func clientFromToken(token string) (*mixin.Client, error) {
	r := strings.NewReader(token)

	var auth mixin.OauthKeystore
	if err := json.NewDecoder(r).Decode(&auth); err == nil && auth.AuthID != "" {
		return mixin.NewFromOauthKeystore(&auth)
	}

	r.Reset(token)
	var key mixin.Keystore
	if err := json.NewDecoder(r).Decode(&key); err == nil {
		return mixin.NewFromKeystore(&key)
	}

	return nil, fmt.Errorf("decode token failed")
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

			client, err := clientFromToken(token)
			if err != nil {
				slog.Error("clientFromToken", "err", err)
				next.ServeHTTP(w, r)
				return
			}

			defer func() {
				if err := recover(); err != nil {
					slog.Error("panic", "err", err)
					next.ServeHTTP(w, r)
				}
			}()

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
					Token:   token,
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
