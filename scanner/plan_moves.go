package scanner

import (
	"context"

	"github.com/Masterminds/squirrel"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// This file is the preview counterpart of phase 2 (missing tracks / moves): move
// detection runs against the in-memory facts collected during phase 1 instead of
// the rows the applying scan writes there. Remaining facts are then turned into
// added/updated/moved/missing/purge plan items.

// previewFacts is a locked snapshot of the collector state for classification.
type previewFacts struct {
	imported []planImportedTrack
	missing  []model.MediaFile
}

func (c *planCollector) snapshot() previewFacts {
	c.mu.Lock()
	defer c.mu.Unlock()
	imported := make([]planImportedTrack, len(c.imported))
	copy(imported, c.imported)
	missing := make([]model.MediaFile, len(c.missing))
	copy(missing, c.missing)
	return previewFacts{imported: imported, missing: missing}
}

// moveCandidate is a newly discovered file (phase-1 added) that an applying scan
// would import and later match as the new location of a missing track.
type moveCandidate struct {
	libID int
	track model.MediaFile
}

// classifyPreviewChanges performs phase-2-equivalent move detection on collected
// facts, then emits plan items for every change the applying scan would make.
func classifyPreviewChanges(ctx context.Context, state *scanState, collector *planCollector) error {
	facts := collector.snapshot()

	// All tracks currently flagged missing in the database (previous scans).
	// DeleteAllMissing purges globally, so this is not scoped to the scan libraries.
	dbMissing, err := state.ds.MediaFile(ctx).GetAll(model.QueryOptions{Filters: squirrel.Eq{"missing": true}})
	if err != nil {
		return err
	}

	// Union missing tracks (dedupe by ID). Phase-1 missing facts already include
	// previously-missing rows from WALKED folders; dbMissing covers the rest.
	missingByID := make(map[string]model.MediaFile)
	for _, mf := range facts.missing {
		missingByID[mf.ID] = mf
	}
	for _, mf := range dbMissing {
		if _, ok := missingByID[mf.ID]; !ok {
			missingByID[mf.ID] = mf
		}
	}

	// Newly discovered files are the "matched" side. Updated tracks (prev != nil) stay updates.
	var matched []moveCandidate
	for _, imp := range facts.imported {
		if imp.prev == nil {
			matched = append(matched, moveCandidate{libID: imp.libID, track: imp.track})
		}
	}
	usedMatched := make(map[string]bool)

	// Within-library matching, mirroring phaseMissingTracks.processMissingTracks.
	matchedByLibPID := make(map[int]map[string][]model.MediaFile)
	for _, m := range matched {
		if m.track.PID == "" {
			continue
		}
		groups, ok := matchedByLibPID[m.libID]
		if !ok {
			groups = make(map[string][]model.MediaFile)
			matchedByLibPID[m.libID] = groups
		}
		groups[m.track.PID] = append(groups[m.track.PID], m.track)
	}

	missingByLibPID := make(map[int]map[string][]model.MediaFile)
	for _, mf := range missingByID {
		if mf.PID == "" {
			continue
		}
		groups, ok := missingByLibPID[mf.LibraryID]
		if !ok {
			groups = make(map[string][]model.MediaFile)
			missingByLibPID[mf.LibraryID] = groups
		}
		groups[mf.PID] = append(groups[mf.PID], mf)
	}

	moved := 0
	recordMove := func(libID int, missing, newTrack model.MediaFile) {
		collector.addItem(model.ScanPlanItem{
			LibraryID:   libID,
			ChangeType:  model.ScanPlanChangeMoved,
			ItemType:    model.ScanPlanItemMedia,
			MediaFileID: missing.ID,
			Path:        newTrack.Path,
			OldPath:     missing.Path,
			Title:       newTrack.Title,
			Artist:      newTrack.Artist,
			Album:       newTrack.Album,
		})
		collector.countMedia(libID, model.ScanPlanChangeMoved)
		delete(missingByID, missing.ID)
		usedMatched[newTrack.ID] = true
		moved++
	}

	for libID, missingGroups := range missingByLibPID {
		matchedGroups := matchedByLibPID[libID]
		for pid, missingTracks := range missingGroups {
			matchedTracks := matchedGroups[pid]
			for _, ms := range missingTracks {
				var exact model.MediaFile
				var equiv model.MediaFile
				for _, mt := range matchedTracks {
					if usedMatched[mt.ID] {
						continue
					}
					if ms.Equals(mt) {
						exact = mt
						break // Prioritize exact match
					}
					if ms.IsEquivalent(mt) {
						equiv = mt
					}
				}

				// Use the exact match if found
				if exact.ID != "" {
					recordMove(libID, ms, exact)
					continue
				}
				// Single missing + single matched in this PID group: treat as a move
				if len(missingTracks) == 1 && len(matchedTracks) == 1 && !usedMatched[matchedTracks[0].ID] {
					recordMove(libID, ms, matchedTracks[0])
					continue
				}
				// Fall back to the equivalent match
				if equiv.ID != "" {
					recordMove(libID, ms, equiv)
				}
			}
		}
	}

	// Cross-library matching for the remaining missing tracks.
	for id, ms := range missingByID {
		var found *moveCandidate
		for i := range matched {
			cand := &matched[i]
			if cand.libID == ms.LibraryID || usedMatched[cand.track.ID] {
				continue
			}
			// First tier: MusicBrainz track ID exact match. Second tier: intrinsic properties.
			if (ms.MbzReleaseTrackID != "" && ms.MbzReleaseTrackID == cand.track.MbzReleaseTrackID && ms.Equals(cand.track)) ||
				(ms.MbzReleaseTrackID == "" && ms.IsEquivalent(cand.track)) {
				found = cand
				break
			}
		}
		if found == nil {
			continue
		}
		recordMove(found.libID, ms, found.track)
		_ = id
	}

	// Remaining phase-1 facts become additions/updates.
	for _, imp := range facts.imported {
		if imp.prev != nil {
			collector.addItem(model.ScanPlanItem{
				LibraryID:   imp.libID,
				ChangeType:  model.ScanPlanChangeUpdated,
				ItemType:    model.ScanPlanItemMedia,
				MediaFileID: imp.prev.ID,
				Path:        imp.track.Path,
				Title:       imp.track.Title,
				Artist:      imp.track.Artist,
				Album:       imp.track.Album,
			})
			collector.countMedia(imp.libID, model.ScanPlanChangeUpdated)
			continue
		}
		if usedMatched[imp.track.ID] {
			continue // consumed by a move
		}
		collector.addItem(model.ScanPlanItem{
			LibraryID:  imp.libID,
			ChangeType: model.ScanPlanChangeAdded,
			ItemType:   model.ScanPlanItemMedia,
			Path:       imp.track.Path,
			Title:      imp.track.Title,
			Artist:     imp.track.Artist,
			Album:      imp.track.Album,
		})
		collector.countMedia(imp.libID, model.ScanPlanChangeAdded)
	}

	// Remaining missing tracks: purged when the policy deletes them, otherwise marked missing.
	purge := conf.Server.Scanner.PurgeMissing == consts.PurgeMissingAlways ||
		(conf.Server.Scanner.PurgeMissing == consts.PurgeMissingFull && state.fullScan)
	for _, mf := range missingByID {
		changeType := model.ScanPlanChangeMissing
		if purge {
			changeType = model.ScanPlanChangePurge
		}
		collector.addItem(model.ScanPlanItem{
			LibraryID:   mf.LibraryID,
			ChangeType:  changeType,
			ItemType:    model.ScanPlanItemMedia,
			MediaFileID: mf.ID,
			Path:        mf.Path,
			Title:       mf.Title,
			Artist:      mf.Artist,
			Album:       mf.Album,
		})
		collector.countMedia(mf.LibraryID, changeType)
	}

	if moved > 0 {
		log.Debug(ctx, "Scanner: Plan preview found moved files", "total", moved)
	}
	return nil
}
