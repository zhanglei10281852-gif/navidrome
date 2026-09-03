package scanner

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	id2 "github.com/navidrome/navidrome/model/id"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/events"
	"github.com/navidrome/navidrome/utils/slice"
)

// Sentinel errors reported by plan service operations; handlers map them to HTTP status.
var (
	ErrPlanRunning  = errors.New("scan plan is still being prepared")
	ErrPlanApplying = errors.New("scan plan is already being applied")
	ErrPlanFailed   = errors.New("scan plan could not be generated; please create a new preview")
	ErrPlanExpired  = errors.New("scan plan expired; please create a new preview")
)

// PlanReadiness reports the cheap (no-filesystem) stale/expired state of a plan,
// for list/detail UIs without running a verification walk.
func PlanReadiness(ctx context.Context, ds model.DataStore, plan *model.ScanPlan) (bool, []model.PlanStaleReason, error) {
	var stale []model.PlanStaleReason
	if planIsExpired(plan, time.Now()) {
		return true, nil, nil
	}
	if reason := checkConfigStaleness(plan); reason != nil {
		stale = append(stale, *reason)
	}
	dbReasons, err := checkDBStaleness(ctx, ds, plan)
	if err != nil {
		return false, nil, err
	}
	stale = append(stale, dbReasons...)
	return false, stale, nil
}

// PreviewPlan creates a scan plan record, binds the current configuration/database
// facts to it, then runs the read-only preview asynchronously. It implements
// model.ScanPlanner.
func (s *controller) PreviewPlan(reqCtx context.Context, req model.PlanRequest) (string, error) {
	ctx := request.AddValues(reqCtx, s.rootCtx)
	ctx = auth.WithAdminUser(ctx, s.ds)

	libs, _, err := scopeLibraries(ctx, s.ds, req.Targets)
	if err != nil {
		return "", err
	}

	// Mirror the interrupted-full-scan promotion so the plan's intensity matches what
	// an apply run would execute.
	fullScan := req.FullScan
	if !fullScan {
		for _, lib := range libs {
			if lib.FullScanInProgress {
				log.Info(ctx, "Scanner: Interrupted full scan detected, plan preview will analyze as full scan")
				fullScan = true
				break
			}
		}
	}

	cfgSnap := currentPlanConfigSnapshot()
	dbFacts, err := collectPlanDBFacts(ctx, s.ds, req.Targets)
	if err != nil {
		return "", err
	}

	now := time.Now()
	libIDs := slice.Map(libs, func(lib model.Library) int { return lib.ID })
	plan := &model.ScanPlan{
		ID:                id2.NewRandom(),
		Status:            model.ScanPlanStatusRunning,
		FullScan:          fullScan,
		Targets:           stableJSON(req.Targets),
		LibraryIDs:        stableJSON(libIDs),
		ConfigFingerprint: hashJSON(cfgSnap),
		ConfigSnapshot:    stableJSON(cfgSnap),
		DbFingerprint:     stableJSON(dbFacts),
		ExpiresAt:         planExpiry(now),
		CreatedAt:         now,
	}
	if err := s.ds.ScanPlan(ctx).Put(plan); err != nil {
		return "", err
	}

	go s.runPreview(ctx, plan.ID, fullScan, req.Targets)
	return plan.ID, nil
}

func (s *controller) runPreview(ctx context.Context, planID string, fullScan bool, targets []model.ScanTarget) {
	release, err := lockScan(ctx)
	if err != nil {
		msg := "another scan is running, please wait and retry"
		_, _ = s.ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusRunning, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  msg,
		})
		s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModePreview, Error: msg, Done: true})
		return
	}
	defer release()

	progress := make(chan *ProgressInfo, 100)
	done := make(chan struct{})
	go s.forwardPlanProgress(progress, done, planID, PlanModePreview)

	s.getScanner().previewFolders(ctx, fullScan, targets, planID, progress)
	close(progress)
	<-done
}

// ApplyPlan validates the plan, moves it to "applying", runs the freshness gates and
// returns stale/expired errors synchronously. When the facts still match, the real
// scan is performed in the background (SSE events track it). It implements model.ScanPlanner.
func (s *controller) ApplyPlan(reqCtx context.Context, planID string) error {
	ctx := request.AddValues(reqCtx, s.rootCtx)
	ctx = auth.WithAdminUser(ctx, s.ds)
	repo := s.ds.ScanPlan(ctx)

	plan, err := repo.Get(planID)
	if err != nil {
		return err
	}
	switch plan.Status {
	case model.ScanPlanStatusApplied:
		return nil // idempotent: already applied, no second scan
	case model.ScanPlanStatusApplying:
		return ErrPlanApplying
	case model.ScanPlanStatusRunning:
		return ErrPlanRunning
	case model.ScanPlanStatusFailed:
		return ErrPlanFailed
	}
	if planIsExpired(plan, time.Now()) {
		_, _ = repo.UpdateStatus(planID, plan.Status, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  "plan expired before apply",
		})
		return ErrPlanExpired
	}

	// CAS: only a ready plan can enter applying, once.
	ok, err := repo.UpdateStatus(planID, model.ScanPlanStatusReady, map[string]any{
		"status": model.ScanPlanStatusApplying,
		"error":  "",
	})
	if err != nil {
		return err
	}
	if !ok {
		return ErrPlanApplying
	}

	release, err := lockScan(ctx)
	if err != nil {
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusReady,
			"error":  "another scan is running",
		})
		return ErrAlreadyScanning
	}

	// Verify freshness up-front (the background scan re-verifies and finalizes).
	verifyProgress := make(chan *ProgressInfo, 100)
	done := make(chan struct{})
	go s.forwardPlanProgress(verifyProgress, done, planID, PlanModeApply)
	expired, stale, gateErr := verifyPlanGates(ctx, s.ds, plan, verifyProgress)
	close(verifyProgress)
	<-done

	if gateErr != nil {
		release()
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  gateErr.Error(),
		})
		s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModeApply, Error: gateErr.Error(), Done: true})
		return gateErr
	}
	if expired {
		release()
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  "plan expired before apply",
		})
		s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModeApply, Error: ErrPlanExpired.Error(), Done: true})
		return ErrPlanExpired
	}
	if len(stale) > 0 {
		release()
		applyErr := &PlanApplyError{StaleReasons: stale}
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusReady,
			"error":  applyErr.Error(),
		})
		s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModeApply, Error: applyErr.Error(), Done: true})
		return applyErr
	}

	// Fresh: run the actual scan in the background, keeping the lock.
	go s.runApplyScan(ctx, planID, release)
	return nil
}

func (s *controller) runApplyScan(ctx context.Context, planID string, release func()) {
	defer release()
	progress := make(chan *ProgressInfo, 100)
	done := make(chan struct{})
	go s.forwardPlanProgress(progress, done, planID, PlanModeApply)

	if s.devExternalScanner {
		// Subprocess performs gates + scan; plan row is updated there.
		(&scannerExternal{}).scan(ctx, false, nil, progress, "--apply-plan", planID)
		plan, getErr := s.ds.ScanPlan(ctx).Get(planID)
		if getErr == nil && plan.Status != model.ScanPlanStatusApplied {
			// Surface stale/rejected state as the terminal error event.
			s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModeApply, Error: plan.Error, Done: true})
		}
	} else {
		plan, getErr := s.ds.ScanPlan(ctx).Get(planID)
		if getErr != nil {
			s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModeApply, Error: getErr.Error(), Done: true})
		} else if err := completePlanApply(ctx, s.ds, s.pls, plan, progress); err != nil {
			s.broker.SendBroadcastMessage(ctx, &events.ScanPlanStatus{PlanID: planID, Mode: PlanModeApply, Error: err.Error(), Done: true})
		}
	}
	close(progress)
	<-done
}

// forwardPlanProgress converts scanner progress into SSE ScanPlanStatus events.
func (s *controller) forwardPlanProgress(in <-chan *ProgressInfo, done chan<- struct{}, planID, mode string) {
	defer close(done)
	var count, folders atomic.Uint32
	for p := range in {
		if p == nil {
			continue
		}
		if p.FileCount > 0 {
			count.Add(uint32(p.FileCount))
			folders.Add(1)
		}
		mode2, id := mode, planID
		if p.Mode != "" {
			mode2 = p.Mode
		}
		if p.PlanID != "" {
			id = p.PlanID
		}
		if p.Warning != "" {
			log.Warn(context.Background(), "Scanner: Plan progress warning", "plan", id, "warning", p.Warning)
		}
		if p.Error != "" {
			log.Error(context.Background(), "Scanner: Plan progress error", "plan", id, "error", p.Error)
		}
		s.broker.SendBroadcastMessage(context.Background(), &events.ScanPlanStatus{
			PlanID: id, Mode: mode2, Phase: p.Phase, Stage: p.Stage,
			LibID: p.LibID, Count: int64(count.Load()), FolderCount: int64(folders.Load()),
			Path: p.Path, Warning: p.Warning, Error: p.Error,
		})
	}
	s.broker.SendBroadcastMessage(context.Background(), &events.ScanPlanStatus{PlanID: planID, Mode: mode, Done: true})
}
