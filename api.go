package cowallet

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cast"
	"github.com/twitchtv/twirp"
)

func (s *Server) Handler() http.Handler {
	m := chi.NewMux()
	m.Use(middleware.Recoverer)
	m.Use(middleware.RealIP)
	m.Use(middleware.Logger)
	m.Use(middleware.Heartbeat("/hc"))
	m.Use(handleAuth())

	m.Get("/vaults", s.findVault)
	m.Get("/requests", s.listRequests)

	return m
}

func renderJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)

	_ = json.NewEncoder(w).Encode(v)
}

func renderErr(w http.ResponseWriter, err error) {
	_ = twirp.WriteError(w, err)
}

func (s *Server) findVault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	members := r.Header.Values("members")
	threshold := cast.ToUint8(r.Header.Get("threshold"))

	if !govalidator.IsIn(user.MixinID, members...) {
		renderErr(w, twirp.PermissionDenied.Error("permission denied"))
		return
	}

	if threshold == 0 || threshold > uint8(len(members)) {
		renderErr(w, twirp.InvalidArgumentError("threshold", "invalid threshold"))
		return
	}

	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	vault, err := findVault(txn, members, threshold)
	if err != nil {
		renderErr(w, err)
		return
	}

	job := &Job{
		CreatedAt: time.Now(),
		User:      user,
		Members:   members,
		Threshold: threshold,
	}

	if err := saveJob(txn, job, 5*time.Minute); err != nil {
		renderErr(w, err)
		return
	}

	if err := txn.Commit(); err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, vault)
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	members := r.Header.Values("members")
	if !govalidator.IsIn(user.MixinID, members...) {
		renderErr(w, twirp.PermissionDenied.Error("permission denied"))
		return
	}

	threshold := cast.ToUint8(r.Header.Get("threshold"))
	since := cast.ToTime(r.Header.Get("offset"))
	limit := cast.ToInt(r.Header.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 10
	}

	txn := s.db.NewTransaction(false)
	defer txn.Discard()

	requests, err := listRequests(txn, members, threshold, since, limit)
	if err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, requests)
}
