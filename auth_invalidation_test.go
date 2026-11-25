package cowallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/google/uuid"
)

func openTestDB(t *testing.T) *badger.DB {
	t.Helper()

	db, err := badger.Open(badger.DefaultOptions("").WithInMemory(true).WithLogger(nil))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestServer(t *testing.T, db *badger.DB) (Server, http.Handler) {
	t.Helper()

	svr := NewServer(db, &mixin.Client{ClientID: uuid.NewString()}, Config{})
	return svr, svr.Handler()
}

func makeOauthToken(t *testing.T, clientID, authID string) string {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	payload := map[string]any{
		"client_id":        clientID,
		"authorization_id": authID,
		"scope":            "PROFILE:READ",
		"private_key":      base64.RawURLEncoding.EncodeToString(priv),
		"ed25519":          base64.RawURLEncoding.EncodeToString(pub),
	}

	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal oauth token: %v", err)
	}
	return string(b)
}

func mockFetchUserMe(t *testing.T, fn func(ctx context.Context, client *mixin.Client) (*mixin.User, error)) *atomic.Int32 {
	t.Helper()

	orig := fetchUserMe
	t.Cleanup(func() { fetchUserMe = orig })

	var calls atomic.Int32
	fetchUserMe = func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		calls.Add(1)
		return fn(ctx, client)
	}
	return &calls
}

func doRequest(h http.Handler, method, path, authHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertTwirpCode(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()

	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, wantStatus, rec.Body.String())
	}

	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	if body.Code != wantCode {
		t.Fatalf("twirp code = %q, want %q, body = %s", body.Code, wantCode, rec.Body.String())
	}
}

func TestAuthInvalidates401PersistsAcrossRestartAndExpires(t *testing.T) {
	origTTL := invalidTokenTTL
	invalidTokenTTL = time.Second
	t.Cleanup(func() { invalidTokenTTL = origTTL })

	db := openTestDB(t)
	svr1, h1 := newTestServer(t, db)
	userID := uuid.NewString()

	var returnOK atomic.Bool
	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		if returnOK.Load() {
			return &mixin.User{UserID: userID}, nil
		}
		return nil, &mixin.Error{Status: 202, Code: mixin.Unauthorized, Description: "Unauthorized"}
	})

	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())

	// 1st request calls fetchUserMe once and gets 401; token is marked invalid.
	rec := doRequest(h1, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after 1st request = %d, want 1", got)
	}

	// /info must still return 200 even with an invalid token.
	rec = doRequest(h1, http.MethodGet, "/info", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /info with invalid token status = %d, want 200", rec.Code)
	}

	// 2nd request on same server hits invalidTokens (0 additional fetchUserMe calls).
	rec = doRequest(h1, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after 2nd request = %d, want 1", got)
	}

	// Simulate restart: new Server instance backed by the same BadgerDB still rejects with 0 calls.
	_, h2 := newTestServer(t, db)
	rec = doRequest(h2, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after restart = %d, want 1", got)
	}

	// Wait for BadgerDB and in-memory TTL (1s) to expire.
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		invalid, err := svr1.isTokenInvalid(token)
		if err != nil {
			t.Fatalf("isTokenInvalid: %v", err)
		}
		if !invalid {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	invalid, err := svr1.isTokenInvalid(token)
	if err != nil {
		t.Fatalf("isTokenInvalid after ttl: %v", err)
	}
	if invalid {
		t.Fatal("expected invalid token entry to expire")
	}

	// Once expired, a re-authorized token can call fetchUserMe again.
	returnOK.Store(true)
	rec = doRequest(h1, http.MethodGet, "/vaults", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status after ttl expiry = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetchUserMe calls after ttl expiry = %d, want 2", got)
	}
}

func TestAuthTransientErrorDoesNotInvalidateToken(t *testing.T) {
	db := openTestDB(t)
	svr, h := newTestServer(t, db)
	userID := uuid.NewString()

	var attempt atomic.Int32
	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		if attempt.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		return &mixin.User{UserID: userID}, nil
	})

	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())

	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")

	invalid, err := svr.isTokenInvalid(token)
	if err != nil {
		t.Fatalf("isTokenInvalid: %v", err)
	}
	if invalid {
		t.Fatal("transient error must not invalidate token")
	}

	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("fetchUserMe calls = %d, want 2", got)
	}
}

func TestAuthPanicInvalidatesToken(t *testing.T) {
	db := openTestDB(t)
	svr, h := newTestServer(t, db)

	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		panic("boom")
	})

	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())

	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")

	invalid, err := svr.isTokenInvalid(token)
	if err != nil {
		t.Fatalf("isTokenInvalid: %v", err)
	}
	if !invalid {
		t.Fatal("panic during auth should invalidate token")
	}

	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls = %d, want 1", got)
	}
}
