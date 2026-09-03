package scanner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/core/playlists"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// PlanApplyError reports that a plan could not be applied because it expired or the
// bound facts no longer match.
type PlanApplyError struct {
	Expired      bool
	StaleReasons []model.PlanStaleReason
}

func (e *PlanApplyError) Error() string {
	if e.Expired {
		return "scan plan expired: create a new preview to apply changes"
	}
	codes := make([]string, 0, len(e.StaleReasons))
	for _, r := range e.StaleReasons {
		codes = append(codes, r.Code)
	}
	return "scan plan is no longer fresh (" + strings.Join(codes, ", ") + "): create a new preview"
}

// verifyPlanGates runs the full freshness verification (expiry, configuration, DB
// facts, filesystem facts). It returns expired=true or the list of stale reasons
// (empty when fresh). Shared between the controller service and the subprocess path.
func verifyPlanGates(ctx context.Context, ds model.DataStore, plan *model.ScanPlan, verifyProgress chan<- *ProgressInfo) (expired bool, stale []model.PlanStaleReason, err error) {
	if planIsExpired(plan, time.Now()) {
		return true, nil, nil
	}
	if reason := checkConfigStaleness(plan); reason != nil {
		stale = append(stale, *reason)
	}
	dbReasons, err := checkDBStaleness(ctx, ds, plan)
	if err != nil {
		return false, nil, fmt.Errorf("verifying db facts: %w", err)
	}
	stale = append(stale, dbReasons...)
	if reason := checkFSStaleness(ctx, ds, plan, verifyProgress); reason != nil {
		stale = append(stale, *reason)
	}
	return false, stale, nil
}

// CallPreviewPlan runs a plan preview with the single-flight lock, mirroring
// CallScan for the subprocess code path.
func CallPreviewPlan(ctx context.Context, ds model.DataStore, pls playlists.Playlists, fullScan bool, targets []model.ScanTarget, planID string) <-chan *ProgressInfo {
	release, err := lockScan(ctx)
	if err != nil {
		log.Error(ctx, "Scanner: Could not start plan preview", err)
		return nil
	}
	progress := make(chan *ProgressInfo, 100)
	scanner := &scannerImpl{ds: ds, pls: pls}
	go func() {
		defer close(progress)
		defer release()
		scanner.previewFolders(ctx, fullScan, targets, planID, progress)
	}()
	return progress
}

// RunPlanApply executes the plan freshness gate and then a real scan. It is shared by
// the in-process controller service and the CLI subprocess (--apply-plan):
// status CAS (ready→applying) is done by the caller; this function expects the plan
// in "applying" state. On any gate failure it moves the plan back to "ready"
// (or to a terminal "expired"/"applied" state) and returns a *PlanApplyError.
func RunPlanApply(ctx context.Context, ds model.DataStore, pls playlists.Playlists, planID string, externalProgress chan<- *ProgressInfo) error {
	repo := ds.ScanPlan(ctx)
	plan, err := repo.Get(planID)
	if err != nil {
		return fmt.Errorf("loading scan plan: %w", err)
	}

	// Idempotent: already applied by a concurrent/previous confirmation.
	if plan.Status == model.ScanPlanStatusApplied {
		log.Info(ctx, "Scanner: Plan already applied", "plan", planID)
		return nil
	}
	if plan.Status != model.ScanPlanStatusApplying {
		return fmt.Errorf("scan plan %s is not applying (status: %s)", planID, plan.Status)
	}

	// Gates: expiration + configuration + DB facts + filesystem facts.
	verifyProgress := make(chan *ProgressInfo, 100)
	go forwardPlanProgress(verifyProgress, externalProgress, planID)
	expired, stale, err := verifyPlanGates(ctx, ds, plan, verifyProgress)
	close(verifyProgress)
	if err != nil {
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  err.Error(),
		})
		return err
	}
	if expired {
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  "plan expired before apply",
		})
		applyErr := &PlanApplyError{Expired: true}
		if externalProgress != nil {
			externalProgress <- &ProgressInfo{Mode: PlanModeApply, PlanID: planID, Error: applyErr.Error()}
		}
		return applyErr
	}
	if len(stale) > 0 {
		msg := "plan rejected: " + strings.Join(sliceReasonCodes(stale), ", ")
		_, _ = repo.UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusReady,
			"error":  msg,
		})
		log.Warn(ctx, "Scanner: Plan apply rejected, facts changed", "plan", planID, "reasons", sliceReasonCodes(stale))
		applyErr := &PlanApplyError{StaleReasons: stale}
		if externalProgress != nil {
			externalProgress <- &ProgressInfo{Mode: PlanModeApply, PlanID: planID, Error: applyErr.Error()}
		}
		return applyErr
	}

	// Fresh: run the real scan with the same parameters, tagging progress as apply.
	return completePlanApply(ctx, ds, pls, plan, externalProgress)
}

// completePlanApply runs the real scan for a freshly-verified plan and marks it
// applied (or back to ready on scan error). It does NOT re-run freshness gates.
func completePlanApply(ctx context.Context, ds model.DataStore, pls playlists.Playlists, plan *model.ScanPlan, externalProgress chan<- *ProgressInfo) error {
	planID := plan.ID
	targets, err := decodePlanTargets(plan)
	if err != nil {
		_, _ = ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  err.Error(),
		})
		return err
	}

	scanProgress := make(chan *ProgressInfo, 100)
	var scanErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Tee scan progress: tag/forward to the caller, and capture any scan error so a
		// failed apply leaves the plan ready (not applied).
		for p := range scanProgress {
			if p == nil {
				continue
			}
			if p.Error != "" {
				scanErr = fmt.Errorf("%s", p.Error)
			}
			if p.PlanID == "" {
				p.PlanID = planID
			}
			if p.Mode == "" {
				p.Mode = PlanModeApply
			}
			if externalProgress != nil {
				externalProgress <- p
			}
		}
	}()
	scanner := &scannerImpl{ds: ds, pls: pls}
	scanner.scanFolders(ctx, plan.FullScan, targets, scanProgress)
	close(scanProgress)
	wg.Wait()

	if scanErr != nil {
		_, _ = ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusReady,
			"error":  scanErr.Error(),
		})
		return scanErr
	}

	now := time.Now()
	ok, err := ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
		"status":     model.ScanPlanStatusApplied,
		"applied_at": &now,
		"error":      "",
	})
	if err != nil {
		_, _ = ds.ScanPlan(ctx).UpdateStatus(planID, model.ScanPlanStatusApplying, map[string]any{
			"status": model.ScanPlanStatusFailed,
			"error":  err.Error(),
		})
		return fmt.Errorf("finalizing applied plan: %w", err)
	}
	if !ok {
		// The plan was moved while scanning (e.g. interrupted recovery).
		if current, errGet := ds.ScanPlan(ctx).Get(planID); errGet == nil && current.Status != model.ScanPlanStatusApplied {
			_, _ = ds.ScanPlan(ctx).UpdateStatus(planID, current.Status, map[string]any{
				"status": model.ScanPlanStatusFailed,
				"error":  "plan status changed during apply",
			})
			return fmt.Errorf("plan %s changed state during apply", planID)
		}
	}
	log.Info(ctx, "Scanner: Plan applied", "plan", planID)
	return nil
}

// forwardPlanProgress tags progress with the plan id and apply mode before forwarding.
func forwardPlanProgress(in <-chan *ProgressInfo, out chan<- *ProgressInfo, planID string) {
	if out == nil {
		// Drain
		for range in {
		}
		return
	}
	for p := range in {
		if p == nil {
			continue
		}
		if p.PlanID == "" {
			p.PlanID = planID
		}
		if p.Mode == "" {
			p.Mode = PlanModeApply
		}
		out <- p
	}
}

func sliceReasonCodes(reasons []model.PlanStaleReason) []string {
	codes := make([]string, 0, len(reasons))
	for _, r := range reasons {
		codes = append(codes, r.Code)
	}
	return codes
}
