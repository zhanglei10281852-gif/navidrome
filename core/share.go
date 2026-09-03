package core

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	. "github.com/navidrome/navidrome/utils/gg"
	"github.com/navidrome/navidrome/utils/nanoid"
	"github.com/navidrome/navidrome/utils/slice"
	"github.com/navidrome/navidrome/utils/str"
)

type Share interface {
	Load(ctx context.Context, id string) (*model.Share, error)
	NewRepository(ctx context.Context) rest.Repository
	// Rotate bumps the share's access generation and revokes all in-flight
	// plays and downloads derived from earlier generations.
	Rotate(ctx context.Context, id string) (int, error)
	// AcquirePlay reserves one concurrent-play slot. The returned context is
	// canceled when the slot is released or the share is rotated; the release
	// function must be called exactly once when the request ends.
	AcquirePlay(ctx context.Context, s *model.Share) (context.Context, context.CancelFunc, error)
	// ReserveDownload reserves one cumulative-download slot. The lease's
	// Commit is called only after a full archive delivery, Release on every
	// other outcome; the two share a single idempotent guard.
	ReserveDownload(ctx context.Context, s *model.Share) (*DownloadLease, error)
	// ActivePlays reports the number of streams currently playing for a share.
	ActivePlays(shareID string) int
}

func NewShare(ds model.DataStore) Share {
	return &shareService{
		ds:      ds,
		budgets: map[string]*shareBudget{},
	}
}

type shareService struct {
	ds        model.DataStore
	budgetsMu sync.Mutex
	budgets   map[string]*shareBudget
}

func (s *shareService) Load(ctx context.Context, id string) (*model.Share, error) {
	repo := s.ds.Share(ctx)
	share, err := repo.Get(id)
	if err != nil {
		return nil, err
	}
	expiresAt := V(share.ExpiresAt)
	if !expiresAt.IsZero() && expiresAt.Before(time.Now()) {
		return nil, model.ErrExpired
	}
	share.LastVisitedAt = new(time.Now())
	share.VisitCount++

	err = repo.(rest.Persistable).Update(id, share, "last_visited_at", "visit_count")
	if err != nil {
		log.Warn(ctx, "Could not increment visit count for share", "share", share.ID)
	}
	return share, nil
}

func (s *shareService) NewRepository(ctx context.Context) rest.Repository {
	repo := s.ds.Share(ctx)
	wrapper := &shareRepositoryWrapper{
		ShareRepository: repo,
		Repository:      repo.(rest.Repository),
		Persistable:     repo.(rest.Persistable),
		ctx:             ctx,
		ds:              s.ds,
		activePlays:     s.ActivePlays,
	}
	return wrapper
}

type shareRepositoryWrapper struct {
	model.ShareRepository
	rest.Repository
	rest.Persistable
	ctx         context.Context
	ds          model.DataStore
	activePlays func(shareID string) int
}

// Get/GetAll enrich the persisted shares with the live number of active
// streams, so owner-facing UIs/APIs can show current usage.
func (r *shareRepositoryWrapper) Get(id string) (*model.Share, error) {
	sh, err := r.ShareRepository.Get(id)
	if err != nil {
		return nil, err
	}
	sh.ActivePlays = r.activePlays(id)
	return sh, nil
}

func (r *shareRepositoryWrapper) GetAll(options ...model.QueryOptions) (model.Shares, error) {
	shares, err := r.ShareRepository.GetAll(options...)
	if err != nil {
		return nil, err
	}
	for i := range shares {
		shares[i].ActivePlays = r.activePlays(shares[i].ID)
	}
	return shares, nil
}

func (r *shareRepositoryWrapper) newId() (string, error) {
	for {
		id, err := nanoid.Generate("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", 10)
		if err != nil {
			return "", err
		}
		exists, err := r.Exists(id)
		if err != nil {
			return "", err
		}
		if !exists {
			return id, nil
		}
	}
}

func (r *shareRepositoryWrapper) Save(entity any) (string, error) {
	s := entity.(*model.Share)
	id, err := r.newId()
	if err != nil {
		return "", err
	}
	s.ID = id
	if V(s.ExpiresAt).IsZero() {
		s.ExpiresAt = new(time.Now().Add(conf.Server.DefaultShareExpiration))
	}

	firstId, _, _ := strings.Cut(s.ResourceIDs, ",")
	v, err := model.GetEntityByID(r.ctx, r.ds, firstId)
	if err != nil {
		return "", err
	}
	switch v.(type) {
	case *model.Artist:
		s.ResourceType = "artist"
		s.Contents = r.contentsLabelFromArtist(s.ID, s.ResourceIDs)
	case *model.Album:
		s.ResourceType = "album"
		s.Contents = r.contentsLabelFromAlbums(s.ID, s.ResourceIDs)
	case *model.Playlist:
		s.ResourceType = "playlist"
		s.Contents = r.contentsLabelFromPlaylist(s.ID, s.ResourceIDs)
	case *model.MediaFile:
		s.ResourceType = "media_file"
		s.Contents = r.contentsLabelFromMediaFiles(s.ID, s.ResourceIDs)
	default:
		log.Error(r.ctx, "Invalid Resource ID", "id", firstId)
		return "", model.ErrNotFound
	}

	s.Contents = str.TruncateRunes(s.Contents, 30, "...")

	return r.Persistable.Save(s)
}

func (r *shareRepositoryWrapper) Update(id string, entity any, _ ...string) error {
	// Epoch and download_count are intentionally not writable here: they are
	// server-managed through Rotate and the download settlement path.
	cols := []string{"description", "downloadable", "max_concurrent_plays", "max_downloads"}

	// TODO Better handling of Share expiration
	if !V(entity.(*model.Share).ExpiresAt).IsZero() {
		cols = append(cols, "expires_at")
	}
	return r.Persistable.Update(id, entity, cols...)
}

func (r *shareRepositoryWrapper) contentsLabelFromArtist(shareID string, ids string) string {
	idList := strings.SplitN(ids, ",", 2)
	a, err := r.ds.Artist(r.ctx).Get(idList[0])
	if err != nil {
		log.Error(r.ctx, "Error retrieving artist name for share", "share", shareID, err)
		return ""
	}
	return a.Name
}

func (r *shareRepositoryWrapper) contentsLabelFromAlbums(shareID string, ids string) string {
	idList := strings.Split(ids, ",")
	all, err := r.ds.Album(r.ctx).GetAll(model.QueryOptions{Filters: squirrel.Eq{"album.id": idList}})
	if err != nil {
		log.Error(r.ctx, "Error retrieving album names for share", "share", shareID, err)
		return ""
	}
	names := slice.Map(all, func(a model.Album) string { return a.Name })
	return strings.Join(names, ", ")
}
func (r *shareRepositoryWrapper) contentsLabelFromPlaylist(shareID string, id string) string {
	pls, err := r.ds.Playlist(r.ctx).Get(id)
	if err != nil {
		log.Error(r.ctx, "Error retrieving album names for share", "share", shareID, err)
		return ""
	}
	return pls.Name
}

func (r *shareRepositoryWrapper) contentsLabelFromMediaFiles(shareID string, ids string) string {
	idList := strings.Split(ids, ",")
	mfs, err := r.ds.MediaFile(r.ctx).GetAll(model.QueryOptions{Filters: squirrel.And{
		squirrel.Eq{"media_file.id": idList},
		squirrel.Eq{"missing": false},
	}})
	if err != nil {
		log.Error(r.ctx, "Error retrieving media files for share", "share", shareID, err)
		return ""
	}

	if len(mfs) == 1 {
		return mfs[0].Title
	}

	albums := slice.Group(mfs, func(mf model.MediaFile) string {
		return mf.Album
	})
	if len(albums) == 1 {
		for name := range albums {
			return name
		}
	}
	artists := slice.Group(mfs, func(mf model.MediaFile) string {
		return mf.AlbumArtist
	})
	if len(artists) == 1 {
		for name := range artists {
			return name
		}
	}

	return mfs[0].Title
}
