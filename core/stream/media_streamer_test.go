package stream_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing/iotest"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/core/stream"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("MediaStreamer", func() {
	var streamer stream.MediaStreamer
	var ds model.DataStore
	ffmpeg := tests.NewMockFFmpeg("fake data")
	ctx := log.NewContext(context.TODO())

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
		cacheDir, _ := os.MkdirTemp("", "file_caches")
		conf.Server.CacheFolder = conf.NewDir(cacheDir)
		conf.Server.TranscodingCacheSize = "100MB"
		ds = &tests.MockDataStore{MockedTranscoding: &tests.MockTranscodingRepo{}}
		ds.MediaFile().(*tests.MockMediaFileRepo).SetData(model.MediaFiles{
			{ID: "123", Path: "tests/fixtures/test.mp3", Suffix: "mp3", BitRate: 128, Duration: 257.0},
		})
		testCache := stream.NewTranscodingCache()
		Eventually(func() bool { return testCache.Available(context.TODO()) }, 10*time.Second).Should(BeTrue())
		streamer = stream.NewMediaStreamer(ds, ffmpeg, testCache)
	})
	AfterEach(func() {
		_ = os.RemoveAll(conf.Server.CacheFolder.String())
	})

	Context("NewStream", func() {
		var mf *model.MediaFile
		BeforeEach(func() {
			var err error
			mf, err = ds.MediaFile().Get(ctx, "123")
			Expect(err).ToNot(HaveOccurred())
		})
		It("returns a seekable stream if format is 'raw'", func() {
			s, err := streamer.NewStream(ctx, mf, stream.Request{Format: "raw"})
			Expect(err).ToNot(HaveOccurred())
			Expect(s.Seekable()).To(BeTrue())
		})
		It("returns a seekable stream if no format is specified (direct play)", func() {
			s, err := streamer.NewStream(ctx, mf, stream.Request{})
			Expect(err).ToNot(HaveOccurred())
			Expect(s.Seekable()).To(BeTrue())
		})
		It("returns a NON seekable stream if transcode is required", func() {
			s, err := streamer.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 64})
			Expect(err).To(BeNil())
			Expect(s.Seekable()).To(BeFalse())
			Expect(s.Duration()).To(Equal(float32(257.0)))
		})
		It("rejects transcode requests beyond MaxConcurrent with ErrTooManyTranscodes", func() {
			// Use an ffmpeg whose Read blocks indefinitely so the cache's
			// background copy can't drain the source and release the slot —
			// keeping the single transcode slot pinned for this test.
			pr, pw := io.Pipe()
			DeferCleanup(func() { _ = pw.Close() })
			blockingFFmpeg := tests.NewMockFFmpeg("")
			blockingFFmpeg.Reader = pr

			conf.Server.Transcoding.MaxConcurrent = 1
			conf.Server.Transcoding.MaxConcurrentPerUser = 0
			tightCache := stream.NewTranscodingCache()
			Eventually(func() bool { return tightCache.Available(context.TODO()) }, 10*time.Second).Should(BeTrue())
			tightStreamer := stream.NewMediaStreamer(ds, blockingFFmpeg, tightCache)

			userCtx := request.WithUsername(ctx, "alice")
			s1, err := tightStreamer.NewStream(userCtx, mf, stream.Request{Format: "mp3", BitRate: 64})
			Expect(err).ToNot(HaveOccurred())
			defer s1.Close()

			// Different cache key so it doesn't dedupe with the first request.
			_, err = tightStreamer.NewStream(userCtx, mf, stream.Request{Format: "mp3", BitRate: 96})
			Expect(errors.Is(err, stream.ErrTooManyTranscodes)).To(BeTrue())
		})

		It("releases the slot once the stream is closed", func() {
			conf.Server.Transcoding.MaxConcurrent = 1
			conf.Server.Transcoding.MaxConcurrentPerUser = 0
			tightCache := stream.NewTranscodingCache()
			Eventually(func() bool { return tightCache.Available(context.TODO()) }, 10*time.Second).Should(BeTrue())
			tightStreamer := stream.NewMediaStreamer(ds, ffmpeg, tightCache)

			userCtx := request.WithUsername(ctx, "alice")
			s1, err := tightStreamer.NewStream(userCtx, mf, stream.Request{Format: "mp3", BitRate: 64})
			Expect(err).ToNot(HaveOccurred())
			_, _ = io.ReadAll(s1)
			_ = s1.Close()
			Eventually(func() bool { return ffmpeg.IsClosed() }, "3s").Should(BeTrue())

			// Slot should now be free for a different transcode.
			s2, err := tightStreamer.NewStream(userCtx, mf, stream.Request{Format: "mp3", BitRate: 96})
			Expect(err).ToNot(HaveOccurred())
			defer s2.Close()
		})

		It("does not consume a slot for raw streams", func() {
			conf.Server.Transcoding.MaxConcurrent = 1
			conf.Server.Transcoding.MaxConcurrentPerUser = 0
			tightCache := stream.NewTranscodingCache()
			Eventually(func() bool { return tightCache.Available(context.TODO()) }, 10*time.Second).Should(BeTrue())
			tightStreamer := stream.NewMediaStreamer(ds, ffmpeg, tightCache)

			userCtx := request.WithUsername(ctx, "alice")
			// First, saturate the single transcode slot.
			s1, err := tightStreamer.NewStream(userCtx, mf, stream.Request{Format: "mp3", BitRate: 64})
			Expect(err).ToNot(HaveOccurred())
			defer s1.Close()

			// Raw stream must still succeed.
			s2, err := tightStreamer.NewStream(userCtx, mf, stream.Request{Format: "raw"})
			Expect(err).ToNot(HaveOccurred())
			defer s2.Close()
		})

		It("returns a seekable stream if the file is complete in the cache", func() {
			s, err := streamer.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 32})
			Expect(err).To(BeNil())
			_, _ = io.ReadAll(s)
			_ = s.Close()
			Eventually(func() bool { return ffmpeg.IsClosed() }, "3s").Should(BeTrue())

			s, err = streamer.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 32})
			Expect(err).To(BeNil())
			Expect(s.Seekable()).To(BeTrue())
		})

		Describe("configuration generations", func() {
			// Builds an isolated stack (own cache, own ffmpeg) for a custom mp3
			// command so generations are fully under test control.
			newStack := func(command string, ff *tests.MockFFmpeg) (stream.MediaStreamer, stream.TranscodingCache, *tests.MockTranscodingRepo, model.DataStore) {
				trcRepo := &tests.MockTranscodingRepo{Data: map[string]*model.Transcoding{
					"mp3": {ID: "mp3-x", Name: "custom mp3", TargetFormat: "mp3", DefaultBitRate: 192, Command: command},
				}}
				localDS := &tests.MockDataStore{MockedTranscoding: trcRepo}
				localDS.MediaFile().(*tests.MockMediaFileRepo).SetData(model.MediaFiles{
					{ID: "123", Path: "tests/fixtures/test.mp3", Suffix: "mp3", BitRate: 128, Duration: 257.0},
				})
				c := stream.NewTranscodingCache()
				Eventually(func() bool { return c.Available(context.TODO()) }, 10*time.Second).Should(BeTrue())
				return stream.NewMediaStreamer(localDS, ff, c), c, trcRepo, localDS
			}

			It("never serves the old generation's cache after the command changes", func() {
				s1, sharedCache, repo, ds1 := newStack("ffmpeg -command v1", tests.NewMockFFmpeg("gen1-data"))
				gen1, ok := stream.EffectiveGeneration(ctx, ds1, "mp3")
				Expect(ok).To(BeTrue())

				// First generation: transcode and fully populate the cache.
				r1, err := s1.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 32, Generation: gen1})
				Expect(err).ToNot(HaveOccurred())
				Expect(r1.Seekable()).To(BeFalse()) // cache MISS
				data1, err := io.ReadAll(r1)
				Expect(err).ToNot(HaveOccurred())
				Expect(string(data1)).To(Equal("gen1-data"))
				_ = r1.Close()

				// Admin commits a new command.
				repo.Data["mp3"].Command = "ffmpeg -command v2"
				gen2, ok := stream.EffectiveGeneration(ctx, ds1, "mp3")
				Expect(ok).To(BeTrue())
				Expect(gen2).NotTo(Equal(gen1))

				// A second streamer, same on-disk cache, new ffmpeg output.
				s2 := stream.NewMediaStreamer(ds1, tests.NewMockFFmpeg("gen2-data"), sharedCache)

				// The new generation must be a MISS, never the old complete entry.
				r2, err := s2.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 32, Generation: gen2})
				Expect(err).ToNot(HaveOccurred())
				Expect(r2.Seekable()).To(BeFalse(), "changed generation must not hit the pre-change cache")
				data2, err := io.ReadAll(r2)
				Expect(err).ToNot(HaveOccurred())
				Expect(string(data2)).To(Equal("gen2-data"), "new generation output must never come from the old cache")
				_ = r2.Close()

				// Once complete, the new generation hits its own namespace.
				Eventually(func() bool {
					r3, err := s2.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 32, Generation: gen2})
					if err != nil {
						return false
					}
					defer r3.Close()
					return r3.Seekable()
				}, "3s").Should(BeTrue())

				// A request still pinned to the old committed snapshot is
				// refused instead of launching v2 under the v1 generation.
				_, err = s2.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 32, Generation: gen1})
				Expect(err).To(MatchError(stream.ErrTranscodeConfigChanged))
			})

			It("keeps cache hits stable for an unchanged configuration", func() {
				s, _, _, localDS := newStack("ffmpeg -stable", tests.NewMockFFmpeg("stable-data"))
				gen, ok := stream.EffectiveGeneration(ctx, localDS, "mp3")
				Expect(ok).To(BeTrue())

				r1, err := s.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 64, Generation: gen})
				Expect(err).ToNot(HaveOccurred())
				_, _ = io.ReadAll(r1)
				_ = r1.Close()

				r2, err := s.NewStream(ctx, mf, stream.Request{Format: "mp3", BitRate: 64, Generation: gen})
				Expect(err).ToNot(HaveOccurred())
				Expect(r2.Seekable()).To(BeTrue(), "unchanged generation must keep hitting its cache")
				_ = r2.Close()
			})
		})
	})

	Context("Serve", func() {
		var mf *model.MediaFile
		BeforeEach(func() {
			var err error
			mf, err = ds.MediaFile().Get(ctx, "123")
			Expect(err).ToNot(HaveOccurred())
		})

		It("keeps empty output a non-error, so callers still reply 200 with an empty body", func() {
			s := stream.NewStream(mf, "mp3", 128, io.NopCloser(bytes.NewReader(nil)))
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/", nil)

			n, err := s.Serve(ctx, w, r)

			Expect(err).ToNot(HaveOccurred())
			Expect(n).To(BeZero())
			Expect(w.Code).To(Equal(http.StatusOK))
		})

		It("aborts the response when the source fails after sending data", func() {
			src := io.NopCloser(io.MultiReader(
				bytes.NewReader(bytes.Repeat([]byte("a"), 64*1024)),
				iotest.ErrReader(errors.New("transcoder died")),
			))
			server := httptest.NewServer(serveHandler(stream.NewStream(mf, "mp3", 128, src)))
			DeferCleanup(server.Close)

			resp, err := http.Get(server.URL)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()

			// A client-side read failure is the only observable proof the response was aborted.
			_, err = io.ReadAll(resp.Body)
			Expect(err).To(HaveOccurred())
		})
	})
})

// Serve runs behind the real server's Recoverer, which must let ErrAbortHandler through.
func serveHandler(s *stream.Stream) http.Handler {
	return middleware.Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = s.Serve(r.Context(), w, r)
	}))
}
