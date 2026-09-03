package persistence

import (
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/navidrome/navidrome/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ScanPlanRepository", func() {
	var repo model.ScanPlanRepository

	BeforeEach(func() {
		repo = NewScanPlanRepository(GinkgoT().Context(), GetDBXBuilder())
		// Clean up any leftover plans/items from other tests
		_, _ = GetDBXBuilder().NewQuery("DELETE FROM scan_plan_item").Execute()
		_, _ = GetDBXBuilder().NewQuery("DELETE FROM scan_plan").Execute()
	})

	AfterEach(func() {
		_, _ = GetDBXBuilder().NewQuery("DELETE FROM scan_plan_item").Execute()
		_, _ = GetDBXBuilder().NewQuery("DELETE FROM scan_plan").Execute()
	})

	Describe("Put/Get", func() {
		It("round-trips a plan with all fields", func() {
			expires := time.Now().Add(24 * time.Hour).Truncate(time.Second)
			plan := &model.ScanPlan{
				ID:                "plan-roundtrip",
				Status:            model.ScanPlanStatusReady,
				FullScan:          true,
				Targets:           `[{"libraryId":1,"folderPath":"Rock"}]`,
				LibraryIDs:        `[1,2]`,
				ConfigFingerprint: "cfp",
				ConfigSnapshot:    `{"Scanner.PurgeMissing":"never"}`,
				FsFingerprint:     "fsfp",
				DbFingerprint:     `{"libs":[]}`,
				Summary:           `{"libraries":{}}`,
				WarningsTotal:     3,
				Error:             "",
				ExpiresAt:         &expires,
			}
			Expect(repo.Put(plan)).To(Succeed())

			loaded, err := repo.Get("plan-roundtrip")
			Expect(err).ToNot(HaveOccurred())
			Expect(loaded.Status).To(Equal(model.ScanPlanStatusReady))
			Expect(loaded.FullScan).To(BeTrue())
			Expect(loaded.Targets).To(Equal(`[{"libraryId":1,"folderPath":"Rock"}]`))
			Expect(loaded.LibraryIDs).To(Equal(`[1,2]`))
			Expect(loaded.ConfigFingerprint).To(Equal("cfp"))
			Expect(loaded.ConfigSnapshot).To(Equal(`{"Scanner.PurgeMissing":"never"}`))
			Expect(loaded.FsFingerprint).To(Equal("fsfp"))
			Expect(loaded.WarningsTotal).To(Equal(3))
			Expect(loaded.ExpiresAt).ToNot(BeNil())
			Expect(loaded.CreatedAt).ToNot(BeZero())
			Expect(loaded.UpdatedAt).ToNot(BeZero())
		})

		It("updates an existing plan on conflict", func() {
			Expect(repo.Put(&model.ScanPlan{ID: "plan-upsert", Status: model.ScanPlanStatusRunning})).To(Succeed())
			Expect(repo.Put(&model.ScanPlan{ID: "plan-upsert", Status: model.ScanPlanStatusReady, FsFingerprint: "fp"})).To(Succeed())

			loaded, err := repo.Get("plan-upsert")
			Expect(err).ToNot(HaveOccurred())
			Expect(loaded.Status).To(Equal(model.ScanPlanStatusReady))
			Expect(loaded.FsFingerprint).To(Equal("fp"))
		})

		It("returns ErrNotFound for missing plans", func() {
			_, err := repo.Get("does-not-exist")
			Expect(err).To(MatchError(model.ErrNotFound))
		})
	})

	Describe("GetAll/CountAll", func() {
		It("lists plans with pagination and sorting", func() {
			Expect(repo.Put(&model.ScanPlan{ID: "p1", Status: model.ScanPlanStatusReady})).To(Succeed())
			Expect(repo.Put(&model.ScanPlan{ID: "p2", Status: model.ScanPlanStatusApplied})).To(Succeed())
			Expect(repo.Put(&model.ScanPlan{ID: "p3", Status: model.ScanPlanStatusReady})).To(Succeed())

			count, err := repo.CountAll()
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(int64(3)))

			plans, err := repo.GetAll(model.QueryOptions{Sort: "id", Order: "asc", Max: 2})
			Expect(err).ToNot(HaveOccurred())
			Expect(plans).To(HaveLen(2))
			Expect(plans[0].ID).To(Equal("p1"))

			plans, err = repo.GetAll(model.QueryOptions{
				Filters: squirrel.Eq{"status": model.ScanPlanStatusReady},
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(plans).To(HaveLen(2))
		})
	})

	Describe("Items", func() {
		BeforeEach(func() {
			Expect(repo.Put(&model.ScanPlan{ID: "plan-items", Status: model.ScanPlanStatusReady})).To(Succeed())
		})

		It("adds and filters items, with counts", func() {
			items := []model.ScanPlanItem{
				{PlanID: "plan-items", LibraryID: 1, ChangeType: model.ScanPlanChangeAdded, ItemType: model.ScanPlanItemMedia, Path: "a.mp3", Title: "A"},
				{PlanID: "plan-items", LibraryID: 1, ChangeType: model.ScanPlanChangeMoved, ItemType: model.ScanPlanItemMedia, Path: "new.mp3", OldPath: "old.mp3", Title: "Moved"},
				{PlanID: "plan-items", LibraryID: 2, ChangeType: model.ScanPlanChangeAdded, ItemType: model.ScanPlanItemMedia, Path: "b.mp3", Title: "B"},
				{PlanID: "plan-items", ChangeType: model.ScanPlanChangeWarning, ItemType: model.ScanPlanItemWarning, Title: "bad tags"},
			}
			Expect(repo.AddItems(items...)).To(Succeed())

			count, err := repo.CountItems("plan-items")
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(Equal(int64(4)))

			all, err := repo.GetItems("plan-items", model.QueryOptions{Sort: "path", Order: "asc"})
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(HaveLen(4))

			lib1, err := repo.GetItems("plan-items", model.QueryOptions{Filters: squirrel.Eq{"library_id": 1}})
			Expect(err).ToNot(HaveOccurred())
			Expect(lib1).To(HaveLen(2))

			moved, err := repo.GetItems("plan-items", model.QueryOptions{Filters: squirrel.Eq{"change_type": model.ScanPlanChangeMoved}})
			Expect(err).ToNot(HaveOccurred())
			Expect(moved).To(HaveLen(1))
			Expect(moved[0].OldPath).To(Equal("old.mp3"))
			Expect(moved[0].Path).To(Equal("new.mp3"))

			// Items belong to the plan: another plan sees none
			Expect(repo.Put(&model.ScanPlan{ID: "plan-other", Status: model.ScanPlanStatusReady})).To(Succeed())
			other, err := repo.GetItems("plan-other")
			Expect(err).ToNot(HaveOccurred())
			Expect(other).To(BeEmpty())
		})

		It("assigns ids to items", func() {
			Expect(repo.AddItems(model.ScanPlanItem{PlanID: "plan-items", ChangeType: model.ScanPlanChangeAdded})).To(Succeed())
			all, err := repo.GetItems("plan-items")
			Expect(err).ToNot(HaveOccurred())
			Expect(all).To(HaveLen(1))
			Expect(all[0].ID).ToNot(BeEmpty())
		})
	})

	Describe("UpdateStatus", func() {
		BeforeEach(func() {
			Expect(repo.Put(&model.ScanPlan{ID: "plan-cas", Status: model.ScanPlanStatusReady})).To(Succeed())
		})

		It("succeeds only when the current status matches", func() {
			ok, err := repo.UpdateStatus("plan-cas", model.ScanPlanStatusReady, map[string]any{
				"status": model.ScanPlanStatusApplying,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			loaded, _ := repo.Get("plan-cas")
			Expect(loaded.Status).To(Equal(model.ScanPlanStatusApplying))

			// A second CAS with the old expectation fails but does not error
			ok, err = repo.UpdateStatus("plan-cas", model.ScanPlanStatusReady, map[string]any{
				"status": model.ScanPlanStatusApplied,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeFalse())

			loaded, _ = repo.Get("plan-cas")
			Expect(loaded.Status).To(Equal(model.ScanPlanStatusApplying))
		})

		It("applies extra columns", func() {
			now := time.Now()
			ok, err := repo.UpdateStatus("plan-cas", model.ScanPlanStatusReady, map[string]any{
				"status":     model.ScanPlanStatusApplied,
				"applied_at": now,
				"error":      "",
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			loaded, _ := repo.Get("plan-cas")
			Expect(loaded.Status).To(Equal(model.ScanPlanStatusApplied))
			Expect(loaded.AppliedAt).ToNot(BeNil())
		})
	})

	Describe("Delete", func() {
		It("deletes the plan and its items", func() {
			Expect(repo.Put(&model.ScanPlan{ID: "plan-del", Status: model.ScanPlanStatusReady})).To(Succeed())
			Expect(repo.AddItems(
				model.ScanPlanItem{PlanID: "plan-del", ChangeType: model.ScanPlanChangeAdded},
				model.ScanPlanItem{PlanID: "plan-del", ChangeType: model.ScanPlanChangeWarning, ItemType: model.ScanPlanItemWarning},
			)).To(Succeed())

			Expect(repo.Delete("plan-del")).To(Succeed())

			_, err := repo.Get("plan-del")
			Expect(err).To(MatchError(model.ErrNotFound))
			count, err := repo.CountItems("plan-del")
			Expect(err).ToNot(HaveOccurred())
			Expect(count).To(BeZero())
		})
	})

	Describe("FailInterrupted", func() {
		It("marks running/applying plans as failed and leaves others untouched", func() {
			Expect(repo.Put(&model.ScanPlan{ID: "p-run", Status: model.ScanPlanStatusRunning})).To(Succeed())
			Expect(repo.Put(&model.ScanPlan{ID: "p-apply", Status: model.ScanPlanStatusApplying})).To(Succeed())
			Expect(repo.Put(&model.ScanPlan{ID: "p-ready", Status: model.ScanPlanStatusReady})).To(Succeed())
			Expect(repo.Put(&model.ScanPlan{ID: "p-applied", Status: model.ScanPlanStatusApplied})).To(Succeed())

			n, err := repo.FailInterrupted(time.Now())
			Expect(err).ToNot(HaveOccurred())
			Expect(n).To(Equal(int64(2)))

			run, _ := repo.Get("p-run")
			Expect(run.Status).To(Equal(model.ScanPlanStatusFailed))
			Expect(run.Error).ToNot(BeEmpty())

			apply, _ := repo.Get("p-apply")
			Expect(apply.Status).To(Equal(model.ScanPlanStatusFailed))

			ready, _ := repo.Get("p-ready")
			Expect(ready.Status).To(Equal(model.ScanPlanStatusReady))

			applied, _ := repo.Get("p-applied")
			Expect(applied.Status).To(Equal(model.ScanPlanStatusApplied))
		})
	})
})
