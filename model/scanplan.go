package model

import (
	"context"
	"time"
)

// Scan plan lifecycle statuses.
const (
	ScanPlanStatusRunning  = "running"  // preview in progress
	ScanPlanStatusReady    = "ready"    // preview finished, waiting for admin confirmation
	ScanPlanStatusApplying = "applying" // apply (real scan) in progress
	ScanPlanStatusApplied  = "applied"  // apply finished successfully
	ScanPlanStatusFailed   = "failed"   // preview or apply failed / interrupted by a restart
)

// Scan plan item change types (media entries).
const (
	ScanPlanChangeAdded    = "added"    // new media file found on disk
	ScanPlanChangeUpdated  = "updated"  // existing track that will be re-imported
	ScanPlanChangeMoved    = "moved"    // track matched as moved/renamed (old path -> new path)
	ScanPlanChangeMissing  = "missing"  // track that will be marked missing
	ScanPlanChangePurge    = "purge"    // missing track that the purge policy will delete
	ScanPlanChangePlaylist = "playlist" // playlist that will be imported/updated
	ScanPlanChangeWarning  = "warning"  // a non-fatal warning collected during the preview
)

// Scan plan item kinds.
const (
	ScanPlanItemMedia    = "media"
	ScanPlanItemPlaylist = "playlist"
	ScanPlanItemWarning  = "warning"
)

// Stale reason codes returned when an apply is rejected because the facts bound
// to the plan no longer match the current state.
const (
	StaleReasonConfigChanged      = "config_changed"      // relevant scanner configuration changed
	StaleReasonLibraryChanged     = "library_changed"     // library scan state/timestamps changed
	StaleReasonMediaChanged       = "media_changed"       // media record counts changed
	StaleReasonPlaylistsChanged  = "playlists_changed"   // playlists changed since the preview
	StaleReasonFilesystemChanged = "filesystem_changed"  // files in scope changed on disk
)

// PlanRequest is the input for creating a scan plan preview. It mirrors the
// parameters of a real scan: FullScan selects full vs quick, Targets optionally
// restricts the scan to library/folder pairs (empty = all libraries).
type PlanRequest struct {
	FullScan bool
	Targets  []ScanTarget
}

// PlanLibrarySummary holds the per-library change counts of a preview.
type PlanLibrarySummary struct {
	LibraryID   int    `json:"libraryId"`
	LibraryName string `json:"libraryName"`
	Added       int    `json:"added"`
	Updated     int    `json:"updated"`
	Moved       int    `json:"moved"`
	Missing     int    `json:"missing"`
	Purge       int    `json:"purge"`
	Playlists   int    `json:"playlists"`
}

// PlanGCSummary reports the cross-library garbage-collection candidate counts
// that a real scan would remove. These are scale indicators, not item-level data.
type PlanGCSummary struct {
	EmptyAlbums          int64 `json:"emptyAlbums"`
	EmptyArtists         int64 `json:"emptyArtists"`
	EmptyFolders         int64 `json:"emptyFolders"`
	OrphanAnnotations    int64 `json:"orphanAnnotations"`
	UnusedTags           int64 `json:"unusedTags"`
	OrphanPlaylistTracks int64 `json:"orphanPlaylistTracks"`
}

// PlanSummary is the aggregated result of a preview, persisted as JSON in scan_plan.summary.
type PlanSummary struct {
	Libraries        map[int]*PlanLibrarySummary `json:"libraries"`
	GC               PlanGCSummary               `json:"gc"`
	RefreshedAlbums  int                         `json:"refreshedAlbums"`
	RefreshedArtists int                         `json:"refreshedArtists"`
	Warnings         int                         `json:"warnings"`
}

// ScanPlan is a persisted, reviewable scan preview.
type ScanPlan struct {
	ID                string     `structs:"id" json:"id"`
	Status            string     `structs:"status" json:"status"`
	FullScan          bool       `structs:"full_scan" json:"fullScan"`
	Targets           string     `structs:"targets" json:"targets"`       // JSON: []ScanTarget
	LibraryIDs        string     `structs:"library_ids" json:"libraryIds"` // JSON: []int
	ConfigFingerprint string     `structs:"config_fingerprint" json:"configFingerprint"`
	ConfigSnapshot    string     `structs:"config_snapshot" json:"configSnapshot"`
	FsFingerprint     string     `structs:"fs_fingerprint" json:"fsFingerprint"`
	DbFingerprint     string     `structs:"db_fingerprint" json:"dbFingerprint"`
	Summary           string     `structs:"summary" json:"summary"` // JSON: PlanSummary
	WarningsTotal     int        `structs:"warnings_total" json:"warningsTotal"`
	Error             string     `structs:"error" json:"error,omitempty"`
	ExpiresAt         *time.Time `structs:"expires_at" json:"expiresAt,omitempty"`
	CreatedAt         time.Time  `structs:"created_at" json:"createdAt"`
	UpdatedAt         time.Time  `structs:"updated_at" json:"updatedAt"`
	AppliedAt         *time.Time `structs:"applied_at" json:"appliedAt,omitempty"`
}

type ScanPlans []ScanPlan

// PlanStaleReason explains why a plan can no longer be applied: the bound facts
// (config, library/media records, playlists or files) no longer match the current state.
type PlanStaleReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ScanPlanItem is a single change entry collected during a preview.
type ScanPlanItem struct {
	ID          string    `structs:"id" json:"id"`
	PlanID      string    `structs:"plan_id" json:"planId"`
	LibraryID   int       `structs:"library_id" json:"libraryId"`
	ChangeType  string    `structs:"change_type" json:"changeType"`
	ItemType    string    `structs:"item_type" json:"itemType"`
	MediaFileID string    `structs:"media_file_id" json:"mediaFileId,omitempty"`
	Path        string    `structs:"path" json:"path"`
	OldPath     string    `structs:"old_path" json:"oldPath,omitempty"` // moves: previous location
	Title       string    `structs:"title" json:"title,omitempty"`
	Artist      string    `structs:"artist" json:"artist,omitempty"`
	Album       string    `structs:"album" json:"album,omitempty"`
	Details     string    `structs:"details" json:"details,omitempty"` // JSON: type-specific extras
	CreatedAt   time.Time `structs:"created_at" json:"createdAt"`
}

type ScanPlanItems []ScanPlanItem

// ScanPlanRepository persists scan plans and their items.
type ScanPlanRepository interface {
	Put(plan *ScanPlan) error
	Get(id string) (*ScanPlan, error)
	GetAll(options ...QueryOptions) (ScanPlans, error)
	CountAll(options ...QueryOptions) (int64, error)
	Delete(id string) error

	// UpdateStatus performs a compare-and-set on the plan status: it updates the
	// row only when the current status equals expect, applying the extra columns
	// (e.g. error, applied_at, summary). Returns true when the row was updated.
	UpdateStatus(id string, expect string, set map[string]any) (bool, error)

	AddItems(items ...ScanPlanItem) error
	GetItems(planID string, options ...QueryOptions) (ScanPlanItems, error)
	CountItems(planID string, options ...QueryOptions) (int64, error)

	// FailInterrupted marks plans left in running/applying (e.g. by a process
	// crash) as failed, so they can never be applied.
	FailInterrupted(now time.Time) (int64, error)
}

// ScanPlanner is the service entry point for the scan-plan feature. It is
// implemented by the scanner controller alongside the regular Scanner interface.
type ScanPlanner interface {
	// PreviewPlan creates a plan record and runs a read-only preview scan.
	PreviewPlan(ctx context.Context, req PlanRequest) (string, error)
	// ApplyPlan verifies the plan is still fresh and runs the real scan.
	ApplyPlan(ctx context.Context, planID string) error
}
