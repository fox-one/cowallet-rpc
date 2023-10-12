package cowallet

import (
	"net/http"
	"strings"

	"github.com/fox-one/mixin-sdk-go"
	"github.com/golang-jwt/jwt"
	"github.com/twitchtv/twirp"
	"github.com/yiplee/go-cache"
	"golang.org/x/sync/singleflight"
)

func extractBearerToken(r *http.Request) string {
	token := r.Header.Get("Authorization")
	return strings.TrimPrefix(token, "Bearer ")
}

func handleAuth(issuer string) func(next http.Handler) http.Handler {
	var (
		users = cache.New[string, *User]()
		sf    singleflight.Group
	)

	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			token := extractBearerToken(r)

			var claim jwt.StandardClaims
			_, _ = jwt.ParseWithClaims(token, &claim, nil)

			if err := claim.Valid(); err != nil {
				_ = twirp.WriteError(w, twirp.Unauthenticated.Error(err.Error()))
				return
			}

			if claim.Issuer != issuer {
				_ = twirp.WriteError(w, twirp.NewError(twirp.Unauthenticated, "auth required"))
				return
			}

			user, err, _ := sf.Do(token, func() (interface{}, error) {
				if u, ok := users.Get(token); ok {
					return u, nil
				}

				u, err := mixin.UserMe(ctx, token)
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
