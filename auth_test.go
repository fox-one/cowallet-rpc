package cowallet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func newTestServer(t *testing.T, db *badger.DB, whitelist ...string) (Server, http.Handler) {
	t.Helper()

	svr := NewServer(db, &mixin.Client{ClientID: uuid.NewString()}, Config{
		Whitelist: whitelist,
	})
	return svr, svr.Handler()
}

func makeOauthToken(t *testing.T, clientID, authID, userID string) string {
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
	if userID != "" {
		payload["user_id"] = userID
	}

	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal oauth token: %v", err)
	}
	return string(b)
}

func makeKeystoreToken(t *testing.T, idField, userID string) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}

	payload := map[string]any{
		idField:       userID,
		"session_id":  uuid.NewString(),
		"private_key": base64.RawURLEncoding.EncodeToString(priv),
		"scope":       "FULL",
	}

	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal keystore token: %v", err)
	}
	return string(b)
}

func seedVaultRenew(t *testing.T, db *badger.DB, members []string, threshold uint8, renewID uuid.UUID, createdAt, expiredAt time.Time) {
	t.Helper()

	err := db.Update(func(txn *badger.Txn) error {
		if err := saveVaultIfNotExist(txn, &Vault{
			Members:   members,
			Threshold: threshold,
		}); err != nil {
			return err
		}

		return saveRenew(txn, &Renew{
			ID:        renewID,
			Sequence:  1,
			CreatedAt: createdAt,
			Members:   members,
			Threshold: threshold,
			To:        expiredAt,
		})
	})
	if err != nil {
		t.Fatalf("seed vault renew: %v", err)
	}
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

func TestAuthPublicRoutes(t *testing.T) {
	db := openTestDB(t)
	_, h := newTestServer(t, db)
	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: uuid.NewString()}, nil
	})

	unpaidToken := makeOauthToken(t, uuid.NewString(), uuid.NewString(), uuid.NewString())

	for _, tc := range []struct {
		name string
		path string
		auth string
	}{
		{name: "hc_no_auth", path: "/hc"},
		{name: "hc_with_unpaid_auth", path: "/hc", auth: "Bearer " + unpaidToken},
		{name: "info_no_auth", path: "/info"},
		{name: "info_with_unpaid_auth", path: "/info", auth: "Bearer " + unpaidToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doRequest(h, http.MethodGet, tc.path, tc.auth)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200, body = %s", tc.path, rec.Code, rec.Body.String())
			}
		})
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("fetchUserMe calls = %d, want 0", got)
	}
}

func TestAuthMissingOrInvalidToken(t *testing.T) {
	db := openTestDB(t)
	_, h := newTestServer(t, db)
	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: uuid.NewString()}, nil
	})

	// Missing Authorization header
	rec := doRequest(h, http.MethodGet, "/vaults", "")
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")

	// Malformed JSON token
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer not-json")
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")

	// OauthKeystore without user_id
	noUserIDToken := makeOauthToken(t, uuid.NewString(), uuid.NewString(), "")
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+noUserIDToken)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")

	if got := calls.Load(); got != 0 {
		t.Fatalf("fetchUserMe calls = %d, want 0", got)
	}
}

func TestAuthRejectsUnpaidAndExpiredUsersBeforeUserMe(t *testing.T) {
	db := openTestDB(t)
	_, h := newTestServer(t, db)
	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: uuid.NewString()}, nil
	})

	unpaidUserID := uuid.NewString()
	unpaidToken := makeOauthToken(t, uuid.NewString(), uuid.NewString(), unpaidUserID)
	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+unpaidToken)
	assertTwirpCode(t, rec, http.StatusForbidden, "permission_denied")

	expiredUserID := uuid.NewString()
	otherMember := uuid.NewString()
	seedVaultRenew(
		t,
		db,
		[]string{expiredUserID, otherMember},
		2,
		uuid.New(),
		time.Now().Add(-2*time.Hour),
		time.Now().Add(-time.Minute),
	)

	expiredToken := makeOauthToken(t, uuid.NewString(), uuid.NewString(), expiredUserID)
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+expiredToken)
	assertTwirpCode(t, rec, http.StatusForbidden, "permission_denied")

	if got := calls.Load(); got != 0 {
		t.Fatalf("fetchUserMe calls = %d, want 0", got)
	}
}

func TestAuthActiveVaultCacheAndExpiration(t *testing.T) {
	db := openTestDB(t)
	_, h := newTestServer(t, db)

	memberID := uuid.NewString()
	otherID := uuid.NewString()
	renewID := uuid.New()
	renewCreatedAt := time.Now().Add(-time.Second)

	seedVaultRenew(
		t,
		db,
		[]string{memberID, otherID},
		2,
		renewID,
		renewCreatedAt,
		time.Now().Add(time.Hour),
	)

	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: memberID}, nil
	})

	token := makeOauthToken(t, uuid.NewString(), uuid.NewString(), memberID)

	// First request succeeds and calls GET /me once.
	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after first request = %d, want 1", got)
	}

	// Second request hits cache without calling GET /me again.
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after cached request = %d, want 1", got)
	}

	// Expire the vault; cached token must be rejected immediately before cache lookup.
	seedVaultRenew(
		t,
		db,
		[]string{memberID, otherID},
		2,
		renewID,
		renewCreatedAt,
		time.Now().Add(-time.Minute),
	)

	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusForbidden, "permission_denied")
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after expiration = %d, want 1", got)
	}
}

func TestAuthSpoofedUserIDRejectedAndNotCached(t *testing.T) {
	db := openTestDB(t)
	_, h := newTestServer(t, db)

	activeMemberID := uuid.NewString()
	attackerID := uuid.NewString()
	seedVaultRenew(
		t,
		db,
		[]string{activeMemberID, uuid.NewString()},
		2,
		uuid.New(),
		time.Now().Add(-time.Second),
		time.Now().Add(time.Hour),
	)

	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: attackerID}, nil
	})

	spoofedToken := makeOauthToken(t, uuid.NewString(), uuid.NewString(), activeMemberID)

	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+spoofedToken)
	assertTwirpCode(t, rec, http.StatusForbidden, "permission_denied")

	// Repeat request must still call fetchUserMe (not cached on mismatch) and still reject.
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+spoofedToken)
	assertTwirpCode(t, rec, http.StatusForbidden, "permission_denied")

	if got := calls.Load(); got != 2 {
		t.Fatalf("fetchUserMe calls = %d, want 2", got)
	}
}

func TestAuthKeystoreClientIDAndAppID(t *testing.T) {
	db := openTestDB(t)
	whitelistedID := uuid.NewString()
	activeMemberID := uuid.NewString()
	unpaidID := uuid.NewString()

	seedVaultRenew(
		t,
		db,
		[]string{activeMemberID, uuid.NewString()},
		1,
		uuid.New(),
		time.Now().Add(-time.Second),
		time.Now().Add(time.Hour),
	)

	_, h := newTestServer(t, db, whitelistedID)

	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: client.ClientID}, nil
	})

	// Standard Keystore with client_id (unpaid -> 403 before GET /me)
	unpaidKeystore := makeKeystoreToken(t, "client_id", unpaidID)
	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+unpaidKeystore)
	assertTwirpCode(t, rec, http.StatusForbidden, "permission_denied")
	if got := calls.Load(); got != 0 {
		t.Fatalf("fetchUserMe calls after unpaid keystore = %d, want 0", got)
	}

	// Standard Keystore with client_id (active vault -> 200)
	activeKeystore := makeKeystoreToken(t, "client_id", activeMemberID)
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+activeKeystore)
	if rec.Code != http.StatusOK {
		t.Fatalf("client_id keystore status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	// Standard Keystore with app_id (whitelisted -> 200)
	appIDKeystore := makeKeystoreToken(t, "app_id", whitelistedID)
	rec = doRequest(h, http.MethodGet, "/addresses", "Bearer "+appIDKeystore)
	if rec.Code != http.StatusOK {
		t.Fatalf("app_id keystore status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("fetchUserMe calls = %d, want 2", got)
	}
}

func TestAuthWhitelistAllowsUserWithoutVaults(t *testing.T) {
	db := openTestDB(t)
	whitelistedID := uuid.NewString()
	dirtyEntry := "  " + strings.ToUpper(whitelistedID) + "  "

	_, h := newTestServer(t, db, dirtyEntry)

	calls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: whitelistedID}, nil
	})

	token := makeOauthToken(t, uuid.NewString(), uuid.NewString(), whitelistedID)

	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /vaults status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	rec = doRequest(h, http.MethodGet, "/addresses", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /addresses status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls = %d, want 1", got)
	}
}

func TestClientFromTokenBackwardsCompatibleWithoutUserID(t *testing.T) {
	token := makeOauthToken(t, uuid.NewString(), uuid.NewString(), "")
	c, err := clientFromToken(token)
	if err != nil {
		t.Fatalf("clientFromToken failed for OauthKeystore without user_id: %v", err)
	}
	if c == nil {
		t.Fatal("clientFromToken returned nil client")
	}
}
