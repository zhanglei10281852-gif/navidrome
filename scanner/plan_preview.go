package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/run"
	"github.com/navidrome/navidrome/utils/slice"
)

// planGCCounter is implemented by persistence.SQLStore (read-only GC candidate counts)
// and mocked in tests.
type planGCCounter interface {
	GCPlanCounts(ctx context.Context, libraryIDs ...int) (model.PlanGCSummary, error)
}

// previewFolders runs a read-only preview scan for a scan plan: it walks and analyzes
// exactly like scanFolders, but persists change items to scan_plan_item instead of
// touching media library state. The plan record is updated with the summary and FS
// fingerprint at the end. Works in-process; scannerExternal implements its own subprocess
// variant.
func (s *scannerImpl) previewFolders(ctx context.Context, fullScan bool, targets []model.ScanTarget, planID string, progress chan<- *ProgressInfo) {
	startTime := time.Now()
	state := &scanState{
		progress: progress,
		ds:       s.ds,
		fullScan: fullScan,
		mode:     modePreview,
	}

	fail := func(err error) {
		state.sendError(err)
		log.Error(ctx, "Scanner: Plan preview failed", "plan", planID, err)
		_, _ = s.ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusRunning, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  err.Error(),
		})
	}

	// --- Library/target setup: mirrors scanFolders without any ScanBegin/property writes.
	allLibs, err := s.ds.Library(ctx).GetAll()
	if err != nil {
		fail(fmt.Errorf("getting libraries: %w", err))
		return
	}
	state.totalLibraryCount = len(allLibs)

	if len(targets) > 0 {
		libPaths := slice.ToMap(allLibs, func(lib model.Library) (int, string) {
			return lib.ID, lib.Path
		})
		state.targets = make(map[int][]string)
		for _, target := range targets {
			folderPath := libraryRelativePath(libPaths[target.LibraryID], target.FolderPath)
			if folderPath == "" {
				folderPath = "."
			}
			state.targets[target.LibraryID] = append(state.targets[target.LibraryID], folderPath)
		}
		state.libraries = slice.Filter(allLibs, func(lib model.Library) bool {
			return len(state.targets[lib.ID]) > 0
		})
	} else {
		state.libraries = allLibs
	}


	if !state.fullScan {
		for _, lib := range state.libraries {
			if lib.FullScanInProgress {
				log.Info(ctx, "Scanner: Interrupted full scan detected, plan preview will analyze as full scan", "lib", lib.Name)
				state.fullScan = true
				break
			}
		}
	}

	state.collector = newPlanCollector(planID, s.ds, state.libraries)

	log.Info(ctx, "Scanner: Starting change preview", "plan", planID, "fullScan", state.fullScan, "numLibraries", len(state.libraries))

	err = run.Sequentially(
		runPhase[*folderEntry](ctx, 1, createPhaseFolders(ctx, state, s.ds)),
		func() error {
			state.sendProgress(&ProgressInfo{Mode: PlanModePreview, PlanID: planID, Stage: "classify"})
			return classifyPreviewChanges(ctx, state, state.collector)
		},
		func() error {
			state.sendProgress(&ProgressInfo{Mode: PlanModePreview, PlanID: planID, Stage: "playlists"})
			return s.previewPlaylists(ctx, state)
		},
		func() error {
			state.sendProgress(&ProgressInfo{Mode: PlanModePreview, PlanID: planID, Stage: "gc"})
			return s.previewGCCounts(ctx, state)
		},
	)
	if err != nil {
		_ = state.collector.flush(ctx)
		fail(err)
		return
	}

	if err := state.collector.flush(ctx); err != nil {
		fail(fmt.Errorf("persisting plan items: %w", err))
		return
	}

	summary := state.collector.snapshotSummary()
	summaryBytes, err := json.Marshal(summary)
	if err != nil {
		fail(fmt.Errorf("encoding summary: %w", err))
		return
	}

	ok, err := s.ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusRunning, map[string]any{
		"status":         model.ScanPlanStatusReady,
		"summary":        string(summaryBytes),
		"fs_fingerprint": state.collector.fsFingerprint(),
		"warnings_total": summary.Warnings,
		"error":          "",
	})
	if err != nil {
		fail(fmt.Errorf("finalizing plan: %w", err))
		return
	}
	if !ok {
		log.Warn(ctx, "Scanner: Plan preview finished but plan was not in 'running' state", "plan", planID)
		return
	}

	log.Info(ctx, "Scanner: Change preview finished", "plan", planID, "duration", time.Since(startTime),
		"libraries", len(summary.Libraries))
	state.sendProgress(&ProgressInfo{Mode: PlanModePreview, PlanID: planID, ChangesDetected: true})
}

// previewPlaylists reports playlist files found during the walk that an applying
// scan would import or update, without persisting anything.
func (s *scannerImpl) previewPlaylists(ctx context.Context, state *scanState) error {
	if !conf.Server.AutoImportPlaylists {
		return nil
	}
	c := state.collector
	candidates := c.playlistCandidates()

	byID := slice.ToMap(state.libraries, func(lib model.Library) (int, model.Library) { return lib.ID, lib })
	for _, cand := range candidates {
		lib, ok := byID[cand.libID]
		if !ok {
			continue
		}
		folder := model.NewFolder(lib, cand.folderPath)
		pls, err := s.pls.PreviewFromFolder(ctx, folder, cand.filename)
		if err != nil || pls == nil {
			log.Debug(ctx, "Scanner: Plan preview could not parse playlist; skip", "file", cand.filename, "err", err)
			c.addWarning(cand.libID, fmt.Sprintf("Playlist %s cannot be parsed: %v", cand.filename, err))
			continue
		}
		status := "new"
		if pls.ID != "" {
			status = "updated"
		}
		details, _ := json.Marshal(map[string]any{"status": status, "tracks": len(pls.Tracks)})
		c.addItem(model.ScanPlanItem{
			LibraryID:  cand.libID,
			ChangeType: model.ScanPlanChangePlaylist,
			ItemType:   model.ScanPlanItemPlaylist,
			Path:       pls.Path,
			Title:      pls.Name,
			Details:    string(details),
		})
		c.countPlaylist(cand.libID)
	}
	return nil
}

// previewGCCounts populates the summary with read-only GC candidate counts when the
// applying scan would actually run GC (i.e. changes were detected).
func (s *scannerImpl) previewGCCounts(ctx context.Context, state *scanState) error {
	summary := state.collector.snapshotSummary()
	hasChanges := summary.Warnings > 0
	for _, l := range summary.Libraries {
		if l.Added+l.Updated+l.Moved+l.Missing+l.Purge+l.Playlists > 0 {
			hasChanges = true
		}
	}
	if !hasChanges {
		return nil
	}
	counter, ok := s.ds.(planGCCounter)
	if !ok {
		return nil
	}
	libIDs := slices.Collect(maps.Keys(state.targets))
	if len(libIDs) == 0 {
		libIDs = slice.Map(state.libraries, func(lib model.Library) int { return lib.ID })
	}
	counts, err := counter.GCPlanCounts(ctx, libIDs...)
	if err != nil {
		log.Warn(ctx, "Scanner: Plan preview GC counts failed", err)
		return nil
	}
	state.collector.setGC(counts)
	return nil
}
