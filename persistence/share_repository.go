package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/Masterminds/squirrel"
	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/pocketbase/dbx"
)

type shareRepository struct {
	sqlRepository
}

func NewShareRepository(ctx context.Context, db dbx.Builder) model.ShareRepository {
	r := &shareRepository{}
	r.ctx = ctx
	r.db = db
	r.registerModel(&model.Share{}, nil)
	r.setSortMappings(map[string]string{
		"username": "username",
	})
	return r
}

func (r *shareRepository) Delete(id string) error {
	return r.deleteOwned(id)
}

func (r *shareRepository) selectShare(options ...model.QueryOptions) SelectBuilder {
	return r.newSelect(options...).Join("user u on u.id = share.user_id").
		Columns("share.*", "user_name as username").
		Where(r.addRestriction())
}

func (r *shareRepository) Exists(id string) (bool, error) {
	return r.exists(r.addRestriction(And{Eq{"id": id}}))
}

func (r *shareRepository) Get(id string) (*model.Share, error) {
	sel := r.selectShare().Where(Eq{"share.id": id})
	var res model.Share
	err := r.queryOne(sel, &res)
	if err != nil {
		return nil, err
	}
	err = r.loadMedia(&res)
	return &res, err
}

func (r *shareRepository) GetAll(options ...model.QueryOptions) (model.Shares, error) {
	sq := r.selectShare(options...)
	res := model.Shares{}
	err := r.queryAll(sq, &res)
	if err != nil {
		return nil, err
	}
	for i := range res {
		err = r.loadMedia(&res[i])
		if err != nil {
			return nil, fmt.Errorf("error loading media for share %s: %w", res[i].ID, err)
		}
	}
	return res, err
}

func (r *shareRepository) loadMedia(share *model.Share) error {
	var err error
	ids := strings.Split(share.ResourceIDs, ",")
	if len(ids) == 0 {
		return nil
	}
	noMissing := func(cond Sqlizer) Sqlizer {
		return And{cond, Eq{"missing": false}}
	}
	switch share.ResourceType {
	case "artist":
		// Match by album-artist participation, not the deprecated album_artist_id
		// column (first album artist only), so co-album-artists are included too.
		// Load as the share owner so their library access is applied.
		ctx, err := r.ownerContext(share)
		if err != nil {
			return err
		}
		albumRepo := NewAlbumRepository(ctx, r.db)
		share.Albums, err = albumRepo.GetAll(model.QueryOptions{Filters: noMissing(ParticipantIDFilter("album", ids, model.RoleAlbumArtist)), Sort: "artist"})
		if err != nil {
			return err
		}
		mfRepo := NewMediaFileRepository(ctx, r.db)
		share.Tracks, err = mfRepo.GetAll(model.QueryOptions{Filters: noMissing(ParticipantIDFilter("media_file", ids, model.RoleAlbumArtist)), Sort: "artist"})
		return err
	case "album":
		albumRepo := NewAlbumRepository(r.ctx, r.db)
		share.Albums, err = albumRepo.GetAll(model.QueryOptions{Filters: noMissing(Eq{"album.id": ids})})
		if err != nil {
			return err
		}
		mfRepo := NewMediaFileRepository(r.ctx, r.db)
		share.Tracks, err = mfRepo.GetAll(model.QueryOptions{Filters: noMissing(Eq{"album_id": ids}), Sort: "album"})
		return err
	case "playlist":
		// Load tracks as the share owner so their library access is applied.
		ctx, err := r.ownerContext(share)
		if err != nil {
			return err
		}
		plsRepo := NewPlaylistRepository(ctx, r.db)
		// Tracks returns nil when the playlist is no longer visible to the owner
		// (e.g. it was made private after the share was created); leave the share
		// with no tracks rather than exposing it.
		trackRepo := plsRepo.Tracks(ids[0], true)
		if trackRepo == nil {
			return nil
		}
		tracks, err := trackRepo.GetAll(model.QueryOptions{Sort: "id", Filters: noMissing(Eq{})})
		if err != nil {
			return err
		}
		share.Tracks = tracks.MediaFiles()
		return nil
	case "media_file":
		mfRepo := NewMediaFileRepository(r.ctx, r.db)
		tracks, err := mfRepo.GetAll(model.QueryOptions{Filters: noMissing(Eq{"media_file.id": ids})})
		share.Tracks = sortByIdPosition(tracks, ids)
		return err
	}
	log.Warn(r.ctx, "Unsupported Share ResourceType", "share", share.ID, "resourceType", share.ResourceType)
	return nil
}

// ownerContext returns a context scoped to the share owner, so repository
// queries apply the owner's library access when a public share is rendered.
func (r *shareRepository) ownerContext(share *model.Share) (context.Context, error) {
	owner, err := NewUserRepository(r.ctx, r.db).Get(share.UserID)
	if err != nil {
		return nil, fmt.Errorf("loading share owner %q: %w", share.UserID, err)
	}
	if owner == nil {
		return nil, fmt.Errorf("share owner %q not found", share.UserID)
	}
	return request.WithUser(r.ctx, *owner), nil
}

func sortByIdPosition(mfs model.MediaFiles, ids []string) model.MediaFiles {
	m := map[string]int{}
	for i, mf := range mfs {
		m[mf.ID] = i
	}
	var sorted model.MediaFiles
	for _, id := range ids {
		if idx, ok := m[id]; ok {
			sorted = append(sorted, mfs[idx])
		}
	}
	return sorted
}

func (r *shareRepository) Update(id string, entity any, cols ...string) error {
	s := entity.(*model.Share)
	s.ID = id
	s.UpdatedAt = time.Now()
	if len(cols) > 0 {
		cols = append(cols, "updated_at")
	}
	return r.updateOwned(id, s, cols...)
}

func (r *shareRepository) Save(entity any) (string, error) {
	s := entity.(*model.Share)
	// TODO Validate record
	u := loggedUser(r.ctx)
	if s.UserID == "" {
		s.UserID = u.ID
	}
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()
	id, err := r.put(s.ID, s)
	if errors.Is(err, model.ErrNotFound) {
		return "", rest.ErrNotFound
	}
	return id, err
}

func (r *shareRepository) CountAll(options ...model.QueryOptions) (int64, error) {
	return r.count(r.selectShare(), options...)
}

// RotateEpoch atomically bumps the share's access generation. Like updateOwned,
// the WHERE clause carries the ownership restriction, so a share owned by
// another user simply does not match. The freshly stored epoch is read back and
// returned.
func (r *shareRepository) RotateEpoch(id string) (int, error) {
	update := Update(r.tableName).
		Where(r.addRestriction(Eq{"id": id})).
		SetMap(map[string]any{
			"epoch":      Expr("epoch + 1"),
			"updated_at": time.Now(),
		})
	count, err := r.executeSQL(update)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, r.classifyOwnedWriteMiss(id)
	}

	var res struct{ Epoch int }
	if err := r.queryOne(r.newSelect().Columns("epoch").Where(Eq{"id": id}), &res); err != nil {
		return 0, err
	}
	return res.Epoch, nil
}

// SettleDownload records one fully delivered archive download. The conditional
// WHERE clause makes the increment a no-op once the cumulative budget is
// exhausted, so racing settlements can never push download_count past
// max_downloads. It is deliberately not ownership-restricted: the caller is the
// anonymous public download path.
func (r *shareRepository) SettleDownload(id string) (bool, error) {
	update := Update(r.tableName).
		Where(Eq{"id": id}).
		Where("(max_downloads = 0 OR download_count < max_downloads)").
		Set("download_count", Expr("download_count + 1"))
	count, err := r.executeSQL(update)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *shareRepository) Count(options ...rest.QueryOptions) (int64, error) {
	return r.CountAll(r.parseRestOptions(r.ctx, options...))
}

func (r *shareRepository) EntityName() string {
	return "share"
}

func (r *shareRepository) NewInstance() any {
	return &model.Share{}
}

func (r *shareRepository) Read(id string) (any, error) {
	sel := r.selectShare().Where(Eq{"share.id": id})
	var res model.Share
	err := r.queryOne(sel, &res)
	return &res, err
}

func (r *shareRepository) ReadAll(options ...rest.QueryOptions) (any, error) {
	sq := r.selectShare(r.parseRestOptions(r.ctx, options...))
	res := model.Shares{}
	err := r.queryAll(sq, &res)
	return res, err
}

var _ model.ShareRepository = (*shareRepository)(nil)
var _ rest.Repository = (*shareRepository)(nil)
var _ rest.Persistable = (*shareRepository)(nil)
