package cowallet

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/cors"
	"github.com/spf13/cast"
	"github.com/twitchtv/twirp"
)

func (s *Server) Handler() http.Handler {
	m := chi.NewMux()
	m.Use(middleware.Recoverer)
	m.Use(middleware.RealIP)
	m.Use(middleware.Logger)
	m.Use(middleware.Heartbeat("/hc"))
	m.Use(cors.AllowAll().Handler)
	m.Use(handleAuth())

	m.Get("/vaults", s.findVault)
	m.Put("/vaults", s.updateVault)
	m.Get("/snapshots", s.listSnapshots)

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

func (s *Server) updateVault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	var body struct {
		Name      string   `json:"name"`
		Members   []string `json:"members"`
		Threshold uint8    `json:"threshold"`
	}

	if !govalidator.IsIn(user.MixinID, body.Members...) {
		slog.Warn("user not in members", "user", user.MixinID, "members", body.Members)
		renderErr(w, twirp.PermissionDenied.Error("permission denied"))
		return
	}

	if body.Threshold == 0 || body.Threshold > uint8(len(body.Members)) {
		renderErr(w, twirp.InvalidArgumentError("threshold", "invalid threshold"))
		return
	}

	if body.Name == "" {
		renderErr(w, twirp.InvalidArgumentError("name", "invalid"))
		return
	}

	txn := s.db.NewTransaction(true)
	defer txn.Commit()

	vault, err := findVault(txn, body.Members, body.Threshold)
	if err != nil {
		renderErr(w, err)
		return
	}

	if vault.Name != body.Name {
		vault.Name = body.Name

		if err := saveVault(txn, vault); err != nil {
			renderErr(w, err)
			return
		}
	}

	if err := txn.Commit(); err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, vault)
}

func (s *Server) findVault(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	q := r.URL.Query()
	members := q["members"]
	threshold := cast.ToUint8(q.Get("threshold"))

	if !govalidator.IsIn(user.MixinID, members...) {
		slog.Warn("xx user not in members", "user", user.MixinID, "members", members)
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

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	q := r.URL.Query()
	members := q["members"]
	if !govalidator.IsIn(user.MixinID, members...) {
		slog.Warn("user not in members", "user", user.MixinID, "members", members)
		renderErr(w, twirp.PermissionDenied.Error("permission denied"))
		return
	}

	threshold := cast.ToUint8(q.Get("threshold"))
	since := cast.ToTime(q.Get("offset"))
	limit := cast.ToInt(q.Get("limit"))
	assetID := q.Get("asset")
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	txn := s.db.NewTransaction(false)
	defer txn.Discard()

	snapshots, err := listSnapshots(txn, members, threshold, assetID, since, limit)
	if err != nil {
		slog.Error("listSnapshots", "error", err)
		renderErr(w, err)
		return
	}

	renderJSON(w, snapshots)
}
