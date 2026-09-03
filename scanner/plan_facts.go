package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils"
	"github.com/navidrome/navidrome/utils/pl"
	"github.com/navidrome/navidrome/utils/slice"
	"github.com/zeebo/xxh3"
)

// This file collects the facts a scan plan binds at preview time and verifies at
// apply time:
//   - config snapshot/fingerprint: scanner configuration that changes results;
//   - DB facts: per-library scan state/counters and global playlist state;
//   - FS fingerprint: a stat-only walk hash of every folder in scope.
//
// A plan may only be applied while all facts still match.

// planConfigSnapshot is the subset of the configuration that influences scan results.
type planConfigSnapshot struct {
	PurgeMissing        string `json:"purgeMissing"`
	PIDAlbum            string `json:"pidAlbum"`
	PIDTrack            string `json:"pidTrack"`
	AutoImportPlaylists bool   `json:"autoImportPlaylists"`
	Extractor           string `json:"extractor"`
	IgnoreDotFolders    bool   `json:"ignoreDotFolders"`
	FollowSymlinks      bool   `json:"followSymlinks"`
	GenreSeparators     string `json:"genreSeparators"`
	GroupAlbumReleases  bool   `json:"groupAlbumReleases"`
	ArtistJoiner        string `json:"artistJoiner"`
}

func currentPlanConfigSnapshot() planConfigSnapshot {
	return planConfigSnapshot{
		PurgeMissing:        conf.Server.Scanner.PurgeMissing,
		PIDAlbum:            conf.Server.PID.Album,
		PIDTrack:            conf.Server.PID.Track,
		AutoImportPlaylists: conf.Server.AutoImportPlaylists,
		Extractor:           conf.Server.Scanner.Extractor,
		IgnoreDotFolders:    conf.Server.Scanner.IgnoreDotFolders,
		FollowSymlinks:      conf.Server.Scanner.FollowSymlinks,
		GenreSeparators:     conf.Server.Scanner.GenreSeparators,
		GroupAlbumReleases:  conf.Server.Scanner.GroupAlbumReleases,
		ArtistJoiner:        conf.Server.Scanner.ArtistJoiner,
	}
}

// planLibraryFact is the per-library state bound to a plan.
type planLibraryFact struct {
	ID                 int       `json:"id"`
	Path               string    `json:"path"`
	LastScanAt         time.Time `json:"lastScanAt"`
	LastScanStartedAt  time.Time `json:"lastScanStartedAt"`
	FullScanInProgress bool      `json:"fullScanInProgress"`
	TotalSongs         int       `json:"totalSongs"`
	TotalFolders       int       `json:"totalFolders"`
}

// planDBFacts is the database state bound to a plan.
type planDBFacts struct {
	Libraries            []planLibraryFact `json:"libraries"`
	PlaylistCount        int64             `json:"playlistCount"`
	PlaylistMaxUpdatedAt time.Time         `json:"playlistMaxUpdatedAt"`
}

func stableJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// hashJSON returns a stable, non-cryptographic hash of a JSON-able value.
func hashJSON(v any) string {
	return strconv.FormatUint(xxh3.HashString(stableJSON(v)), 16)
}

// decodePlanTargets parses the targets JSON persisted in a plan.
func decodePlanTargets(plan *model.ScanPlan) ([]model.ScanTarget, error) {
	var targets []model.ScanTarget
	if plan.Targets != "" {
		if err := json.Unmarshal([]byte(plan.Targets), &targets); err != nil {
			return nil, fmt.Errorf("parsing plan targets: %w", err)
		}
	}
	return targets, nil
}

// scopeLibraries resolves scan targets into the in-scope libraries and their
// normalized in-library target folders, mirroring scanFolders' target handling.
func scopeLibraries(ctx context.Context, ds model.DataStore, targets []model.ScanTarget) (model.Libraries, map[int][]string, error) {
	allLibs, err := ds.Library(ctx).GetAll()
	if err != nil {
		return nil, nil, fmt.Errorf("getting libraries: %w", err)
	}

	if len(targets) == 0 {
		return allLibs, nil, nil
	}

	libPaths := slice.ToMap(allLibs, func(lib model.Library) (int, string) { return lib.ID, lib.Path })
	targetMap := make(map[int][]string)
	for _, t := range targets {
		libPath, ok := libPaths[t.LibraryID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: library ID %d not found", model.ErrNotFound, t.LibraryID)
		}
		folderPath := libraryRelativePath(libPath, t.FolderPath)
		if folderPath == "" {
			folderPath = "."
		}
		targetMap[t.LibraryID] = append(targetMap[t.LibraryID], folderPath)
	}

	libs := slice.Filter(allLibs, func(lib model.Library) bool { return len(targetMap[lib.ID]) > 0 })
	return libs, targetMap, nil
}

// collectPlanDBFacts gathers the database state the plan must match at apply time.
func collectPlanDBFacts(ctx context.Context, ds model.DataStore, targets []model.ScanTarget) (planDBFacts, error) {
	libs, _, err := scopeLibraries(ctx, ds, targets)
	if err != nil {
		return planDBFacts{}, err
	}

	facts := planDBFacts{Libraries: make([]planLibraryFact, 0, len(libs))}
	for _, lib := range libs {
		facts.Libraries = append(facts.Libraries, planLibraryFact{
			ID:                 lib.ID,
			Path:               lib.Path,
			LastScanAt:         lib.LastScanAt,
			LastScanStartedAt:  lib.LastScanStartedAt,
			FullScanInProgress: lib.FullScanInProgress,
			TotalSongs:         lib.TotalSongs,
			TotalFolders:       lib.TotalFolders,
		})
	}
	slices.SortFunc(facts.Libraries, func(a, b planLibraryFact) int { return a.ID - b.ID })

	// Playlists are global (not library-scoped), so bind their global state.
	count, err := ds.Playlist(ctx).CountAll()
	if err != nil {
		return planDBFacts{}, fmt.Errorf("counting playlists: %w", err)
	}
	facts.PlaylistCount = count

	pls, err := ds.Playlist(ctx).GetAll(model.QueryOptions{Sort: "updated_at", Order: "desc", Max: 1})
	if err != nil {
		return planDBFacts{}, fmt.Errorf("getting latest playlist: %w", err)
	}
	if len(pls) > 0 {
		facts.PlaylistMaxUpdatedAt = pls[0].UpdatedAt
	}
	return facts, nil
}

// fsFingerprintHasher accumulates a stat-only fingerprint of the walked tree.
// Folders are walked in sorted order by walkDirTree, so the stream is deterministic.
type fsFingerprintHasher struct {
	h xxh3.Hasher
}

func (f *fsFingerprintHasher) addFolder(libID int, folder *folderEntry) {
	fmt.Fprintf(&f.h, "%d\x00%s\x00%s\x00%d\x00%d\x00%d\n",
		libID, folder.path, folder.hash(),
		folder.modTime.UnixNano(), folder.imagesUpdatedAt.UnixNano(), len(folder.audioFiles))
}

func (f *fsFingerprintHasher) sum() string {
	return strconv.FormatUint(f.h.Sum64(), 16)
}

// computePlanFSFingerprint walks every folder in scope and hashes the same
// folder states the scanner uses to detect changes. It only stats the filesystem
// (no tag extraction), so it is cheap enough to run before applying a plan.
func computePlanFSFingerprint(ctx context.Context, ds model.DataStore, targets []model.ScanTarget, progress chan<- *ProgressInfo) (string, error) {
	libs, targetMap, err := scopeLibraries(ctx, ds, targets)
	if err != nil {
		return "", err
	}

	hasher := &fsFingerprintHasher{}
	for _, lib := range libs {
		if utils.IsCtxDone(ctx) {
			return "", ctx.Err()
		}
		job, err := newScanJob(ctx, ds, lib, false, targetMap[lib.ID])
		if err != nil {
			return "", fmt.Errorf("creating scan job for library %s: %w", lib.Name, err)
		}
		out, err := walkDirTree(ctx, job, job.targetFolders...)
		if err != nil {
			return "", fmt.Errorf("walking library %s: %w", lib.Name, err)
		}
		var folders int64
		for folder := range pl.ReadOrDone(ctx, out) {
			folders++
			hasher.addFolder(lib.ID, folder)
			if progress != nil && folders%100 == 0 {
				progress <- &ProgressInfo{LibID: lib.ID, FileCount: 1, Path: folder.path, Phase: "verify"}
			}
		}
		log.Debug(ctx, "Scanner: FS fingerprint walk finished", "lib", lib.Name, "folders", folders)
	}
	return hasher.sum(), nil
}

// planExpiry returns the expiration timestamp for a plan created at now, or nil
// when expiration is disabled (Scanner.ScanPlanTTL <= 0).
func planExpiry(now time.Time) *time.Time {
	ttl := conf.Server.Scanner.ScanPlanTTL
	if ttl <= 0 {
		return nil
	}
	t := now.Add(ttl)
	return &t
}

func planIsExpired(plan *model.ScanPlan, now time.Time) bool {
	return plan.ExpiresAt != nil && now.After(*plan.ExpiresAt)
}

// checkConfigStaleness compares the bound configuration fingerprint with the current one.
func checkConfigStaleness(plan *model.ScanPlan) *model.PlanStaleReason {
	current := hashJSON(currentPlanConfigSnapshot())
	if current == plan.ConfigFingerprint {
		return nil
	}
	return &model.PlanStaleReason{
		Code:    model.StaleReasonConfigChanged,
		Message: "scanner configuration changed since the plan was created",
	}
}

// checkDBStaleness re-gathers the DB facts and reports any per-state differences.
func checkDBStaleness(ctx context.Context, ds model.DataStore, plan *model.ScanPlan) ([]model.PlanStaleReason, error) {
	targets, err := decodePlanTargets(plan)
	if err != nil {
		return nil, err
	}
	current, err := collectPlanDBFacts(ctx, ds, targets)
	if err != nil {
		return nil, err
	}

	// Fast path: identical serialized facts.
	if stableJSON(current) == plan.DbFingerprint {
		return nil, nil
	}

	var stored planDBFacts
	if plan.DbFingerprint != "" {
		if err := json.Unmarshal([]byte(plan.DbFingerprint), &stored); err != nil {
			return nil, fmt.Errorf("parsing stored DB facts: %w", err)
		}
	}

	var reasons []model.PlanStaleReason
	currentByID := slice.ToMap(current.Libraries, func(f planLibraryFact) (int, planLibraryFact) { return f.ID, f })
	storedByID := slice.ToMap(stored.Libraries, func(f planLibraryFact) (int, planLibraryFact) { return f.ID, f })

	for id, was := range storedByID {
		now, ok := currentByID[id]
		if !ok {
			reasons = append(reasons, model.PlanStaleReason{
				Code:    model.StaleReasonLibraryChanged,
				Message: fmt.Sprintf("library %d was removed since the plan was created", id),
			})
			continue
		}
		if now.Path != was.Path || !now.LastScanAt.Equal(was.LastScanAt) ||
			!now.LastScanStartedAt.Equal(was.LastScanStartedAt) || now.FullScanInProgress != was.FullScanInProgress {
			reasons = append(reasons, model.PlanStaleReason{
				Code: model.StaleReasonLibraryChanged,
				Message: fmt.Sprintf("library %d was re-scanned or reconfigured since the plan was created",
					id),
			})
		}
		if now.TotalSongs != was.TotalSongs || now.TotalFolders != was.TotalFolders {
			reasons = append(reasons, model.PlanStaleReason{
				Code: model.StaleReasonMediaChanged,
				Message: fmt.Sprintf("media records in library %d changed since the plan was created",
					id),
			})
		}
	}
	for id := range currentByID {
		if _, ok := storedByID[id]; !ok {
			reasons = append(reasons, model.PlanStaleReason{
				Code:    model.StaleReasonLibraryChanged,
				Message: fmt.Sprintf("library %d was added to the scan scope since the plan was created", id),
			})
		}
	}

	if current.PlaylistCount != stored.PlaylistCount ||
		!current.PlaylistMaxUpdatedAt.Equal(stored.PlaylistMaxUpdatedAt) {
		reasons = append(reasons, model.PlanStaleReason{
			Code:    model.StaleReasonPlaylistsChanged,
			Message: "playlists changed since the plan was created",
		})
	}

	return reasons, nil
}

// checkFSStaleness re-walks the scope and compares the stat-only fingerprint.
func checkFSStaleness(ctx context.Context, ds model.DataStore, plan *model.ScanPlan, progress chan<- *ProgressInfo) *model.PlanStaleReason {
	targets, err := decodePlanTargets(plan)
	if err != nil {
		return &model.PlanStaleReason{Code: model.StaleReasonFilesystemChanged, Message: err.Error()}
	}
	current, err := computePlanFSFingerprint(ctx, ds, targets, progress)
	if err != nil {
		log.Warn(ctx, "Scanner: Error computing FS fingerprint", err)
		return &model.PlanStaleReason{
			Code:    model.StaleReasonFilesystemChanged,
			Message: fmt.Sprintf("could not verify filesystem state: %s", err),
		}
	}
	if current != plan.FsFingerprint {
		return &model.PlanStaleReason{
			Code:    model.StaleReasonFilesystemChanged,
			Message: "files in the scan scope changed since the plan was created",
		}
	}
	return nil
}
