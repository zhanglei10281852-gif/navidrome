package artwork

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils"
)

// MaxImageUploadSize returns the configured max upload size in bytes, or the built-in default.
func MaxImageUploadSize() int64 {
	return parseSize(conf.Server.MaxImageUploadSize, consts.DefaultMaxImageUploadSize)
}

func parseSize(value, fallback string) int64 {
	if size, err := humanize.ParseBytes(value); err == nil && size > 0 {
		return int64(size)
	}
	size, _ := humanize.ParseBytes(fallback)
	return int64(size)
}

// CoverCommit carries the per-entity persistence hooks that drive one recoverable cover
// change (replacement or removal). The Uploader owns file staging, reference publication and
// artwork-state ordering; the hooks only touch the entity row.
type CoverCommit struct {
	// Current returns the uploaded-image filename persisted on the entity right now ("" if none).
	Current func(ctx context.Context) (string, error)
	// Persist stores filename as the entity's cover reference; "" clears it. It must be safe to
	// call from the serialized cover commit, and durable by the time it returns nil.
	Persist func(ctx context.Context, filename string) error
}

// Uploader stores a user-uploaded entity image through a recoverable commit and refreshes the
// entity's artwork state so resolution and reads converge on the new reference.
type Uploader interface {
	// SetImage runs one atomic, recoverable cover replacement: the upload is fully written to a
	// candidate first, the complete image is published, and the entity reference is persisted;
	// until the reference is durable, clients and resolution keep seeing the previous cover.
	SetImage(ctx context.Context, entityType, entityID, name string, reader io.Reader, ext string, commit CoverCommit) error
	// RemoveImage persists the empty reference first, refreshes artwork state, and only then
	// unlinks the file, so the database can never name a deleted image.
	RemoveImage(ctx context.Context, entityType, entityID string, commit CoverCommit) error
	// EnqueueArtwork re-resolves the item's artwork. Call it AFTER persisting a new filename
	// outside a SetImage commit, or the worker resolves the old one.
	EnqueueArtwork(ctx context.Context, entityType, entityID string)
}

var uploadEntityKind = map[string]model.Kind{
	consts.EntityArtist:   model.KindArtistArtwork,
	consts.EntityPlaylist: model.KindPlaylistArtwork,
	consts.EntityRadio:    model.KindRadioArtwork,
}

// uploadCoverFileMode matches the mode the previous direct os.Create uploads settled to under
// the usual process umask, so first-upload file semantics do not change.
const uploadCoverFileMode = 0o644

// stagedCandidateTail marks an upload candidate: names look like ".<final>.tmp<random>".
// The leading dot and non-image tail also keep resolution from ever opening a candidate.
const stagedCandidateTail = ".tmp*"

func isStagedCandidate(name string) bool {
	return strings.HasPrefix(name, ".") && strings.Contains(name, strings.TrimSuffix(stagedCandidateTail, "*"))
}

type uploader struct {
	ds model.DataStore
}

func NewUploader(ds model.DataStore) Uploader {
	return &uploader{ds: ds}
}

// stagedCover is one fully-written candidate file awaiting its atomic publication.
type stagedCover struct {
	tmpPath   string
	finalPath string
	// mtime captured after the complete write; rename preserves it, so a later commit that
	// replaced the same final path is detectable.
	mtime time.Time
}

// stageCover copies the whole upload into a uniquely named candidate in the target directory
// and closes it before returning. A failed stream never leaves readable bytes at a cover name.
func stageCover(ctx context.Context, finalPath string, reader io.Reader) (*stagedCover, error) {
	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating image directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(finalPath)+stagedCandidateTail)
	if err != nil {
		return nil, fmt.Errorf("creating image file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("writing image file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("writing image file: %w", err)
	}
	// CreateTemp modes files 0600; match the historical upload mode and ignore a chmod failure
	// (the file is still readable by this process and gets reclaimed on the next sweep).
	if err := os.Chmod(tmpName, uploadCoverFileMode); err != nil {
		log.Warn(ctx, "Artwork: Could not chmod staged image", "path", tmpName, err)
	}
	info, err := os.Stat(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("stating staged image file: %w", err)
	}
	return &stagedCover{tmpPath: tmpName, finalPath: finalPath, mtime: info.ModTime()}, nil
}

// publish atomically places the complete candidate at its final name.
func (c *stagedCover) publish() error {
	if err := os.Rename(c.tmpPath, c.finalPath); err != nil {
		return err
	}
	c.tmpPath = ""
	return nil
}

// discard removes a candidate that never published; an already-published candidate lives at
// its final name and is handled by commit rollback/reclaim instead.
func (c *stagedCover) discard(ctx context.Context) {
	if c == nil || c.tmpPath == "" {
		return
	}
	if err := os.Remove(c.tmpPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn(ctx, "Artwork: Could not remove staged image; periodic sweep will retry",
			"path", c.tmpPath, err)
	}
}

func (s *uploader) SetImage(ctx context.Context, entityType, entityID, name string, reader io.Reader, ext string, commit CoverCommit) error {
	filename := imageFilename(entityID, name, ext)
	finalPath := model.UploadedImagePath(entityType, filename)

	unlock := lockCover(entityType, entityID)
	defer unlock()

	current, err := commit.Current(ctx)
	if err != nil {
		return err
	}
	oldPath := model.UploadedImagePath(entityType, current)
	oldMtime := mtimeOf(oldPath)
	// Same name and extension: publishing would overwrite the bytes the old reference names,
	// so this case persists the reference first and publishes only once it is durable.
	inPlace := oldPath != "" && oldPath == finalPath

	staged, err := stageCover(ctx, finalPath, reader)
	if err != nil {
		return err
	}

	// Phase 1 (distinct paths): publish before persisting. The reference still names the old,
	// untouched file, so every reader keeps serving the previous cover.
	published := false
	if !inPlace {
		if err := staged.publish(); err != nil {
			staged.discard(ctx)
			return fmt.Errorf("publishing image file: %w", err)
		}
		published = true
	}

	// Phase 2: persist the entity reference.
	if err := commit.Persist(ctx, filename); err != nil {
		if published {
			s.rollbackPublished(ctx, entityType, filename, staged.finalPath, staged.mtime, commit)
		} else {
			staged.discard(ctx)
		}
		return err
	}

	// Phase 1 (in place): the reference already names this path, so publishing now exposes only
	// the complete new image, never a partial one.
	if inPlace {
		if err := staged.publish(); err != nil {
			// The stored filename is unchanged and the old complete file still backs it, so the
			// reference stays coherent; drop the candidate and let the client retry.
			staged.discard(ctx)
			return fmt.Errorf("publishing image file: %w", err)
		}
	}

	// Phase 3: drop resolved state and re-queue so resolution converges on the new reference.
	stateCleared := s.refreshArtwork(ctx, entityType, entityID)

	// Phase 4: reclaim the replaced file once nothing can still read it.
	s.reclaimSuperseded(ctx, entityType, oldPath, finalPath, oldMtime, stateCleared, commit)
	return nil
}

func (s *uploader) RemoveImage(ctx context.Context, entityType, entityID string, commit CoverCommit) error {
	unlock := lockCover(entityType, entityID)
	defer unlock()

	current, err := commit.Current(ctx)
	if err != nil {
		return err
	}
	oldPath := model.UploadedImagePath(entityType, current)
	oldMtime := mtimeOf(oldPath)

	// Persist the empty reference first, so a failed update leaves a readable image and the
	// database never names a file that was deleted.
	if err := commit.Persist(ctx, ""); err != nil {
		return err
	}

	// Clear resolved state before unlinking: readers then resolve the non-custom sources
	// instead of hitting a dangling path.
	stateCleared := s.refreshArtwork(ctx, entityType, entityID)
	if oldPath == "" {
		return nil
	}
	// A concurrent replacement may have committed this exact reference again after our persist.
	if current, err := commit.Current(ctx); err == nil && model.UploadedImagePath(entityType, current) == oldPath {
		return nil
	}
	if !stateCleared {
		// item_artwork may still serve these bytes; the periodic sweep reclaims after convergence.
		return nil
	}
	if info, err := os.Stat(oldPath); err != nil || (oldMtime != 0 && info.ModTime().UnixNano() != oldMtime) {
		return nil
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// The reference is already gone, so this cannot dangle; the sweep finishes the unlink.
		log.Warn(ctx, "Artwork: Could not remove deleted cover file; periodic sweep will retry",
			"path", oldPath, err)
	}
	return nil
}

// rollbackPublished undoes a publish whose entity reference failed to persist. The fresh file is
// removed only while it is still the bytes this request wrote and no reference names it, so a
// concurrent or retried submission that committed later never loses its image.
func (s *uploader) rollbackPublished(ctx context.Context, entityType, filename, finalPath string, mtime time.Time, commit CoverCommit) {
	if current, err := commit.Current(ctx); err == nil && current == filename {
		return // another attempt already persisted this reference
	}
	info, err := os.Stat(finalPath)
	if err != nil {
		return
	}
	if !info.ModTime().Equal(mtime) {
		return // the file was replaced after our publish
	}
	if err := os.Remove(finalPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn(ctx, "Artwork: Could not roll back unpersisted image; periodic sweep will retry",
			"path", finalPath, err)
	}
}

// reclaimSuperseded unlinks the previous cover once the commit is complete and the old file is
// unreferenced by both the entity and the resolved-state row. Every guard re-checks live state,
// so a later successful submission cannot have its image deleted.
func (s *uploader) reclaimSuperseded(ctx context.Context, entityType, oldPath, finalPath string, oldMtime int64, stateCleared bool, commit CoverCommit) {
	if oldPath == "" || oldPath == finalPath || !stateCleared {
		return
	}
	if current, err := commit.Current(ctx); err == nil && model.UploadedImagePath(entityType, current) == oldPath {
		return
	}
	info, err := os.Stat(oldPath)
	if err != nil || (oldMtime != 0 && info.ModTime().UnixNano() != oldMtime) {
		return
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn(ctx, "Artwork: Superseded image kept; periodic sweep will retry", "path", oldPath, err)
	}
}

// refreshArtwork drops the item's resolved state and enqueues a Bump re-resolution. It reports
// whether the state clear succeeded: only cleared state can leave an old backing file unreferenced.
// The enqueue is still attempted when the clear failed — the worker upserts fresh state, which
// drives resolution to the new reference even without the delete.
func (s *uploader) refreshArtwork(ctx context.Context, entityType, id string) (stateCleared bool) {
	kind, ok := uploadEntityKind[entityType]
	if !ok {
		return false
	}
	if err := s.ds.Artwork().DeleteForItems(ctx, kind, []string{id}); err != nil {
		log.Warn(ctx, "Artwork: Could not clear artwork state after cover change", "kind", kind, "id", id, err)
	} else {
		stateCleared = true
	}
	item := model.ArtworkQueueItem{
		ItemKind: kind.Prefix(), ItemID: id,
		ImageType: model.ImageTypePrimary, Priority: model.ArtworkPriorityBump,
	}
	if err := s.ds.ArtworkQueue().Enqueue(ctx, item); err != nil {
		// Request-time read-through resolves the new reference and re-enqueues itself.
		log.Warn(ctx, "Artwork: Could not enqueue artwork refresh after cover change", "kind", kind, "id", id, err)
	}
	return stateCleared
}

func (s *uploader) EnqueueArtwork(ctx context.Context, entityType, id string) {
	kind, ok := uploadEntityKind[entityType]
	if !ok {
		return
	}
	if err := Refresh(ctx, s.ds, kind, id); err != nil {
		log.Warn(ctx, "Artwork: Could not refresh artwork after upload", "kind", kind, "id", id, err)
	}
}

// cover locks serialize commits for one entity within this process, so concurrent or retried
// replacements cannot interleave staging, persistence and old-file reclamation.
var (
	coverLocksMu sync.Mutex
	coverLocks   = map[string]*coverMutex{}
)

type coverMutex struct{ sync.Mutex }

func lockCover(entityType, entityID string) (unlock func()) {
	key := entityType + "\x00" + entityID
	coverLocksMu.Lock()
	mu, ok := coverLocks[key]
	if !ok {
		mu = &coverMutex{}
		coverLocks[key] = mu
	}
	coverLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func imageFilename(id, name, ext string) string {
	clean := utils.CleanFileName(name)
	if clean == "" {
		return id + ext
	}
	return id + "_" + clean + ext
}

// uploadedImageEntities lists the entity types whose covers live under <data>/artwork/<type>.
var uploadedImageEntities = []string{consts.EntityArtist, consts.EntityPlaylist, consts.EntityRadio}

// sweepUploadedImages reclaims cover candidates left by crashed/restarted submissions and
// uploaded files no entity references anymore. Anything younger than cutoff is spared: a commit
// can be between its publication and its reference persistence. It is the safety net behind the
// commit's own reclamation; the common case is unlinked inline.
func sweepUploadedImages(ctx context.Context, ds model.DataStore, cutoff time.Time) (int, error) {
	removed := 0
	for _, entityType := range uploadedImageEntities {
		live, err := liveUploadedFilenames(ctx, ds, entityType)
		if err != nil {
			return removed, err
		}
		dir := filepath.Join(conf.Server.DataFolder.String(), consts.ArtworkFolder, entityType)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			name := entry.Name()
			// Candidates are never named by a reference, so they are not kept by the live set.
			if !isStagedCandidate(name) {
				if _, ok := live[name]; ok {
					continue
				}
			}
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					log.Warn(ctx, "Artwork: Could not sweep unreferenced upload", "path", name, err)
				}
				continue
			}
			removed++
		}
	}
	return removed, nil
}

// liveUploadedFilenames is the set of cover filenames currently referenced by entities.
func liveUploadedFilenames(ctx context.Context, ds model.DataStore, entityType string) (map[string]struct{}, error) {
	live := make(map[string]struct{})
	add := func(name string) {
		if name != "" {
			live[name] = struct{}{}
		}
	}
	switch entityType {
	case consts.EntityArtist:
		cursor, err := ds.Artist().GetCursor(ctx)
		if err != nil {
			return nil, err
		}
		for artist, err := range cursor {
			if err != nil {
				return nil, err
			}
			add(artist.UploadedImage)
		}
	case consts.EntityPlaylist:
		cursor, err := ds.Playlist().GetCursor(ctx)
		if err != nil {
			return nil, err
		}
		for pls, err := range cursor {
			if err != nil {
				return nil, err
			}
			add(pls.UploadedImage)
		}
	case consts.EntityRadio:
		radios, err := ds.Radio().GetAll(ctx)
		if err != nil {
			return nil, err
		}
		for _, radio := range radios {
			add(radio.UploadedImage)
		}
	default:
		return nil, fmt.Errorf("artwork: unsupported upload entity %q", entityType)
	}
	return live, nil
}
