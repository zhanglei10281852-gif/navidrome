package persistence

import (
	"context"
	"fmt"

	. "github.com/Masterminds/squirrel"
	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/model"
	"github.com/pocketbase/dbx"
)

type transcodingRepository struct {
	sqlRepository
}

func NewTranscodingRepository(db dbx.Builder) model.TranscodingRepository {
	r := &transcodingRepository{}
	r.db = db
	r.registerModel(&model.Transcoding{}, nil)
	return r
}

func (r *transcodingRepository) Get(ctx context.Context, id string) (*model.Transcoding, error) {
	sel := r.newSelect(ctx).Columns("*").Where(Eq{"id": id})
	var res model.Transcoding
	err := r.queryOne(ctx, sel, &res)
	return &res, err
}

func (r *transcodingRepository) CountAll(ctx context.Context, qo ...model.QueryOptions) (int64, error) {
	return r.count(ctx, Select(), qo...)
}

// All returns every transcoding row with its Command field intact. It is a
// server-side lookup, so it is not subject to the REST command redaction.
func (r *transcodingRepository) All(ctx context.Context) (model.Transcodings, error) {
	sel := r.newSelect(ctx).Columns("*")
	res := model.Transcodings{}
	err := r.queryAll(ctx, sel, &res)
	return res, err
}

// CountPlayers returns how many players are pinned to the transcoding.
func (r *transcodingRepository) CountPlayers(ctx context.Context, transcodingID string) (int64, error) {
	sel := Select("count(*) as count").From("player").Where(Eq{"transcoding_id": transcodingID})
	var res struct {
		Count int64
	}
	if err := r.queryOne(ctx, sel, &res); err != nil {
		return 0, err
	}
	return res.Count, nil
}

func (r *transcodingRepository) FindByFormat(ctx context.Context, format string) (*model.Transcoding, error) {
	sel := r.newSelect(ctx).Columns("*").Where(Eq{"target_format": format})
	var res model.Transcoding
	err := r.queryOne(ctx, sel, &res)
	return &res, err
}

func (r *transcodingRepository) Put(ctx context.Context, t *model.Transcoding) error {
	if !loggedUser(ctx).IsAdmin {
		return rest.ErrPermissionDenied
	}
	_, err := r.put(ctx, t.ID, t)
	return err
}

func (r *transcodingRepository) Count(ctx context.Context, options ...rest.QueryOptions) (int64, error) {
	return r.count(ctx, Select(), r.parseRestOptions(ctx, options...))
}

func (r *transcodingRepository) Read(ctx context.Context, id string) (*model.Transcoding, error) {
	res, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !loggedUser(ctx).IsAdmin {
		res.Command = ""
	}
	return res, nil
}

func (r *transcodingRepository) ReadAll(ctx context.Context, options ...rest.QueryOptions) ([]model.Transcoding, error) {
	sel := r.newSelect(ctx, r.parseRestOptions(ctx, options...)).Columns("*")
	res := model.Transcodings{}
	err := r.queryAll(ctx, sel, &res)
	if err != nil {
		return nil, err
	}
	if !loggedUser(ctx).IsAdmin {
		for i := range res {
			res[i].Command = ""
		}
	}
	return res, nil
}

func (r *transcodingRepository) Save(ctx context.Context, t *model.Transcoding) (string, error) {
	if !loggedUser(ctx).IsAdmin {
		return "", rest.ErrPermissionDenied
	}
	return r.put(ctx, t.ID, t)
}

func (r *transcodingRepository) Update(ctx context.Context, id string, entity model.Transcoding, cols ...string) error {
	if !loggedUser(ctx).IsAdmin {
		return rest.ErrPermissionDenied
	}
	t := &entity
	t.ID = id
	_, err := r.put(ctx, id, t)
	return err
}

func (r *transcodingRepository) Delete(ctx context.Context, ids ...string) error {
	if !loggedUser(ctx).IsAdmin {
		return rest.ErrPermissionDenied
	}
	// Commit boundary: verify every referenced id up front, so a batch delete
	// is all-or-nothing and a profile still pinned to a player is rejected
	// deterministically at admin time instead of leaving dangling player
	// references that later fail playback at random.
	for _, id := range ids {
		players, err := r.CountPlayers(ctx, id)
		if err != nil {
			return err
		}
		if players > 0 {
			return &rest.ValidationError{Errors: map[string]string{
				"id": fmt.Sprintf("cannot delete transcoding in use by %d player(s); reassign those players first", players),
			}}
		}
	}
	for _, id := range ids {
		if err := r.deleteByID(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

var _ model.TranscodingRepository = (*transcodingRepository)(nil)
var _ rest.Repository[model.Transcoding] = (*transcodingRepository)(nil)
var _ rest.Persistable[model.Transcoding] = (*transcodingRepository)(nil)
