package cowallet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/yiplee/go-cache"
	"golang.org/x/sync/singleflight"
)

var (
	userCacheTTL    = 15 * time.Minute
	invalidTokenTTL = 24 * time.Hour
	errAuthPanic    = errors.New("auth panic")
)

var fetchUserMe = func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
	return client.UserMe(ctx)
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func isPermanentAuthError(err error) bool {
	return mixin.IsErrorCodes(err, mixin.Unauthorized) ||
		errors.Is(err, errAuthPanic)
}

func isForbiddenError(err error) bool {
	return mixin.IsErrorCodes(err, http.StatusForbidden)
}

func extractBearerToken(r *http.Request) string {
	token := r.Header.Get("Authorization")
	return strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
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

func (s *Server) isTokenInvalid(token string) (bool, error) {
	hash := hashToken(token)
	cacheKey := string(hash)
	if s.invalidTokens.Contain(cacheKey) {
		return true, nil
	}

	var (
		found bool
		rem   time.Duration
	)
	err := s.db.View(func(txn *badger.Txn) error {
		var err error
		rem, found, err = getInvalidTokenTTL(txn, hash)
		return err
	})
	if err != nil {
		return false, err
	}

	if found {
		if rem <= 0 {
			rem = invalidTokenTTL
		}
		s.invalidTokens.Set(cacheKey, struct{}{}, cache.WithTTL(rem))
		return true, nil
	}

	return false, nil
}

func (s *Server) invalidateToken(token string) {
	s.users.Delete(token)

	hash := hashToken(token)
	ttl := invalidTokenTTL
	s.invalidTokens.Set(string(hash), struct{}{}, cache.WithTTL(ttl))

	if err := s.db.Update(func(txn *badger.Txn) error {
		return saveInvalidToken(txn, hash, ttl)
	}); err != nil {
		slog.Error("saveInvalidToken", "err", err)
	}
}

func (s *Server) handleAuth() func(next http.Handler) http.Handler {
	var sf singleflight.Group

	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			token := extractBearerToken(r)
			if token == "" {
				next.ServeHTTP(w, r)
				return
			}

			invalid, err := s.isTokenInvalid(token)
			if err != nil {
				slog.Error("isTokenInvalid", "err", err)
				next.ServeHTTP(w, r)
				return
			}

			if invalid {
				next.ServeHTTP(w, r)
				return
			}

			client, err := clientFromToken(token)
			if err != nil {
				slog.Error("clientFromToken", "err", err)
				next.ServeHTTP(w, r)
				return
			}

			res, err, _ := sf.Do(token, func() (res interface{}, err error) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic", "err", r)
						err = fmt.Errorf("%w: %v", errAuthPanic, r)
					}
				}()

				if u, ok := s.users.Get(token); ok {
					return u, nil
				}

				u, err := fetchUserMe(ctx, client)
				if err != nil {
					return nil, err
				}

				user := &User{
					MixinID: u.UserID,
					Token:   token,
				}

				s.users.Set(token, user, cache.WithTTL(userCacheTTL))
				return user, nil
			})

			if err != nil {
				if isPermanentAuthError(err) {
					s.invalidateToken(token)
				}

				slog.Error("auth user", "err", err)
				next.ServeHTTP(w, r)
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUser(ctx, res.(*User))))
		}

		return http.HandlerFunc(fn)
	}
}
