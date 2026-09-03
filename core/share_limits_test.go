package core

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Share budgets and generations", func() {
	var ds *tests.MockDataStore
	var shareRepo *tests.MockShareRepo
	var svc Share
	ctx := context.Background()

	BeforeEach(func() {
		ds = &tests.MockDataStore{}
		shareRepo = &tests.MockShareRepo{}
		ds.MockedShare = shareRepo
		svc = NewShare(ds)
	})

	Describe("AcquirePlay", func() {
		It("does not limit shares without a configured budget", func() {
			sh := &model.Share{ID: "free", MaxConcurrentPlays: 0}
			for i := 0; i < 50; i++ {
				_, release, err := svc.AcquirePlay(ctx, sh)
				Expect(err).ToNot(HaveOccurred())
				release()
			}
			// Unlimited shares are never gated and their no-op slots are not
			// tracked in the live counter.
			Expect(svc.ActivePlays("free")).To(BeZero())
		})

		It("rejects streams beyond the cap and admits again after release", func() {
			sh := &model.Share{ID: "cap2", MaxConcurrentPlays: 2}
			_, r1, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			_, r2, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			_, _, err = svc.AcquirePlay(ctx, sh)
			Expect(err).To(MatchError(ErrSharePlayLimit))
			Expect(svc.ActivePlays("cap2")).To(Equal(2))

			r1()
			Expect(svc.ActivePlays("cap2")).To(Equal(1))
			_, r3, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			r2()
			r3()
			Expect(svc.ActivePlays("cap2")).To(BeZero())
		})

		It("tolerates repeated release calls", func() {
			sh := &model.Share{ID: "once", MaxConcurrentPlays: 1}
			_, release, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			release()
			release()
			release()
			Expect(svc.ActivePlays("once")).To(BeZero())
		})

		It("cancels the derived context when released", func() {
			sh := &model.Share{ID: "ctx", MaxConcurrentPlays: 1}
			playCtx, release, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			Expect(playCtx.Err()).ToNot(HaveOccurred())
			release()
			Expect(playCtx.Err()).To(MatchError(context.Canceled))
		})
	})

	Describe("ReserveDownload", func() {
		It("allows unlimited downloads when no budget is set", func() {
			sh := &model.Share{ID: "inf", MaxDownloads: 0}
			for i := 0; i < 10; i++ {
				lease, err := svc.ReserveDownload(ctx, sh)
				Expect(err).ToNot(HaveOccurred())
				lease.Release()
			}
		})

		It("never over-issues the last slot under concurrent contention", func() {
			shareRepo.ID = "race"
			shareRepo.Entity = &model.Share{ID: "race", MaxDownloads: 1, DownloadCount: 0}
			sh := &model.Share{ID: "race", MaxDownloads: 1, DownloadCount: 0}

			var granted, busy, limited int64
			var leases []*DownloadLease
			var mu sync.Mutex
			var wg sync.WaitGroup
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					lease, err := svc.ReserveDownload(ctx, sh)
					switch {
					case err == nil:
						atomic.AddInt64(&granted, 1)
						mu.Lock()
						leases = append(leases, lease)
						mu.Unlock()
					case errors.Is(err, ErrShareDownloadsBusy):
						atomic.AddInt64(&busy, 1)
					case errors.Is(err, ErrShareDownloadLimit):
						atomic.AddInt64(&limited, 1)
					}
				}()
			}
			wg.Wait()

			Expect(granted).To(Equal(int64(1)), "exactly one reservation may succeed")
			Expect(granted + busy + limited).To(Equal(int64(50)))

			// Completing the one reserved delivery settles one download.
			leases[0].Commit()
			Expect(shareRepo.SettleCallCount).To(Equal(1))
			Expect(shareRepo.Entity.(*model.Share).DownloadCount).To(Equal(1))

			// Budget is now fully settled: new requests hit the permanent limit.
			_, err := svc.ReserveDownload(ctx, sh)
			Expect(err).To(MatchError(ErrShareDownloadLimit))
		})

		It("returns the slot to the pool when the delivery fails", func() {
			sh := &model.Share{ID: "retry", MaxDownloads: 1, DownloadCount: 0}
			lease, err := svc.ReserveDownload(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			// While reserved, a second request is busy (not a settled limit).
			_, err = svc.ReserveDownload(ctx, sh)
			Expect(err).To(MatchError(ErrShareDownloadsBusy))
			// Failed delivery: release, do not settle.
			lease.Release()
			Expect(shareRepo.SettleCallCount).To(BeZero())

			// The slot is available again.
			lease2, err := svc.ReserveDownload(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			lease2.Commit()
			Expect(shareRepo.SettleCallCount).To(Equal(1))
		})

		It("rejects when the settled budget is already exhausted", func() {
			sh := &model.Share{ID: "done", MaxDownloads: 2, DownloadCount: 2}
			_, err := svc.ReserveDownload(ctx, sh)
			Expect(err).To(MatchError(ErrShareDownloadLimit))
		})

		It("never settles twice even if both Commit and Release are called", func() {
			sh := &model.Share{ID: "dedupe", MaxDownloads: 5}
			lease, err := svc.ReserveDownload(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			lease.Commit()
			lease.Release()
			lease.Commit()
			Expect(shareRepo.SettleCallCount).To(Equal(1))
		})
	})

	Describe("Rotate", func() {
		BeforeEach(func() {
			shareRepo.ID = "rot"
			shareRepo.Entity = &model.Share{ID: "rot", Epoch: 0}
		})

		It("persists a new generation via the repository", func() {
			epoch, err := svc.Rotate(ctx, "rot")
			Expect(err).ToNot(HaveOccurred())
			Expect(epoch).To(Equal(1))
			Expect(shareRepo.RotateCalled).To(BeTrue())
			Expect(shareRepo.Entity.(*model.Share).Epoch).To(Equal(1))
		})

		It("cancels in-flight plays when rotating", func() {
			sh := &model.Share{ID: "rot", MaxConcurrentPlays: 5}
			playCtx, release, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			Expect(playCtx.Err()).ToNot(HaveOccurred())

			_, err = svc.Rotate(ctx, "rot")
			Expect(err).ToNot(HaveOccurred())
			Expect(playCtx.Err()).To(MatchError(context.Canceled))

			release()
			Expect(svc.ActivePlays("rot")).To(BeZero())
		})

		It("revokes in-flight download leases so they do not settle", func() {
			sh := &model.Share{ID: "rot", MaxDownloads: 5}
			lease, err := svc.ReserveDownload(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			Expect(lease.Revoked()).To(BeFalse())

			_, err = svc.Rotate(ctx, "rot")
			Expect(err).ToNot(HaveOccurred())
			Expect(lease.Revoked()).To(BeTrue())

			// Even a "successful" commit after revocation must not settle.
			lease.Commit()
			Expect(shareRepo.SettleCallCount).To(BeZero())
		})
	})

	Describe("wrapper usage enrichment and write whitelist", func() {
		It("fills ActivePlays on Get/GetAll", func() {
			sh := &model.Share{ID: "live", MaxConcurrentPlays: 5}
			shareRepo.ID = "live"
			shareRepo.Entity = sh
			_, release, err := svc.AcquirePlay(ctx, sh)
			Expect(err).ToNot(HaveOccurred())
			defer release()

			repo := svc.NewRepository(ctx).(model.ShareRepository)
			got, err := repo.Get("live")
			Expect(err).ToNot(HaveOccurred())
			Expect(got.ActivePlays).To(Equal(1))
		})

		It("whitelists budget columns but not epoch/download_count on Update", func() {
			persistable := svc.NewRepository(ctx).(rest.Persistable)
			err := persistable.Update("x", &model.Share{})
			Expect(err).ToNot(HaveOccurred())
			Expect(shareRepo.Cols).To(ContainElements(
				"max_concurrent_plays", "max_downloads", "description", "downloadable"))
			Expect(shareRepo.Cols).ToNot(ContainElement("epoch"))
			Expect(shareRepo.Cols).ToNot(ContainElement("download_count"))
		})
	})
})
