package cowallet

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/dgraph-io/badger/v4"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/cors"
	"github.com/spf13/cast"
	"github.com/twitchtv/twirp"
	"github.com/yiplee/go-cache"
)

func (s *Server) Handler() http.Handler {
	m := chi.NewMux()
	m.Use(middleware.Recoverer)
	m.Use(middleware.RealIP)
	m.Use(middleware.Logger)
	m.Use(middleware.Heartbeat("/hc"))
	m.Use(cors.AllowAll().Handler)
	m.Use(handleAuth())

	m.Get("/info", s.getSystemInfo)

	m.Route("/vaults", func(r chi.Router) {
		r.Get("/", s.listVaults)
		r.Get("/{addr}", s.findVault)
		r.Put("/{addr}", s.updateVault)
	})

	m.Route("/snapshots", func(r chi.Router) {
		r.Get("/", s.listSnapshots)
		r.Get("/{addr}", s.listSnapshots)
	})

	m.Route("/addresses", func(r chi.Router) {
		r.Get("/", s.listAddresses)
		r.Post("/", s.saveAddress)
		r.Delete("/{addr}", s.deleteAddress)
	})

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

type VaultView struct {
	*Vault

	Name      string    `json:"name"`
	ExpiredAt time.Time `json:"expired_at"`
}

type VaultParam struct {
	user      *User
	members   []string
	threshold uint8
	query     url.Values
}

func extractVault(r *http.Request) (*VaultParam, error) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		return nil, twirp.Unauthenticated.Error("unauthenticated")
	}

	q := r.URL.Query()
	members := q["members"]
	threshold := cast.ToUint8(q.Get("threshold"))

	if s := chi.URLParam(r, "addr"); s != "" {
		addr, err := mixin.MixAddressFromString(s)
		if err != nil {
			return nil, twirp.InvalidArgumentError("addr", "invalid")
		}

		members = addr.Members()
		threshold = addr.Threshold
	} else if _, err := mixin.NewMixAddress(members, threshold); err != nil {
		return nil, twirp.InvalidArgumentError("members", "invalid")
	}

	if !govalidator.IsIn(user.MixinID, members...) {
		return nil, twirp.PermissionDenied.Error("permission denied")
	}

	return &VaultParam{
		user:      user,
		members:   members,
		threshold: threshold,
		query:     q,
	}, nil
}

func (s *Server) getSystemInfo(w http.ResponseWriter, r *http.Request) {
	renderJSON(w, map[string]any{
		"client_id":    s.client.ClientID,
		"pay_asset_id": s.cfg.PayAssetID,
		"pay_amount":   s.cfg.PayAmount,
	})
}

func (s *Server) listVaults(w http.ResponseWriter, r *http.Request) {
	if _, err := extractVault(r); err == nil {
		s.findVault(w, r)
		return
	}

	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	txn := s.db.NewTransaction(false)
	defer txn.Discard()

	vaults, err := listVaults(txn, user.MixinID)
	if err != nil {
		renderErr(w, err)
		return
	}

	views := []VaultView{}
	for _, v := range vaults {
		name, err := getRemarkName(txn, uuid.MustParse(user.MixinID), v.Members, v.Threshold)
		if err != nil {
			renderErr(w, err)
			return
		}

		expiredAt, _, err := getVaultExpiredAt(txn, v.Members, v.Threshold)
		if err != nil {
			renderErr(w, err)
			return
		}

		view := VaultView{
			Vault:     v,
			Name:      name,
			ExpiredAt: expiredAt,
		}

		s.bindVaultAssets(ctx, &view)
		views = append(views, view)
	}

	renderJSON(w, views)
}

func (s *Server) updateVault(w http.ResponseWriter, r *http.Request) {
	p, err := extractVault(r)
	if err != nil {
		renderErr(w, err)
		return
	}

	var body struct {
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		renderErr(w, twirp.InvalidArgumentError("body", "invalid"))
		return
	}

	remark := &Remark{
		User:      uuid.MustParse(p.user.MixinID),
		Members:   p.members,
		Threshold: p.threshold,
		Name:      strings.TrimSpace(body.Name),
		UpdatedAt: time.Now(),
	}

	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	if err := saveRemark(txn, remark); err != nil {
		renderErr(w, err)
		return
	}

	if err := txn.Commit(); err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, remark)
}

func (s *Server) findVault(w http.ResponseWriter, r *http.Request) {
	p, err := extractVault(r)
	if err != nil {
		renderErr(w, err)
		return
	}

	txn := s.db.NewTransaction(true)
	defer txn.Discard()

	vault, err := findVault(txn, p.members, p.threshold)
	if err != nil {
		renderErr(w, err)
		return
	}

	name, err := getRemarkName(txn, uuid.MustParse(p.user.MixinID), vault.Members, vault.Threshold)
	if err != nil {
		renderErr(w, err)
		return
	}

	expiredAt, _, err := getVaultExpiredAt(txn, vault.Members, vault.Threshold)
	if err != nil {
		renderErr(w, err)
		return
	}

	if dur := time.Until(expiredAt); dur > 0 {
		job := &Job{
			CreatedAt: time.Now(),
			User:      p.user,
			Members:   vault.Members,
			Threshold: vault.Threshold,
		}

		if err := saveJob(txn, job, min(5*time.Minute, dur)); err != nil {
			renderErr(w, err)
			return
		}
	}

	if err := txn.Commit(); err != nil {
		renderErr(w, err)
		return
	}

	view := VaultView{
		Vault:     vault,
		Name:      name,
		ExpiredAt: expiredAt,
	}
	s.bindVaultAssets(r.Context(), &view)
	renderJSON(w, view)
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	p, err := extractVault(r)
	if err != nil {
		renderErr(w, err)
		return
	}

	since := cast.ToTime(p.query.Get("offset"))
	limit := cast.ToInt(p.query.Get("limit"))
	assetID := p.query.Get("asset")
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	txn := s.db.NewTransaction(false)
	defer txn.Discard()

	snapshots, err := listSnapshots(txn, p.members, p.threshold, assetID, since, limit)
	if err != nil {
		slog.Error("listSnapshots", "error", err)
		renderErr(w, err)
		return
	}

	renderJSON(w, snapshots)
}

func (s *Server) listAddresses(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	outputs, err := ListAddress(s.db, uuid.MustParse(user.MixinID))
	if err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, outputs)
}

func (s *Server) deleteAddress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	addr, err := mixin.MixAddressFromString(chi.URLParam(r, "addr"))
	if err != nil {
		renderErr(w, twirp.InvalidArgument.Error("invalid address"))
		return
	}

	v := Address{
		UserID:    uuid.MustParse(user.MixinID),
		Members:   addr.Members(),
		Threshold: uint8(addr.Threshold),
	}

	if err := s.db.Update(func(txn *badger.Txn) error {
		return saveAddress(txn, v)
	}); err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, v)
}

func (s *Server) saveAddress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, ok := UserFrom(ctx)
	if !ok {
		renderErr(w, twirp.Unauthenticated.Error("unauthenticated"))
		return
	}

	var body struct {
		Address   string   `json:"address"`
		Members   []string `json:"members"`
		Threshold uint8    `json:"threshold"`
		Label     string   `json:"label"`
	}

	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		renderErr(w, twirp.InvalidArgumentError("body", "invalid"))
		return
	}

	if body.Address != "" {
		addr, err := mixin.MixAddressFromString(body.Address)
		if err != nil {
			renderErr(w, twirp.InvalidArgumentError("address", "invalid"))
			return
		}
		body.Members = addr.Members()
		body.Threshold = addr.Threshold
	} else if _, err := mixin.NewMixAddress(body.Members, body.Threshold); err != nil {
		renderErr(w, twirp.InvalidArgumentError("members", "invalid"))
		return
	}

	v := Address{
		UserID:    uuid.MustParse(user.MixinID),
		Members:   body.Members,
		Threshold: body.Threshold,
		Label:     strings.TrimSpace(body.Label),
		UpdatedAt: time.Now(),
	}

	if v.Label == "" {
		renderErr(w, twirp.InvalidArgument.Error("label is required"))
		return
	}

	if err := s.db.Update(func(txn *badger.Txn) error {
		return saveAddress(txn, v)
	}); err != nil {
		renderErr(w, err)
		return
	}

	renderJSON(w, v)
}

func (s *Server) getSafeAsset(ctx context.Context, id string) (*mixin.SafeAsset, error) {
	v, ok := s.assets.Get(id)
	if ok {
		return v, nil
	}

	v, err := s.client.SafeReadAsset(ctx, id)
	if err != nil {
		return nil, err
	}

	s.assets.Set(id, v, cache.WithTTL(10*time.Minute))
	return v, nil
}

func (s *Server) bindVaultAssets(ctx context.Context, view *VaultView) {
	for _, asset := range view.Assets {
		v, err := s.getSafeAsset(ctx, asset.ID)
		if err != nil {
			continue
		}

		asset.Asset = v
	}
}
