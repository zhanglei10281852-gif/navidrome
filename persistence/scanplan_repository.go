package persistence

import (
	"context"
	"time"

	. "github.com/Masterminds/squirrel"
	"github.com/navidrome/navidrome/model"
	id2 "github.com/navidrome/navidrome/model/id"
	"github.com/pocketbase/dbx"
)

const scanPlanTable = "scan_plan"
const scanPlanItemTable = "scan_plan_item"

type scanPlanRepository struct {
	sqlRepository
}

func NewScanPlanRepository(ctx context.Context, db dbx.Builder) model.ScanPlanRepository {
	r := &scanPlanRepository{}
	r.ctx = ctx
	r.db = db
	r.tableName = scanPlanTable
	return r
}

func (r *scanPlanRepository) Put(plan *model.ScanPlan) error {
	now := time.Now()
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = now
	}
	plan.UpdatedAt = now
	if plan.ID == "" {
		plan.ID = id2.NewRandom()
	}

	_, err := r.db.NewQuery(`
		INSERT INTO scan_plan (id, status, full_scan, targets, library_ids, config_fingerprint, config_snapshot,
			fs_fingerprint, db_fingerprint, summary, warnings_total, error, expires_at, created_at, updated_at, applied_at)
		VALUES ({:id}, {:status}, {:full_scan}, {:targets}, {:library_ids}, {:config_fingerprint}, {:config_snapshot},
			{:fs_fingerprint}, {:db_fingerprint}, {:summary}, {:warnings_total}, {:error}, {:expires_at}, {:created_at}, {:updated_at}, {:applied_at})
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			full_scan = excluded.full_scan,
			targets = excluded.targets,
			library_ids = excluded.library_ids,
			config_fingerprint = excluded.config_fingerprint,
			config_snapshot = excluded.config_snapshot,
			fs_fingerprint = excluded.fs_fingerprint,
			db_fingerprint = excluded.db_fingerprint,
			summary = excluded.summary,
			warnings_total = excluded.warnings_total,
			error = excluded.error,
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at,
			applied_at = excluded.applied_at
	`).Bind(dbx.Params{
		"id":                 plan.ID,
		"status":             plan.Status,
		"full_scan":          plan.FullScan,
		"targets":            plan.Targets,
		"library_ids":        plan.LibraryIDs,
		"config_fingerprint": plan.ConfigFingerprint,
		"config_snapshot":    plan.ConfigSnapshot,
		"fs_fingerprint":     plan.FsFingerprint,
		"db_fingerprint":     plan.DbFingerprint,
		"summary":            plan.Summary,
		"warnings_total":     plan.WarningsTotal,
		"error":              plan.Error,
		"expires_at":         plan.ExpiresAt,
		"created_at":         plan.CreatedAt,
		"updated_at":         plan.UpdatedAt,
		"applied_at":         plan.AppliedAt,
	}).Execute()
	return err
}

func (r *scanPlanRepository) Get(id string) (*model.ScanPlan, error) {
	sel := r.newSelect().Columns("*").Where(Eq{"id": id})
	res := model.ScanPlan{}
	if err := r.queryOne(sel, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *scanPlanRepository) GetAll(options ...model.QueryOptions) (model.ScanPlans, error) {
	sel := r.newSelect(options...).Columns("*")
	res := model.ScanPlans{}
	err := r.queryAll(sel, &res, options...)
	return res, err
}

func (r *scanPlanRepository) CountAll(options ...model.QueryOptions) (int64, error) {
	return r.count(r.newSelect(), options...)
}

func (r *scanPlanRepository) Delete(id string) error {
	if _, err := r.executeSQL(Delete(scanPlanItemTable).Where(Eq{"plan_id": id})); err != nil {
		return err
	}
	return r.delete(Eq{"id": id})
}

func (r *scanPlanRepository) UpdateStatus(id string, expect string, set map[string]any) (bool, error) {
	upd := Update(scanPlanTable).
		Where(Eq{"id": id, "status": expect}).
		SetMap(set)
	count, err := r.executeSQL(upd)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *scanPlanRepository) AddItems(items ...model.ScanPlanItem) error {
	if len(items) == 0 {
		return nil
	}
	now := time.Now()
	insert := Insert(scanPlanItemTable).Columns(
		"id", "plan_id", "library_id", "change_type", "item_type", "media_file_id",
		"path", "old_path", "title", "artist", "album", "details", "created_at")
	for i := range items {
		it := &items[i]
		if it.ID == "" {
			it.ID = id2.NewRandom()
		}
		if it.CreatedAt.IsZero() {
			it.CreatedAt = now
		}
		insert = insert.Values(
			it.ID, it.PlanID, it.LibraryID, it.ChangeType, it.ItemType, it.MediaFileID,
			it.Path, it.OldPath, it.Title, it.Artist, it.Album, it.Details, it.CreatedAt)
	}
	_, err := r.executeSQL(insert)
	return err
}

func (r *scanPlanRepository) GetItems(planID string, options ...model.QueryOptions) (model.ScanPlanItems, error) {
	sel := Select("*").From(scanPlanItemTable).Where(Eq{"plan_id": planID})
	if len(options) > 0 {
		sel = r.applyOptions(sel, options...)
		sel = r.applyFilters(sel, options...)
	}
	res := model.ScanPlanItems{}
	query, args, err := r.toSQL(sel)
	if err != nil {
		return nil, err
	}
	err = r.db.NewQuery(query).Bind(args).WithContext(r.ctx).All(&res)
	return res, err
}

func (r *scanPlanRepository) CountItems(planID string, options ...model.QueryOptions) (int64, error) {
	sel := Select("count(*) as count").From(scanPlanItemTable).Where(Eq{"plan_id": planID})
	if len(options) > 0 {
		sel = r.applyFilters(sel, options...)
	}
	var res struct{ Count int64 }
	err := r.queryOne(sel, &res)
	return res.Count, err
}

func (r *scanPlanRepository) FailInterrupted(now time.Time) (int64, error) {
	upd := Update(scanPlanTable).
		Where(Eq{"status": []string{model.ScanPlanStatusRunning, model.ScanPlanStatusApplying}}).
		SetMap(map[string]any{
			"status":     model.ScanPlanStatusFailed,
			"error":      "interrupted by server restart",
			"updated_at": now,
		})
	return r.executeSQL(upd)
}

var _ model.ScanPlanRepository = (*scanPlanRepository)(nil)
