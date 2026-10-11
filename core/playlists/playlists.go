package playlists

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/artwork"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
)

type Playlists interface {
	// Reads
	GetAll(ctx context.Context, options ...model.QueryOptions) (model.Playlists, error)
	Get(ctx context.Context, id string) (*model.Playlist, error)
	GetWithTracks(ctx context.Context, id string) (*model.Playlist, error)
	Tracks(ctx context.Context, id string) (model.PlaylistTrackRepository, error)
	GetPlaylists(ctx context.Context, mediaFileId string) (model.Playlists, error)

	// Mutations
	Create(ctx context.Context, playlistId string, name string, ids []string) (string, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, playlistID string, name *string, comment *string, public *bool, idsToAdd []string, idxToRemove []int) error

	// Track management
	AddTracks(ctx context.Context, playlistID string, ids []string) (int, error)
	InsertTracks(ctx context.Context, playlistID string, ids []string, pos int) (int, error)
	AddAlbums(ctx context.Context, playlistID string, albumIds []string) (int, error)
	AddArtists(ctx context.Context, playlistID string, artistIds []string) (int, error)
	AddDiscs(ctx context.Context, playlistID string, discs []model.DiscID) (int, error)
	RemoveTracks(ctx context.Context, playlistID string, trackIds []string) error
	ReorderTrack(ctx context.Context, playlistID string, pos int, newPos int) error

	// Cover art
	SetImage(ctx context.Context, playlistID string, reader io.Reader, ext string) error
	RemoveImage(ctx context.Context, playlistID string) error

	// Import
	ImportFile(ctx context.Context, absolutePath string, sync bool) (*model.Playlist, error)
	ImportFromFolder(ctx context.Context, folder *model.Folder, filename string) (*model.Playlist, error)
	ImportM3U(ctx context.Context, reader io.Reader) (*model.Playlist, error)

	// REST adapters
	Repository() rest.Repository[model.Playlist]
	TracksRepository(ctx context.Context, playlistId string, refreshSmartPlaylist bool) rest.Repository[model.PlaylistTrack]
}

// ImageUploadService is the cover-commit contract satisfied by artwork.Uploader.
type ImageUploadService interface {
	SetImage(ctx context.Context, entityType, entityID, name string, reader io.Reader, ext string, commit artwork.CoverCommit) error
	RemoveImage(ctx context.Context, entityType, entityID string, commit artwork.CoverCommit) error
}

type playlists struct {
	ds        model.DataStore
	imgUpload ImageUploadService
	repo      *playlistRepositoryWrapper
}

func NewPlaylists(ds model.DataStore, imgUpload ImageUploadService) Playlists {
	s := &playlists{ds: ds, imgUpload: imgUpload}
	s.repo = &playlistRepositoryWrapper{PlaylistRepository: ds.Playlist(), service: s}
	return s
}

func InPath(folder model.Folder) bool {
	if conf.Server.PlaylistsPath == "" {
		return true
	}
	rel, _ := filepath.Rel(folder.LibraryPath, folder.AbsolutePath())
	for path := range strings.SplitSeq(conf.Server.PlaylistsPath, string(filepath.ListSeparator)) {
		if match, _ := doublestar.Match(path, rel); match {
			return true
		}
	}
	return false
}

// --- Read operations ---

func (s *playlists) GetAll(ctx context.Context, options ...model.QueryOptions) (model.Playlists, error) {
	return s.ds.Playlist().GetAll(ctx, options...)
}

func (s *playlists) Get(ctx context.Context, id string) (*model.Playlist, error) {
	return s.ds.Playlist().Get(ctx, id)
}

func (s *playlists) GetWithTracks(ctx context.Context, id string) (*model.Playlist, error) {
	return s.ds.Playlist().GetWithTracks(ctx, id, true, false)
}

func (s *playlists) GetPlaylists(ctx context.Context, mediaFileId string) (model.Playlists, error) {
	return s.ds.Playlist().GetPlaylists(ctx, mediaFileId)
}

// Tracks scopes a repository to one playlist's tracks, for callers that page or stream them rather
// than loading every one like GetWithTracks. Gets first because PlaylistRepository.Tracks discards
// its error behind a nil (and warns), and this is probed with ids that are usually not playlists.
func (s *playlists) Tracks(ctx context.Context, id string) (model.PlaylistTrackRepository, error) {
	repo := s.ds.Playlist()
	if _, err := repo.Get(ctx, id); err != nil {
		return nil, err
	}
	tracks := repo.Tracks(ctx, id, true)
	if tracks == nil {
		return nil, model.ErrNotFound
	}
	return tracks, nil
}

// --- Mutation operations ---

// Create creates a new playlist (when name is provided) or replaces tracks on an existing
// playlist (when playlistId is provided). This matches the Subsonic createPlaylist semantics.
func (s *playlists) Create(ctx context.Context, playlistId string, name string, ids []string) (string, error) {
	usr, _ := request.UserFrom(ctx)
	err := s.ds.WithTxImmediate(func(tx model.DataStore) error {
		var pls *model.Playlist
		var err error

		if playlistId != "" {
			pls, err = tx.Playlist().Get(ctx, playlistId)
			if err != nil {
				return err
			}
			// Ownership first: a non-owner must get ErrNotAuthorized, not a read-only conflict.
			if !usr.IsAdmin && pls.OwnerID != usr.ID {
				return model.ErrNotAuthorized
			}
			if !pls.TracksEditable() {
				return model.ErrPlaylistNotEditable
			}
		} else {
			pls = &model.Playlist{Name: name}
			pls.OwnerID = usr.ID
		}
		pls.Tracks = nil
		pls.AddMediaFilesByID(ids)

		err = tx.Playlist().Put(ctx, pls)
		playlistId = pls.ID
		return err
	})
	return playlistId, err
}

func (s *playlists) Delete(ctx context.Context, id string) error {
	pls, err := s.checkWritable(ctx, id)
	if err != nil {
		return err
	}

	// Remove the entity (and its image reference) before unlinking the cover file, so a failed
	// deletion can never leave a row pointing at a missing file; the periodic sweep reclaims the
	// file when the unlink fails.
	coverPath := pls.UploadedImagePath()
	if err := s.ds.Playlist().Delete(ctx, id); err != nil {
		return err
	}
	if coverPath != "" {
		if err := os.Remove(coverPath); err != nil && !os.IsNotExist(err) {
			log.Warn(ctx, "Failed to remove playlist image on delete", "path", coverPath, err)
		}
	}
	return nil
}

func (s *playlists) Update(ctx context.Context, playlistID string,
	name *string, comment *string, public *bool,
	idsToAdd []string, idxToRemove []int) error {
	var pls *model.Playlist
	var err error
	hasTrackChanges := len(idsToAdd) > 0 || len(idxToRemove) > 0
	if hasTrackChanges {
		pls, err = s.checkTracksEditable(ctx, playlistID)
	} else {
		pls, err = s.checkWritable(ctx, playlistID)
	}
	if err != nil {
		return err
	}
	return s.ds.WithTxImmediate(func(tx model.DataStore) error {
		repo := tx.Playlist()

		if len(idxToRemove) > 0 {
			tracksRepo := repo.Tracks(ctx, playlistID, false)
			// Convert 0-based indices to 1-based position IDs and delete them directly,
			// avoiding the need to load all tracks into memory.
			positions := make([]string, len(idxToRemove))
			for i, idx := range idxToRemove {
				positions[i] = strconv.Itoa(idx + 1)
			}
			if err := tracksRepo.Delete(ctx, positions...); err != nil {
				return err
			}
			if len(idsToAdd) > 0 {
				if _, err := tracksRepo.Add(ctx, idsToAdd); err != nil {
					return err
				}
			}
			return s.updateMetadata(ctx, tx, pls, name, comment, public)
		}

		if len(idsToAdd) > 0 {
			if _, err := repo.Tracks(ctx, playlistID, false).Add(ctx, idsToAdd); err != nil {
				return err
			}
		}
		if name == nil && comment == nil && public == nil {
			return nil
		}
		// Reuse the playlist from checkWritable (no tracks loaded, so Put only refreshes counters)
		return s.updateMetadata(ctx, tx, pls, name, comment, public)
	})
}

// --- Permission helpers ---

// checkWritable fetches the playlist and verifies the current user can modify it.
func (s *playlists) checkWritable(ctx context.Context, id string) (*model.Playlist, error) {
	pls, err := s.ds.Playlist().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	usr, _ := request.UserFrom(ctx)
	if !usr.IsAdmin && pls.OwnerID != usr.ID {
		return nil, model.ErrNotAuthorized
	}
	return pls, nil
}

// checkTracksEditable verifies the user owns the playlist and its tracks are editable.
func (s *playlists) checkTracksEditable(ctx context.Context, playlistID string) (*model.Playlist, error) {
	pls, err := s.checkWritable(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	if !pls.TracksEditable() {
		return nil, model.ErrPlaylistNotEditable
	}
	return pls, nil
}

// updateMetadata applies optional metadata changes to a playlist and persists it.
// Accepts a DataStore parameter so it can be used inside transactions.
// The caller is responsible for permission checks.
func (s *playlists) updateMetadata(ctx context.Context, ds model.DataStore, pls *model.Playlist, name *string, comment *string, public *bool) error {
	if name != nil {
		pls.Name = *name
	}
	if comment != nil {
		pls.Comment = *comment
	}
	if public != nil {
		pls.Public = *public
	}
	return ds.Playlist().Put(ctx, pls)
}

// --- Track management operations ---

func (s *playlists) AddTracks(ctx context.Context, playlistID string, ids []string) (int, error) {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return 0, err
	}
	return s.ds.Playlist().Tracks(ctx, playlistID, false).Add(ctx, ids)
}

// InsertTracks adds tracks before the 1-based position pos; a position past the end appends.
func (s *playlists) InsertTracks(ctx context.Context, playlistID string, ids []string, pos int) (int, error) {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return 0, err
	}
	var count int
	// Immediate: a deferred tx that reads before shifting fails at once with SQLITE_BUSY under
	// concurrent writers instead of waiting for the lock.
	err := s.ds.WithTxImmediate(func(tx model.DataStore) error {
		var err error
		count, err = tx.Playlist().Tracks(ctx, playlistID, false).Insert(ctx, ids, pos)
		return err
	})
	return count, err
}

func (s *playlists) AddAlbums(ctx context.Context, playlistID string, albumIds []string) (int, error) {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return 0, err
	}
	return s.ds.Playlist().Tracks(ctx, playlistID, false).AddAlbums(ctx, albumIds)
}

func (s *playlists) AddArtists(ctx context.Context, playlistID string, artistIds []string) (int, error) {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return 0, err
	}
	return s.ds.Playlist().Tracks(ctx, playlistID, false).AddArtists(ctx, artistIds)
}

func (s *playlists) AddDiscs(ctx context.Context, playlistID string, discs []model.DiscID) (int, error) {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return 0, err
	}
	return s.ds.Playlist().Tracks(ctx, playlistID, false).AddDiscs(ctx, discs)
}

func (s *playlists) RemoveTracks(ctx context.Context, playlistID string, trackIds []string) error {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return err
	}
	return s.ds.WithTx(func(tx model.DataStore) error {
		return tx.Playlist().Tracks(ctx, playlistID, false).Delete(ctx, trackIds...)
	})
}

func (s *playlists) ReorderTrack(ctx context.Context, playlistID string, pos int, newPos int) error {
	if _, err := s.checkTracksEditable(ctx, playlistID); err != nil {
		return err
	}
	return s.ds.WithTxImmediate(func(tx model.DataStore) error {
		return tx.Playlist().Tracks(ctx, playlistID, false).Reorder(ctx, pos, newPos)
	})
}

// --- Cover art operations ---

func (s *playlists) SetImage(ctx context.Context, playlistID string, reader io.Reader, ext string) error {
	pls, err := s.checkWritable(ctx, playlistID)
	if err != nil {
		return err
	}

	commit := artwork.CoverCommit{
		Current: s.currentImage(playlistID),
		Persist: func(ctx context.Context, filename string) error {
			pls.UploadedImage = filename
			return s.ds.Playlist().Put(ctx, pls)
		},
	}
	return s.imgUpload.SetImage(ctx, consts.EntityPlaylist, pls.ID, pls.Name, reader, ext, commit)
}

func (s *playlists) RemoveImage(ctx context.Context, playlistID string) error {
	pls, err := s.checkWritable(ctx, playlistID)
	if err != nil {
		return err
	}

	commit := artwork.CoverCommit{
		Current: s.currentImage(playlistID),
		Persist: func(ctx context.Context, _ string) error {
			pls.UploadedImage = ""
			return s.ds.Playlist().Put(ctx, pls)
		},
	}
	return s.imgUpload.RemoveImage(ctx, consts.EntityPlaylist, pls.ID, commit)
}

// currentImage returns a hook reading the playlist's persisted cover filename, so the commit
// re-checks the live reference under its lock before reclaiming any file.
func (s *playlists) currentImage(playlistID string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		current, err := s.ds.Playlist().Get(ctx, playlistID)
		if err != nil {
			return "", err
		}
		return current.UploadedImage, nil
	}
}
