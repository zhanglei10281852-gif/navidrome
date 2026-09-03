package scanner_test

import (
	"context"
	"path/filepath"
	"testing/fstest"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/artwork"
	"github.com/navidrome/navidrome/core/metrics"
	"github.com/navidrome/navidrome/core/playlists"
	"github.com/navidrome/navidrome/core/storage/storagetest"
	"github.com/navidrome/navidrome/db"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/persistence"
	"github.com/navidrome/navidrome/scanner"
	"github.com/navidrome/navidrome/server/events"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Scan plan preview/apply", Ordered, func() {
	var (
		ctx context.Context
		ds  *tests.MockDataStore
		s   model.Scanner
		lib model.Library
	)

	BeforeAll(func() {
		ctx = request.WithUser(GinkgoT().Context(), model.User{ID: "plan-admin", IsAdmin: true})
		conf.Server.DbPath = filepath.Join(GinkgoT().TempDir(), "test-plan-scan.db?_journal_mode=WAL")
		db.Db().SetMaxOpenConns(1)
	})

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
		conf.Server.MusicFolder = "fake:///music"
		conf.Server.DevExternalScanner = false

		db.Init(ctx)
		DeferCleanup(func() { Expect(tests.ClearDB()).To(Succeed()) })

		ds = &tests.MockDataStore{RealDS: persistence.New(db.Db())}

		adminUser := model.User{
			ID: "plan-admin", UserName: "admin", Name: "Admin User", IsAdmin: true,
			NewPassword: "password",
		}
		Expect(ds.User(ctx).Put(&adminUser)).To(Succeed())

		s = scanner.New(ctx, ds, events.NoopBroker(),
			playlists.NewPlaylists(ds, artwork.NewUploader(ds)), metrics.NewNoopInstance())

		lib = model.Library{ID: 1, Name: "Fake Library", Path: "fake:///music"}
		Expect(ds.Library(ctx).Put(&lib)).To(Succeed())
	})

	runScan := func(fullScan bool) error {
		_, err := s.ScanAll(ctx, fullScan)
		return err
	}

	Describe("plan previews", Ordered, func() {
		var (
			planner model.ScanPlanner
			fsys    storagetest.FakeFS
		)

		BeforeEach(func() {
			// Reuse the main suite's context/ds/lib setup.
			planner = s.(model.ScanPlanner)

			revolver := template(_t{"albumartist": "The Beatles", "album": "Revolver", "year": 1966})
			help := template(_t{"albumartist": "The Beatles", "album": "Help!", "year": 1965})
			fsys = createFS(fstest.MapFS{
				"The Beatles/Revolver/01 - Taxman.mp3":        revolver(track(1, "Taxman")),
				"The Beatles/Revolver/02 - Eleanor Rigby.mp3": revolver(track(2, "Eleanor Rigby")),
				"The Beatles/Help!/01 - Help!.mp3":            help(track(1, "Help!")),
			})
		})

		preview := func(fullScan bool) *model.ScanPlan {
			id, err := planner.PreviewPlan(ctx, model.PlanRequest{FullScan: fullScan})
			Expect(err).ToNot(HaveOccurred())
			var plan *model.ScanPlan
			Eventually(func() string {
				p, err := ds.ScanPlan(ctx).Get(id)
				Expect(err).ToNot(HaveOccurred())
				plan = p
				return p.Status
			}).WithTimeout(15 * time.Second).WithPolling(100 * time.Millisecond).
				Should(BeElementOf(model.ScanPlanStatusReady, model.ScanPlanStatusFailed))
			Expect(plan.Status).To(Equal(model.ScanPlanStatusReady), plan.Error)
			return plan
		}
		itemsOfType := func(planID, changeType string) model.ScanPlanItems {
			all, err := ds.ScanPlan(ctx).GetItems(planID)
			Expect(err).ToNot(HaveOccurred())
			var out model.ScanPlanItems
			for _, it := range all {
				if it.ChangeType == changeType {
					out = append(out, it)
				}
			}
			return out
		}

		It("reports new tracks as added without changing media state", func() {
			before, err := ds.MediaFile(ctx).CountAll()
			Expect(err).ToNot(HaveOccurred())

			plan := preview(true)
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeAdded)).To(HaveLen(3))

			after, err := ds.MediaFile(ctx).CountAll()
			Expect(err).ToNot(HaveOccurred())
			Expect(after).To(Equal(before), "preview must not import media files")
		})

		It("collects warnings for unreadable files without writing anything", func() {
			fsys.Add("The Beatles/Revolver/broken.mp3", &fstest.MapFile{Data: []byte("not an audio file")})
			plan := preview(true)
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeWarning)).ToNot(BeEmpty(), "bad tags produce warnings")
			Expect(ds.MediaFile(ctx).CountAll()).To(Equal(int64(0)))
		})

		It("detects added/updated/missing changes after a real scan", func() {
			// First establish the baseline with a real scan.
			Expect(runScan(true)).To(Succeed())
			Expect(ds.MediaFile(ctx).CountAll()).To(Equal(int64(3)))

			revolver := template(_t{"albumartist": "The Beatles", "album": "Revolver", "year": 1966})
			fsys.Add("The Beatles/Revolver/03 - New Song.mp3", revolver(track(3, "New Song")), time.Now())
			fsys.UpdateTags("The Beatles/Revolver/01 - Taxman.mp3",
				_t{"albumartist": "The Beatles", "album": "Revolver (Remaster)", "year": 1966},
				time.Now().Add(2*time.Hour))
			fsys.Remove("The Beatles/Help!/01 - Help!.mp3")

			plan := preview(false)
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeAdded)).To(HaveLen(1))
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeUpdated)).To(HaveLen(1))
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeMissing)).To(HaveLen(1))

			// Media state untouched by the preview.
			Expect(ds.MediaFile(ctx).CountAll()).To(Equal(int64(3)))
		})

		It("classifies a moved file as moved, not added+missing", func() {
			Expect(runScan(true)).To(Succeed())
			fsys.Move("The Beatles/Revolver/02 - Eleanor Rigby.mp3",
				"The Beatles/Help!/Eleanor Rigby.mp3", time.Now())

			plan := preview(false)
			moved := itemsOfType(plan.ID, model.ScanPlanChangeMoved)
			Expect(moved).To(HaveLen(1))
			Expect(moved[0].OldPath).To(ContainSubstring("Eleanor Rigby"))
			Expect(moved[0].Path).To(ContainSubstring("Eleanor Rigby"))
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeAdded)).To(BeEmpty())
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeMissing)).To(BeEmpty())
		})

		It("reports purge items under PurgeMissing=always", func() {
			Expect(runScan(true)).To(Succeed())
			fsys.Remove("The Beatles/Help!/01 - Help!.mp3")
			conf.Server.Scanner.PurgeMissing = consts.PurgeMissingAlways

			plan := preview(false)
			purge := itemsOfType(plan.ID, model.ScanPlanChangePurge)
			Expect(purge).To(HaveLen(1))
			Expect(itemsOfType(plan.ID, model.ScanPlanChangeMissing)).To(BeEmpty())
		})
	})

	Describe("plan apply", Ordered, func() {
		var planner model.ScanPlanner
		var fsys storagetest.FakeFS

		BeforeEach(func() {
			planner = s.(model.ScanPlanner)
			revolver := template(_t{"albumartist": "The Beatles", "album": "Revolver", "year": 1966})
			fsys = createFS(fstest.MapFS{
				"The Beatles/Revolver/01 - Taxman.mp3":        revolver(track(1, "Taxman")),
				"The Beatles/Revolver/02 - Eleanor Rigby.mp3": revolver(track(2, "Eleanor Rigby")),
			})
		})

		It("applies a fresh plan (preview then real scan) and is idempotent", func() {
			id, err := planner.PreviewPlan(ctx, model.PlanRequest{FullScan: true})
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() string {
				p, _ := ds.ScanPlan(ctx).Get(id)
				return p.Status
			}).WithTimeout(15 * time.Second).WithPolling(100 * time.Millisecond).
				Should(Equal(model.ScanPlanStatusReady))

			Expect(planner.ApplyPlan(ctx, id)).To(Succeed())

			Eventually(func() string {
				p, err := ds.ScanPlan(ctx).Get(id)
				Expect(err).ToNot(HaveOccurred())
				return p.Status
			}).WithTimeout(20 * time.Second).WithPolling(100 * time.Millisecond).
				Should(Equal(model.ScanPlanStatusApplied))
			Expect(ds.MediaFile(ctx).CountAll()).To(Equal(int64(2)))

			// A second confirmation must not run another scan.
			Expect(planner.ApplyPlan(ctx, id)).To(Succeed())
		})

		It("rejects applying a stale plan with structured reasons", func() {
			id, err := planner.PreviewPlan(ctx, model.PlanRequest{FullScan: true})
			Expect(err).ToNot(HaveOccurred())
			Eventually(func() string {
				p, _ := ds.ScanPlan(ctx).Get(id)
				return p.Status
			}).WithTimeout(15 * time.Second).WithPolling(100 * time.Millisecond).
				Should(Equal(model.ScanPlanStatusReady))

			// Files changed after the preview.
			templ := storagetest.Template(_t{"albumartist": "The Beatles", "album": "Revolver", "year": 1966})
			fsys.Add("The Beatles/Revolver/99 - Surprise.mp3", templ(storagetest.Track(9, "Surprise")), time.Now())

			err = planner.ApplyPlan(ctx, id)
			Expect(err).To(HaveOccurred())
			Expect(err).To(BeAssignableToTypeOf(&scanner.PlanApplyError{}))
			Expect(staleReasonCodes(err)).To(ContainElement(model.StaleReasonFilesystemChanged))

			// The plan stays ready (not applied) and media were not imported.
			p, _ := ds.ScanPlan(ctx).Get(id)
			Expect(p.Status).To(Equal(model.ScanPlanStatusReady))
		})

		It("rejects applying an expired plan", func() {
			// Wait until the previous apply scan finished to avoid lock contention.
			Eventually(func() bool { return !scanner.IsScanning() }).
				WithTimeout(20 * time.Second).WithPolling(100 * time.Millisecond).Should(BeTrue())

			past := time.Now().Add(-time.Hour)
			expired := &model.ScanPlan{
				ID: "expired-plan", Status: model.ScanPlanStatusReady,
				ConfigFingerprint: "x", DbFingerprint: `{"libraries":[]}`,
				Targets: "[]", LibraryIDs: "[]",
				ExpiresAt: &past,
			}
			Expect(ds.ScanPlan(ctx).Put(expired)).To(Succeed())

			err := planner.ApplyPlan(ctx, expired.ID)
			Expect(err).To(MatchError(scanner.ErrPlanExpired))
		})
	})
})

func staleReasonCodes(err error) []string {
	if applyErr, ok := err.(*scanner.PlanApplyError); ok {
		codes := make([]string, 0, len(applyErr.StaleReasons))
		for _, r := range applyErr.StaleReasons {
			codes = append(codes, r.Code)
		}
		return codes
	}
	return nil
}
