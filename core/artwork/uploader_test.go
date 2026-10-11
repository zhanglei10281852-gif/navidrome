package artwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// refStore is the in-memory entity behind a CoverCommit in the tests.
type refStore struct {
	mu         sync.Mutex
	current    string
	persistErr error
	persisted  []string
}

func (r *refStore) hooks() CoverCommit {
	return CoverCommit{
		Current: func(context.Context) (string, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.current, nil
		},
		Persist: func(_ context.Context, filename string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.persisted = append(r.persisted, filename)
			if r.persistErr != nil {
				return r.persistErr
			}
			r.current = filename
			return nil
		},
	}
}

// failingReader returns some bytes and then an error, simulating a stream that dies mid-upload.
type failingReader struct {
	remaining int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, errors.New("connection reset by peer")
	}
	n := min(len(p), f.remaining)
	for i := range n {
		p[i] = 'x'
	}
	f.remaining -= n
	return n, nil
}

var _ = Describe("Uploader", func() {
	var ctx context.Context
	var svc Uploader
	var tmpDir string
	var artRepo *tests.MockArtworkRepo
	var queueRepo *tests.MockArtworkQueueRepo
	var ds *tests.MockDataStore

	BeforeEach(func() {
		ctx = GinkgoT().Context()
		DeferCleanup(configtest.SetupConfig())
		tmpDir = GinkgoT().TempDir()
		conf.Server.DataFolder = conf.NewDir(tmpDir)
		artRepo = tests.CreateMockArtworkRepo()
		queueRepo = tests.CreateMockArtworkQueueRepo()
		ds = &tests.MockDataStore{MockedArtwork: artRepo, MockedArtworkQueue: queueRepo}
		svc = NewUploader(ds)
	})

	artistFile := func(filename string) string {
		return filepath.Join(tmpDir, "artwork", "artist", filename)
	}

	expectBumpQueued := func(entityID string) {
		queued, err := queueRepo.DequeueBatch(ctx, 1000)
		Expect(err).ToNot(HaveOccurred())
		Expect(queued).To(ContainElement(SatisfyAll(
			HaveField("ItemKind", model.KindArtistArtwork.Prefix()),
			HaveField("ItemID", entityID),
			HaveField("Priority", model.ArtworkPriorityBump),
		)))
	}

	writeOld := func(filename, bytes string) string {
		path := artistFile(filename)
		Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(bytes), 0o600)).To(Succeed())
		return path
	}

	Describe("SetImage", func() {
		It("creates directory and saves image file on first upload", func() {
			store := &refStore{}
			err := svc.SetImage(ctx, consts.EntityArtist, "ar-1", "Pink Floyd", strings.NewReader("fake image data"), ".jpg", store.hooks())
			Expect(err).ToNot(HaveOccurred())
			Expect(store.current).To(Equal("ar-1_pink_floyd.jpg"))

			data, err := os.ReadFile(artistFile("ar-1_pink_floyd.jpg"))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("fake image data"))
			expectBumpQueued("ar-1")
		})

		It("falls back to ID-only filename when name cleans to empty", func() {
			store := &refStore{}
			err := svc.SetImage(ctx, consts.EntityPlaylist, "pl-1", "!!!", strings.NewReader("data"), ".png", store.hooks())
			Expect(err).ToNot(HaveOccurred())
			Expect(store.current).To(Equal("pl-1.png"))
			Expect(filepath.Join(tmpDir, "artwork", "playlist", "pl-1.png")).To(BeAnExistingFile())
		})

		It("removes the old image after a replacement commit", func() {
			oldFile := writeOld("ar-1_old.png", "old")
			store := &refStore{current: "ar-1_old.png"}

			Expect(svc.SetImage(ctx, consts.EntityArtist, "ar-1", "New Name", strings.NewReader("new image"), ".jpg", store.hooks())).
				To(Succeed())

			Expect(oldFile).ToNot(BeAnExistingFile())
			newPath := artistFile("ar-1_new_name.jpg")
			Expect(newPath).To(BeAnExistingFile())
			data, err := os.ReadFile(newPath)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("new image"))
			Expect(store.current).To(Equal("ar-1_new_name.jpg"))
		})

		It("keeps the old cover and leaves no candidate when the upload stream fails", func() {
			oldFile := writeOld("ar-1_old.png", "old-bytes")
			store := &refStore{current: "ar-1_old.png"}

			err := svc.SetImage(ctx, consts.EntityArtist, "ar-1", "New Name", &failingReader{remaining: 100}, ".jpg", store.hooks())
			Expect(err).To(MatchError(ContainSubstring("writing image file")))

			// Old cover is still fully readable.
			data, err := os.ReadFile(oldFile)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("old-bytes"))
			// Reference untouched, no state refresh, no half-written final or candidate file.
			Expect(store.current).To(Equal("ar-1_old.png"))
			Expect(store.persisted).To(BeEmpty())
			Expect(artistFile("ar-1_new_name.jpg")).ToNot(BeAnExistingFile())
			entries, err := os.ReadDir(filepath.Dir(oldFile))
			Expect(err).ToNot(HaveOccurred())
			for _, e := range entries {
				Expect(e.Name()).ToNot(HavePrefix("."))
			}
			Expect(queueRepo.DequeueBatch(ctx, 1000)).To(BeEmpty())
		})

		It("rolls the new file back when persisting the reference fails (different name)", func() {
			oldFile := writeOld("ar-1_old.png", "old-bytes")
			store := &refStore{current: "ar-1_old.png", persistErr: errors.New("db is down")}

			err := svc.SetImage(ctx, consts.EntityArtist, "ar-1", "New Name", strings.NewReader("new image"), ".jpg", store.hooks())
			Expect(err).To(MatchError(ContainSubstring("db is down")))

			Expect(artistFile("ar-1_new_name.jpg")).ToNot(BeAnExistingFile(), "unpersisted publish must be rolled back")
			data, err := os.ReadFile(oldFile)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("old-bytes"))
			Expect(queueRepo.DequeueBatch(ctx, 1000)).To(BeEmpty(), "no refresh on a failed commit")
		})

		It("keeps the old bytes coherent when an in-place replacement fails to persist", func() {
			final := writeOld("ar-1_name.jpg", "old-bytes")
			store := &refStore{current: "ar-1_name.jpg", persistErr: errors.New("db is down")}

			err := svc.SetImage(ctx, consts.EntityArtist, "ar-1", "Name", strings.NewReader("new image"), ".jpg", store.hooks())
			Expect(err).To(HaveOccurred())

			data, err := os.ReadFile(final)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("old-bytes"), "same-path reference must keep serving the old complete image")
			entries, err := os.ReadDir(filepath.Dir(final))
			Expect(err).ToNot(HaveOccurred())
			for _, e := range entries {
				Expect(e.Name()).ToNot(HavePrefix("."))
			}
		})

		It("does not delete a later successful commit's file when rolling back", func() {
			// First read sees the old name; the re-check during rollback sees the new name, as if
			// a concurrent retried submission committed in between.
			oldFile := writeOld("ar-1_old.png", "old")
			reads := 0
			commit := CoverCommit{
				Current: func(context.Context) (string, error) {
					reads++
					if reads == 1 {
						return "ar-1_old.png", nil
					}
					return "ar-1_new_name.jpg", nil
				},
				Persist: func(context.Context, string) error { return errors.New("db is down") },
			}

			Expect(svc.SetImage(ctx, consts.EntityArtist, "ar-1", "New Name", strings.NewReader("new image"), ".jpg", commit)).
				To(HaveOccurred())

			Expect(oldFile).To(BeAnExistingFile())
			Expect(artistFile("ar-1_new_name.jpg")).To(BeAnExistingFile(), "a reference now names this file")
		})

		It("atomically replaces the same path and refreshes state on an in-place success", func() {
			final := writeOld("ar-1_name.jpg", "old-bytes")
			Expect(artRepo.PutItemArtwork(ctx, &model.ItemArtwork{
				ItemKind: model.KindArtistArtwork.Prefix(), ItemID: "ar-1",
				Hash: "oldhash", Source: "upload", SourcePath: final,
			})).To(Succeed())
			store := &refStore{current: "ar-1_name.jpg"}

			Expect(svc.SetImage(ctx, consts.EntityArtist, "ar-1", "Name", strings.NewReader("brand-new-bytes"), ".jpg", store.hooks())).
				To(Succeed())

			data, err := os.ReadFile(final)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(data)).To(Equal("brand-new-bytes"))
			_, err = artRepo.GetItemArtwork(ctx, model.KindArtistArtwork, "ar-1", model.ImageTypePrimary)
			Expect(err).To(MatchError(model.ErrNotFound))
			expectBumpQueued("ar-1")
		})

		It("serializes concurrent replacements: the final file is always one complete image", func() {
			writeOld("ar-1_name.jpg", strings.Repeat("o", 4096))
			payloads := map[string]bool{}
			for _, b := range []string{"A", "B", "C", "D"} {
				payloads[strings.Repeat(b, 8192)] = true
			}

			var wg sync.WaitGroup
			errs := make(chan error, 4)
			for b := range payloads {
				wg.Add(1)
				go func(payload string) {
					defer wg.Done()
					store := &refStore{}
					commit := store.hooks()
					// Every goroutine replaces the same reference; the lock orders them.
					commit.Current = func(context.Context) (string, error) { return "ar-1_name.jpg", nil }
					commit.Persist = func(_ context.Context, filename string) error {
						store.current = filename
						return nil
					}
					errs <- svc.SetImage(ctx, consts.EntityArtist, "ar-1", "Name", strings.NewReader(payload), ".jpg", commit)
				}(b)
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				Expect(err).ToNot(HaveOccurred())
			}

			final := artistFile("ar-1_name.jpg")
			data, err := os.ReadFile(final)
			Expect(err).ToNot(HaveOccurred())
			Expect(payloads[string(data)]).To(BeTrue(), "no mixed or partial bytes from interleaved writes")
			entries, err := os.ReadDir(filepath.Dir(final))
			Expect(err).ToNot(HaveOccurred())
			for _, e := range entries {
				Expect(e.Name()).ToNot(HavePrefix("."))
			}
		})
	})

	Describe("RemoveImage", func() {
		It("clears the reference while the file still exists, then removes the file", func() {
			path := writeOld("ar-1_test.jpg", "img")
			fileThereDuringPersist := false
			store := &refStore{current: "ar-1_test.jpg"}
			commit := store.hooks()
			commit.Persist = func(ctx context.Context, filename string) error {
				if _, err := os.Stat(path); err == nil {
					fileThereDuringPersist = true
				}
				return store.hooks().Persist(ctx, filename)
			}

			Expect(svc.RemoveImage(ctx, consts.EntityArtist, "ar-1", commit)).To(Succeed())
			Expect(fileThereDuringPersist).To(BeTrue(), "DB clear must happen before unlink")
			Expect(path).ToNot(BeAnExistingFile())
			Expect(store.current).To(BeEmpty())
			_, err := artRepo.GetItemArtwork(ctx, model.KindArtistArtwork, "ar-1", model.ImageTypePrimary)
			Expect(err).To(MatchError(model.ErrNotFound))
			expectBumpQueued("ar-1")
		})

		It("keeps the file when the reference cannot be persisted", func() {
			path := writeOld("ar-1_test.jpg", "img")
			store := &refStore{current: "ar-1_test.jpg", persistErr: errors.New("db is down")}

			err := svc.RemoveImage(ctx, consts.EntityArtist, "ar-1", store.hooks())
			Expect(err).To(MatchError(ContainSubstring("db is down")))
			Expect(path).To(BeAnExistingFile())
			Expect(store.current).To(Equal("ar-1_test.jpg"))
		})

		It("succeeds with no current image and still refreshes state", func() {
			Expect(artRepo.PutItemArtwork(ctx, &model.ItemArtwork{
				ItemKind: model.KindArtistArtwork.Prefix(), ItemID: "ar-1", Hash: "oldhash",
			})).To(Succeed())
			Expect(svc.RemoveImage(ctx, consts.EntityArtist, "ar-1", (&refStore{}).hooks())).To(Succeed())
			_, err := artRepo.GetItemArtwork(ctx, model.KindArtistArtwork, "ar-1", model.ImageTypePrimary)
			Expect(err).To(MatchError(model.ErrNotFound))
		})

		It("keeps the file when artwork state cannot be cleared, but still queues convergence", func() {
			path := writeOld("ar-1_test.jpg", "img")
			artRepo.Err = errors.New("artwork table is gone")
			defer func() { artRepo.Err = nil }()

			Expect(svc.RemoveImage(ctx, consts.EntityArtist, "ar-1", (&refStore{current: "ar-1_test.jpg"}).hooks())).
				To(Succeed())
			Expect(path).To(BeAnExistingFile(), "stale state still names this path")
			queued, err := queueRepo.DequeueBatch(ctx, 1000)
			Expect(err).ToNot(HaveOccurred())
			Expect(queued).To(ContainElement(HaveField("ItemID", "ar-1")))
		})
	})

	Describe("EnqueueArtwork", func() {
		It("clears artwork state and enqueues a Bump", func() {
			Expect(artRepo.PutItemArtwork(ctx, &model.ItemArtwork{
				ItemKind: "ar", ItemID: "ar-1", Hash: "oldhash", Source: "external",
			})).To(Succeed())

			svc.EnqueueArtwork(ctx, consts.EntityArtist, "ar-1")

			_, err := artRepo.GetItemArtwork(ctx, model.KindArtistArtwork, "ar-1", model.ImageTypePrimary)
			Expect(err).To(MatchError(model.ErrNotFound))
			expectBumpQueued("ar-1")
		})

		It("is a no-op for an unknown entity type", func() {
			svc.EnqueueArtwork(context.Background(), "unknown", "x-1")
			Expect(queueRepo.DequeueBatch(ctx, 1000)).To(BeEmpty())
		})
	})

	Describe("sweepUploadedImages", func() {
		It("reclaims stale candidates and unreferenced files, keeps referenced and recent ones", func() {
			referenced := writeOld("ar-1_kept.jpg", "keep")
			unreferenced := writeOld("ar-9_gone.jpg", "gone")
			staleCandidate := artistFile(".ar-8_old.jpg.tmp123456")
			recentCandidate := artistFile(".ar-7_fresh.jpg.tmp654321")
			recentUnreferenced := writeOld("ar-6_recent.jpg", "recent")
			Expect(os.WriteFile(staleCandidate, []byte("partial"), 0o600)).To(Succeed())
			Expect(os.WriteFile(recentCandidate, []byte("partial"), 0o600)).To(Succeed())

			old := time.Now().Add(-2 * time.Hour)
			Expect(os.Chtimes(unreferenced, old, old)).To(Succeed())
			Expect(os.Chtimes(staleCandidate, old, old)).To(Succeed())

			artistRepo := tests.CreateMockArtistRepo()
			artistRepo.SetData(model.Artists{{ID: "ar-1", Name: "Kept", UploadedImage: "ar-1_kept.jpg"}})
			ds.MockedArtist = artistRepo

			removed, err := sweepUploadedImages(ctx, ds, time.Now().Add(-time.Hour))
			Expect(err).ToNot(HaveOccurred())
			Expect(removed).To(Equal(2))

			Expect(referenced).To(BeAnExistingFile())
			Expect(unreferenced).ToNot(BeAnExistingFile())
			Expect(staleCandidate).ToNot(BeAnExistingFile())
			Expect(recentCandidate).To(BeAnExistingFile(), "an in-flight candidate is inside the grace window")
			Expect(recentUnreferenced).To(BeAnExistingFile())
		})
	})
})

var _ = Describe("MaxImageUploadSize", func() {
	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
	})

	It("returns the configured size when valid", func() {
		conf.Server.MaxImageUploadSize = "20MB"
		Expect(MaxImageUploadSize()).To(Equal(int64(20_000_000)))
	})

	It("returns the default size when config is empty", func() {
		conf.Server.MaxImageUploadSize = ""
		Expect(MaxImageUploadSize()).To(Equal(int64(10_000_000)))
	})

	It("returns the default size when config is invalid", func() {
		conf.Server.MaxImageUploadSize = "not-a-size"
		Expect(MaxImageUploadSize()).To(Equal(int64(10_000_000)))
	})

	It("parses raw byte values", func() {
		conf.Server.MaxImageUploadSize = "52428800"
		Expect(MaxImageUploadSize()).To(Equal(int64(52_428_800)))
	})
})
