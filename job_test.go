package cowallet

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/google/uuid"
)

func mockSafeListUtxos(t *testing.T, fn func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error)) *atomic.Int32 {
	t.Helper()

	orig := safeListUtxos
	t.Cleanup(func() { safeListUtxos = orig })

	var calls atomic.Int32
	safeListUtxos = func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		calls.Add(1)
		return fn(ctx, client, opt)
	}
	return &calls
}

func seedUserJob(t *testing.T, db *badger.DB, user *User, members []string, threshold uint8) {
	t.Helper()

	err := db.Update(func(txn *badger.Txn) error {
		return saveJob(txn, &Job{
			CreatedAt: time.Now(),
			User:      user,
			Members:   members,
			Threshold: threshold,
		}, 5*time.Minute)
	})
	if err != nil {
		t.Fatalf("seed user job: %v", err)
	}
}

func TestJob401InvalidatesTokenEvictsCacheAndDeletesJob(t *testing.T) {
	db := openTestDB(t)
	svr, h := newTestServer(t, db)

	memberID := uuid.NewString()
	members := []string{memberID, uuid.NewString()}

	userMeCalls := mockFetchUserMe(t, func(ctx context.Context, client *mixin.Client) (*mixin.User, error) {
		return &mixin.User{UserID: memberID}, nil
	})

	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())

	// Populate svr.users cache via an authenticated HTTP request.
	rec := doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	if rec.Code != http.StatusOK {
		t.Fatalf("initial /vaults status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := userMeCalls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls = %d, want 1", got)
	}

	seedUserJob(t, db, &User{MixinID: memberID, Token: token}, members, 2)

	utxoCalls := mockSafeListUtxos(t, func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		return nil, &mixin.Error{Status: 202, Code: mixin.Unauthorized, Description: "Unauthorized"}
	})

	_ = svr.handlePendingJobs(context.Background())

	if got := utxoCalls.Load(); got != 1 {
		t.Fatalf("safeListUtxos calls = %d, want 1", got)
	}

	// Token must be invalidated in memory and BadgerDB, and evicted from users cache.
	if _, ok := svr.users.Get(token); ok {
		t.Fatal("expected token to be evicted from svr.users")
	}
	invalid, err := svr.isTokenInvalid(token)
	if err != nil {
		t.Fatalf("isTokenInvalid: %v", err)
	}
	if !invalid {
		t.Fatal("expected token to be marked invalid")
	}

	// Subsequent HTTP request must fail immediately with 0 fetchUserMe calls.
	rec = doRequest(h, http.MethodGet, "/vaults", "Bearer "+token)
	assertTwirpCode(t, rec, http.StatusUnauthorized, "unauthenticated")
	if got := userMeCalls.Load(); got != 1 {
		t.Fatalf("fetchUserMe calls after job 401 = %d, want 1", got)
	}

	// Job must be deleted so the next tick makes 0 SafeListUtxos calls.
	jobs, err := ListJobs(db)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("remaining jobs = %d, want 0", len(jobs))
	}

	_ = svr.handlePendingJobs(context.Background())
	if got := utxoCalls.Load(); got != 1 {
		t.Fatalf("safeListUtxos calls after job deleted = %d, want 1", got)
	}
}

func TestJobSkipsAlreadyInvalidatedTokenAndDeletesJob(t *testing.T) {
	db := openTestDB(t)
	svr1, _ := newTestServer(t, db)

	memberID := uuid.NewString()
	members := []string{memberID, uuid.NewString()}
	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())

	svr1.invalidateToken(token)
	seedUserJob(t, db, &User{MixinID: memberID, Token: token}, members, 2)

	utxoCalls := mockSafeListUtxos(t, func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		return nil, nil
	})

	// Use a fresh Server instance to confirm DB-persisted invalidTokens prevents Mixin API calls.
	svr2, _ := newTestServer(t, db)
	if err := svr2.handlePendingJobs(context.Background()); err != nil {
		t.Fatalf("handlePendingJobs: %v", err)
	}

	if got := utxoCalls.Load(); got != 0 {
		t.Fatalf("safeListUtxos calls = %d, want 0", got)
	}

	jobs, err := ListJobs(db)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("remaining jobs = %d, want 0", len(jobs))
	}
}

func TestJob403DeletesJobWithoutInvalidatingToken(t *testing.T) {
	db := openTestDB(t)
	svr, _ := newTestServer(t, db)

	memberID := uuid.NewString()
	members := []string{memberID, uuid.NewString()}
	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())
	seedUserJob(t, db, &User{MixinID: memberID, Token: token}, members, 2)

	utxoCalls := mockSafeListUtxos(t, func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		return nil, &mixin.Error{Status: 403, Code: http.StatusForbidden, Description: "Forbidden"}
	})

	_ = svr.handlePendingJobs(context.Background())

	if got := utxoCalls.Load(); got != 1 {
		t.Fatalf("safeListUtxos calls = %d, want 1", got)
	}

	invalid, err := svr.isTokenInvalid(token)
	if err != nil {
		t.Fatalf("isTokenInvalid: %v", err)
	}
	if invalid {
		t.Fatal("403 forbidden must not globally invalidate token")
	}

	jobs, err := ListJobs(db)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("remaining jobs = %d, want 0", len(jobs))
	}

	_ = svr.handlePendingJobs(context.Background())
	if got := utxoCalls.Load(); got != 1 {
		t.Fatalf("safeListUtxos calls on second tick = %d, want 1", got)
	}
}

func TestJobTransientErrorKeepsJobAndToken(t *testing.T) {
	db := openTestDB(t)
	svr, _ := newTestServer(t, db)

	memberID := uuid.NewString()
	members := []string{memberID, uuid.NewString()}
	token := makeOauthToken(t, uuid.NewString(), uuid.NewString())
	seedUserJob(t, db, &User{MixinID: memberID, Token: token}, members, 2)

	mockSafeListUtxos(t, func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		return nil, context.DeadlineExceeded
	})

	if err := svr.handlePendingJobs(context.Background()); err == nil {
		t.Fatal("expected handlePendingJobs to return transient error")
	}

	invalid, err := svr.isTokenInvalid(token)
	if err != nil {
		t.Fatalf("isTokenInvalid: %v", err)
	}
	if invalid {
		t.Fatal("transient error must not invalidate token")
	}

	jobs, err := ListJobs(db)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("remaining jobs = %d, want 1", len(jobs))
	}
}

func TestJobConditionalDeletePreservesNewerMemberToken(t *testing.T) {
	db := openTestDB(t)
	svr, _ := newTestServer(t, db)

	memberA := uuid.NewString()
	memberB := uuid.NewString()
	members := []string{memberA, memberB}

	tokenA := makeOauthToken(t, uuid.NewString(), uuid.NewString())
	tokenB := makeOauthToken(t, uuid.NewString(), uuid.NewString())

	seedUserJob(t, db, &User{MixinID: memberA, Token: tokenA}, members, 2)

	mockSafeListUtxos(t, func(ctx context.Context, client *mixin.Client, opt mixin.SafeListUtxoOption) ([]*mixin.SafeUtxo, error) {
		// Simulate member B overwriting the vault job while member A's call is in flight.
		seedUserJob(t, db, &User{MixinID: memberB, Token: tokenB}, members, 2)
		return nil, &mixin.Error{Status: 202, Code: mixin.Unauthorized, Description: "Unauthorized"}
	})

	_ = svr.handlePendingJobs(context.Background())

	invalidA, err := svr.isTokenInvalid(tokenA)
	if err != nil {
		t.Fatalf("isTokenInvalid(A): %v", err)
	}
	if !invalidA {
		t.Fatal("expected member A token to be marked invalid")
	}

	jobs, err := ListJobs(db)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("remaining jobs = %d, want 1", len(jobs))
	}
	if jobs[0].User == nil || jobs[0].User.Token != tokenB {
		t.Fatalf("expected member B job to be preserved, got %+v", jobs[0].User)
	}
}

func TestJobNilUserInitializesVaultAndDeletesItself(t *testing.T) {
	db := openTestDB(t)
	svr, _ := newTestServer(t, db)

	memberID := uuid.NewString()
	members := []string{memberID, uuid.NewString()}
	seedUserJob(t, db, nil, members, 1)

	if err := svr.handlePendingJobs(context.Background()); err != nil {
		t.Fatalf("handlePendingJobs: %v", err)
	}

	txn := db.NewTransaction(false)
	defer txn.Discard()

	vaults, err := listVaults(txn, memberID)
	if err != nil {
		t.Fatalf("listVaults: %v", err)
	}
	if len(vaults) != 1 {
		t.Fatalf("vault count = %d, want 1", len(vaults))
	}

	jobs, err := ListJobs(db)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("remaining jobs = %d, want 0", len(jobs))
	}
}
