package cowallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/google/uuid"
	"github.com/twitchtv/twirp"
	"github.com/yiplee/go-cache"
	"golang.org/x/sync/singleflight"
)

var errUserIDMismatch = errors.New("user id mismatch")

var fetchUserMe = func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
	return client.UserMe(ctx)
}

func extractBearerToken(r *http.Request) string {
	token := r.Header.Get("Authorization")
	return strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
}

func parseToken(token string) (*mixin.Client, string, error) {
	r := strings.NewReader(token)

	var auth struct {
		mixin.OauthKeystore
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(r).Decode(&auth); err == nil && auth.AuthID != "" {
		c, err := mixin.NewFromOauthKeystore(&auth.OauthKeystore)
		if err != nil {
			return nil, "", err
		}

		return c, auth.UserID, nil
	}

	r.Reset(token)
	var key mixin.Keystore
	if err := json.NewDecoder(r).Decode(&key); err == nil {
		c, err := mixin.NewFromKeystore(&key)
		if err != nil {
			return nil, "", err
		}

		return c, c.ClientID, nil
	}

	return nil, "", fmt.Errorf("decode token failed")
}

func clientFromToken(token string) (*mixin.Client, error) {
	c, _, err := parseToken(token)
	return c, err
}

func (s *Server) isUserAllowed(uid uuid.UUID) (bool, error) {
	if _, ok := s.whitelist[uid.String()]; ok {
		return true, nil
	}

	var allowed bool
	err := s.db.View(func(txn *badger.Txn) error {
		var err error
		allowed, err = hasActiveVault(txn, uid)
		return err
	})

	return allowed, err
}

func (s *Server) handleAuth() func(next http.Handler) http.Handler {
	var (
		users = cache.New[string, *User]()
		sf    singleflight.Group
	)

	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			token := extractBearerToken(r)
			if token == "" {
				renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
				return
			}

			client, userID, err := parseToken(token)
			if err != nil {
				slog.Error("parseToken", "err", err)
				renderErr(w, twirp.Unauthenticated.Error("invalid token"))
				return
			}

			uid, err := uuid.Parse(strings.TrimSpace(userID))
			if err != nil {
				slog.Error("parse user_id", "err", err)
				renderErr(w, twirp.Unauthenticated.Error("invalid user_id"))
				return
			}

			allowed, err := s.isUserAllowed(uid)
			if err != nil {
				slog.Error("isUserAllowed", "user_id", uid.String(), "err", err)
				renderErr(w, twirp.InternalErrorWith(err))
				return
			}

			if !allowed {
				renderErr(w, twirp.PermissionDenied.Error("permission denied"))
				return
			}

			res, err, _ := sf.Do(token, func() (res interface{}, err error) {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("panic", "err", r)
						err = fmt.Errorf("auth panic: %v", r)
					}
				}()

				if u, ok := users.Get(token); ok {
					if !strings.EqualFold(u.MixinID, uid.String()) {
						return nil, errUserIDMismatch
					}

					return u, nil
				}

				u, err := fetchUserMe(ctx, client)
				if err != nil {
					return nil, err
				}

				if !strings.EqualFold(u.UserID, uid.String()) {
					slog.Warn("user_id mismatch", "expected", uid.String(), "actual", u.UserID)
					return nil, errUserIDMismatch
				}

				user := &User{
					MixinID: u.UserID,
					Token:   token,
				}

				users.Set(token, user)
				return user, nil
			})

			if err != nil {
				if errors.Is(err, errUserIDMismatch) {
					renderErr(w, twirp.PermissionDenied.Error("user id mismatch"))
					return
				}

				slog.Error("auth user", "user_id", uid.String(), "err", err)
				renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
				return
			}

			next.ServeHTTP(w, r.WithContext(WithUser(ctx, res.(*User))))
		}

		return http.HandlerFunc(fn)
	}
}
