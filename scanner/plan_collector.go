package scanner

import (
	"context"
	"sync"

	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// planWarningLimit caps the number of warning items persisted per plan. Additional
// warnings are still counted in the summary so reviewers know the list is truncated.
const planWarningLimit = 1000

// planItemFlushBatch bounds how many items are written in one INSERT.
const planItemFlushBatch = 500

// planImportedTrack is a phase-1 track that the scan would import, with the
// previous DB record (nil for a newly discovered file).
type planImportedTrack struct {
	libID int
	track model.MediaFile
	prev  *model.MediaFile
}

// planPlaylistCandidate is a playlist file found during the walk, checked later
// for new-vs-existing status.
type planPlaylistCandidate struct {
	libID      int
	folderPath string
	filename   string
}

// planCollector gathers the change items of a preview scan. It is safe for
// concurrent use (phase-1 folders are processed with a worker pool).
type planCollector struct {
	planID string
	ds     model.DataStore

	mu sync.Mutex

	// Raw phase-1 inputs for move classification
	imported []planImportedTrack // all tracks the scan would (re)import
	missing  []model.MediaFile   // DB tracks not found on disk (newly missing this scan)
	playlists []planPlaylistCandidate

	// Folder counts for phase-3 scale reporting
	refreshedAlbums  int
	refreshedArtists int

	// FS state, fed for every walked folder (not just outdated ones)
	fsHash fsFingerprintHasher

	// Summary state
	libs      map[int]*model.PlanLibrarySummary
	gc        model.PlanGCSummary
	warnings  int // total warnings, including ones not persisted past the cap
	persisted int // number of warning items persisted

	// Buffered items pending insertion
	pending []model.ScanPlanItem
}

func newPlanCollector(planID string, ds model.DataStore, libs model.Libraries) *planCollector {
	c := &planCollector{
		planID: planID,
		ds:     ds,
		libs:   make(map[int]*model.PlanLibrarySummary, len(libs)),
	}
	for _, lib := range libs {
		c.libs[lib.ID] = &model.PlanLibrarySummary{LibraryID: lib.ID, LibraryName: lib.Name}
	}
	return c
}

func (c *planCollector) libSummary(libID int) *model.PlanLibrarySummary {
	s, ok := c.libs[libID]
	if !ok {
		s = &model.PlanLibrarySummary{LibraryID: libID}
		c.libs[libID] = s
	}
	return s
}

// recordFolder stores the FS fingerprint entry and every phase-1 fact of a
// processed (outdated) folder: imported tracks, vanished tracks and playlist files.
func (c *planCollector) recordFolder(libID int, entry *folderEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.fsHash.addFolder(libID, entry)
}

// recordWalkedFolder feeds the FS fingerprint for EVERY walked folder, including
// ones that are up to date and never processed.
func (c *planCollector) recordWalkedFolder(libID int, entry *folderEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fsHash.addFolder(libID, entry)

	// Playlist files are enumerated during the walk regardless of folder outdatedness.
	if len(entry.playlistFiles) > 0 {
		for name := range entry.playlistFiles {
			c.playlists = append(c.playlists, planPlaylistCandidate{
				libID:      libID,
				folderPath: entry.path,
				filename:   name,
			})
		}
	}
}

func (c *planCollector) recordImported(libID int, track model.MediaFile, prev *model.MediaFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.imported = append(c.imported, planImportedTrack{libID: libID, track: track, prev: prev})
}

func (c *planCollector) recordAlbums(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshedAlbums += n
}

func (c *planCollector) recordArtists(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshedArtists += n
}

func (c *planCollector) recordMissing(tracks ...model.MediaFile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.missing = append(c.missing, tracks...)
}

func (c *planCollector) addWarning(libID int, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.warnings++
	if c.persisted >= planWarningLimit {
		return
	}
	c.pending = append(c.pending, model.ScanPlanItem{
		PlanID:     c.planID,
		LibraryID:  libID,
		ChangeType: model.ScanPlanChangeWarning,
		ItemType:   model.ScanPlanItemWarning,
		Title:      msg,
	})
	c.persisted++
}

// addItem queues a change item for insertion (used by classification).
func (c *planCollector) addItem(it model.ScanPlanItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	it.PlanID = c.planID
	c.pending = append(c.pending, it)
}

// countMedia updates the per-library summary for a media change type.
func (c *planCollector) countMedia(libID int, changeType string) {
	s := c.libSummary(libID)
	switch changeType {
	case model.ScanPlanChangeAdded:
		s.Added++
	case model.ScanPlanChangeUpdated:
		s.Updated++
	case model.ScanPlanChangeMoved:
		s.Moved++
	case model.ScanPlanChangeMissing:
		s.Missing++
	case model.ScanPlanChangePurge:
		s.Purge++
	}
}

func (c *planCollector) countPlaylist(libID int) {
	c.libSummary(libID).Playlists++
}

func (c *planCollector) setGC(gc model.PlanGCSummary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gc = gc
}

// flush writes all buffered items to the plan. Safe to call multiple times.
func (c *planCollector) flush(ctx context.Context) error {
	c.mu.Lock()
	batch := c.pending
	c.pending = nil
	c.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}
	for start := 0; start < len(batch); start += planItemFlushBatch {
		end := start + planItemFlushBatch
		if end > len(batch) {
			end = len(batch)
		}
		if err := c.ds.ScanPlan(ctx).AddItems(batch[start:end]...); err != nil {
			return err
		}
	}
	log.Debug(ctx, "Scanner: Plan preview flushed items", "plan", c.planID, "items", len(batch))
	return nil
}

func (c *planCollector) fsFingerprint() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fsHash.sum()
}

func (c *planCollector) playlistCandidates() []planPlaylistCandidate {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]planPlaylistCandidate, len(c.playlists))
	copy(out, c.playlists)
	return out
}

// snapshotSummary builds the JSON-able summary accumulated so far.
func (c *planCollector) snapshotSummary() model.PlanSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	libs := make(map[int]*model.PlanLibrarySummary, len(c.libs))
	for k, v := range c.libs {
		cp := *v
		libs[k] = &cp
	}
	return model.PlanSummary{
		Libraries:        libs,
		GC:               c.gc,
		RefreshedAlbums:  c.refreshedAlbums,
		RefreshedArtists: c.refreshedArtists,
		Warnings:         c.warnings,
	}
}
