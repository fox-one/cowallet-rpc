package cowallet

import (
	"context"
)

type contextKey struct{}

var (
	// ContextKeyUser is the context key for user.
	userContextKey = contextKey{}
)

func WithUser(ctx context.Context, user *User) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

func UserFrom(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(userContextKey).(*User)
	return user, ok
}
