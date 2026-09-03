package scanner

import (
	"context"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Plan facts", func() {
	var (
		ctx context.Context
		ds  *tests.MockDataStore
		lr  *tests.MockLibraryRepo
	)

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
		ctx = context.Background()
		lr = &tests.MockLibraryRepo{}
		lr.SetData(model.Libraries{
			{ID: 1, Name: "Lib 1", Path: "/music", LastScanAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				LastScanStartedAt: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), TotalSongs: 10, TotalFolders: 5},
			{ID: 2, Name: "Lib 2", Path: "/more-music", LastScanAt: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC),
				TotalSongs: 20, TotalFolders: 8},
		})
		ds = &tests.MockDataStore{MockedLibrary: lr}
		// Playlist mock: start empty
		ds.MockedPlaylist = tests.CreateMockPlaylistRepo()
	})

	Describe("config snapshot", func() {
		It("hashes the same config identically", func() {
			snap := currentPlanConfigSnapshot()
			Expect(hashJSON(snap)).To(Equal(hashJSON(currentPlanConfigSnapshot())))
		})

		It("flags a stale configuration when a result-relevant option changes", func() {
			plan := &model.ScanPlan{ConfigFingerprint: hashJSON(currentPlanConfigSnapshot())}
			Expect(checkConfigStaleness(plan)).To(BeNil())

			conf.Server.Scanner.PurgeMissing = consts.PurgeMissingAlways
			reason := checkConfigStaleness(plan)
			Expect(reason).ToNot(BeNil())
			Expect(reason.Code).To(Equal(model.StaleReasonConfigChanged))

			conf.Server.Scanner.PurgeMissing = consts.PurgeMissingNever
			conf.Server.PID.Album = "different"
			Expect(checkConfigStaleness(plan).Code).To(Equal(model.StaleReasonConfigChanged))
		})

		It("does not flag unrelated configuration changes", func() {
			plan := &model.ScanPlan{ConfigFingerprint: hashJSON(currentPlanConfigSnapshot())}
			conf.Server.LogLevel = "debug" // not part of the snapshot
			Expect(checkConfigStaleness(plan)).To(BeNil())
		})
	})

	Describe("DB facts", func() {
		It("reports fresh when nothing changed", func() {
			facts, err := collectPlanDBFacts(ctx, ds, nil)
			Expect(err).ToNot(HaveOccurred())
			plan := &model.ScanPlan{DbFingerprint: stableJSON(facts)}

			reasons, err := checkDBStaleness(ctx, ds, plan)
			Expect(err).ToNot(HaveOccurred())
			Expect(reasons).To(BeEmpty())
		})

		It("flags library changes (re-scan/reconfigure)", func() {
			facts, _ := collectPlanDBFacts(ctx, ds, nil)
			plan := &model.ScanPlan{DbFingerprint: stableJSON(facts)}

			data := lr.Data[1]
			data.LastScanAt = time.Now()
			lr.Data[1] = data

			reasons, err := checkDBStaleness(ctx, ds, plan)
			Expect(err).ToNot(HaveOccurred())
			Expect(reasons).To(ContainElement(HaveField("Code", model.StaleReasonLibraryChanged)))
		})

		It("flags media record count changes", func() {
			facts, _ := collectPlanDBFacts(ctx, ds, nil)
			plan := &model.ScanPlan{DbFingerprint: stableJSON(facts)}

			data := lr.Data[2]
			data.TotalSongs = 42
			lr.Data[2] = data

			reasons, err := checkDBStaleness(ctx, ds, plan)
			Expect(err).ToNot(HaveOccurred())
			Expect(reasons).To(ContainElement(HaveField("Code", model.StaleReasonMediaChanged)))
		})

		It("flags playlist changes", func() {
			facts, _ := collectPlanDBFacts(ctx, ds, nil)
			plan := &model.ScanPlan{DbFingerprint: stableJSON(facts)}

			pr := tests.CreateMockPlaylistRepo()
			pr.SetData(model.Playlists{
				{ID: "pl1", Name: "Mix", UpdatedAt: time.Now()},
			})
			ds.MockedPlaylist = pr

			reasons, err := checkDBStaleness(ctx, ds, plan)
			Expect(err).ToNot(HaveOccurred())
			Expect(reasons).To(ContainElement(HaveField("Code", model.StaleReasonPlaylistsChanged)))
		})

		It("scopes facts to targeted libraries", func() {
			targets := []model.ScanTarget{{LibraryID: 1}}
			facts, err := collectPlanDBFacts(ctx, ds, targets)
			Expect(err).ToNot(HaveOccurred())
			Expect(facts.Libraries).To(HaveLen(1))
			Expect(facts.Libraries[0].ID).To(Equal(1))
		})

		It("errors on unknown target library", func() {
			_, _, err := scopeLibraries(ctx, ds, []model.ScanTarget{{LibraryID: 99}})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("TTL / expiration", func() {
		It("returns an expiry when the TTL is positive", func() {
			conf.Server.Scanner.ScanPlanTTL = 24 * time.Hour
			now := time.Now()
			exp := planExpiry(now)
			Expect(exp).ToNot(BeNil())
			Expect(exp.Sub(now)).To(BeNumerically("~", 24*time.Hour, time.Second))

			Expect(planIsExpired(&model.ScanPlan{ExpiresAt: exp}, now)).To(BeFalse())
			Expect(planIsExpired(&model.ScanPlan{ExpiresAt: exp}, now.Add(25*time.Hour))).To(BeTrue())
		})

		It("does not expire when TTL is zero", func() {
			conf.Server.Scanner.ScanPlanTTL = 0
			Expect(planExpiry(time.Now())).To(BeNil())
			Expect(planIsExpired(&model.ScanPlan{}, time.Now())).To(BeFalse())
		})
	})
})
