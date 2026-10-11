package stream

import (
	"context"

	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Transcoding generations", func() {
	var (
		ds  *tests.MockDataStore
		ctx context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		ds = &tests.MockDataStore{MockedTranscoding: &tests.MockTranscodingRepo{}}
	})

	Describe("TranscodingGeneration", func() {
		It("is stable for the same configuration", func() {
			t := model.Transcoding{TargetFormat: "mp3", Command: "ffmpeg -f mp3", DefaultBitRate: 192}
			Expect(TranscodingGeneration(t)).To(Equal(TranscodingGeneration(t)))
			Expect(TranscodingGeneration(t)).To(HavePrefix(generationPrefix))
		})

		It("changes when the command changes", func() {
			old := model.Transcoding{TargetFormat: "opus", Command: "ffmpeg -c:a libopus -b:a 96k", DefaultBitRate: 96}
			new := old
			new.Command = "ffmpeg -c:a libopus -b:a 128k -vbr on"
			Expect(TranscodingGeneration(new)).NotTo(Equal(TranscodingGeneration(old)))
		})

		It("changes when the target format or default bitrate changes", func() {
			base := model.Transcoding{TargetFormat: "mp3", Command: "ffmpeg", DefaultBitRate: 192}
			format := base
			format.TargetFormat = "opus"
			bitrate := base
			bitrate.DefaultBitRate = 320
			Expect(TranscodingGeneration(format)).NotTo(Equal(TranscodingGeneration(base)))
			Expect(TranscodingGeneration(bitrate)).NotTo(Equal(TranscodingGeneration(base)))
		})

		It("does not depend on the row id or display name", func() {
			a := model.Transcoding{ID: "id-1", Name: "first", TargetFormat: "mp3", Command: "ffmpeg", DefaultBitRate: 192}
			b := model.Transcoding{ID: "id-2", Name: "renamed", TargetFormat: "mp3", Command: "ffmpeg", DefaultBitRate: 192}
			// A rename keeps the generation (and therefore all cache hits).
			Expect(TranscodingGeneration(b)).To(Equal(TranscodingGeneration(a)))
		})
	})

	Describe("EffectiveTranscoding", func() {
		It("prefers a committed DB row with a command", func() {
			ds.MockedTranscoding = &tests.MockTranscodingRepo{Data: map[string]*model.Transcoding{
				"mp3": {ID: "custom", TargetFormat: "mp3", Command: "custom-command", DefaultBitRate: 320},
			}}
			t, ok := EffectiveTranscoding(ctx, ds, "mp3")
			Expect(ok).To(BeTrue())
			Expect(t.ID).To(Equal("custom"))
			Expect(t.Command).To(Equal("custom-command"))
		})

		It("falls back to the built-in command when the DB row has none", func() {
			ds.MockedTranscoding = &tests.MockTranscodingRepo{Data: map[string]*model.Transcoding{
				"mp3": {ID: "row", TargetFormat: "mp3", DefaultBitRate: 160},
			}}
			t, ok := EffectiveTranscoding(ctx, ds, "mp3")
			Expect(ok).To(BeTrue())
			var builtin string
			for _, d := range consts.DefaultTranscodings {
				if d.TargetFormat == "mp3" {
					builtin = d.Command
				}
			}
			Expect(t.Command).To(Equal(builtin))
			Expect(t.DefaultBitRate).To(Equal(160))
		})

		It("uses the built-in default when no row exists", func() {
			t, ok := EffectiveTranscoding(ctx, ds, "mp3")
			Expect(ok).To(BeTrue())
			Expect(t.Command).ToNot(BeEmpty())
		})

		It("reports false for an unknown format", func() {
			_, ok := EffectiveTranscoding(ctx, ds, "wma-pro")
			Expect(ok).To(BeFalse())
		})

		It("resolves identically on repeated calls (stable committed snapshot)", func() {
			g1, ok1 := EffectiveGeneration(ctx, ds, "mp3")
			g2, ok2 := EffectiveGeneration(ctx, ds, "mp3")
			Expect(ok1).To(BeTrue())
			Expect(ok2).To(BeTrue())
			Expect(g1).To(Equal(g2))
		})
	})

	Describe("CurrentTranscodingGenerations", func() {
		It("includes built-in default generations", func() {
			gens, err := CurrentTranscodingGenerations(ctx, ds)
			Expect(err).ToNot(HaveOccurred())
			for _, d := range consts.DefaultTranscodings {
				Expect(gens).To(HaveKey(TranscodingGeneration(model.Transcoding{
					Name: d.Name, TargetFormat: d.TargetFormat, Command: d.Command, DefaultBitRate: d.DefaultBitRate,
				})))
			}
		})

		It("includes effective generations of committed rows", func() {
			custom := model.Transcoding{ID: "c1", Name: "custom mp3", TargetFormat: "mp3", Command: "custom-command", DefaultBitRate: 320}
			ds.MockedTranscoding = &tests.MockTranscodingRepo{Data: map[string]*model.Transcoding{"mp3": &custom}}
			gens, err := CurrentTranscodingGenerations(ctx, ds)
			Expect(err).ToNot(HaveOccurred())
			Expect(gens).To(HaveKey(TranscodingGeneration(custom)))
		})

		It("applies the built-in command fallback to rows before hashing", func() {
			ds.MockedTranscoding = &tests.MockTranscodingRepo{Data: map[string]*model.Transcoding{
				"mp3": {ID: "row", TargetFormat: "mp3", DefaultBitRate: 160},
			}}
			gens, err := CurrentTranscodingGenerations(ctx, ds)
			Expect(err).ToNot(HaveOccurred())
			effective, ok := EffectiveTranscoding(ctx, ds, "mp3")
			Expect(ok).To(BeTrue())
			Expect(gens).To(HaveKey(TranscodingGeneration(effective)))
		})
	})
})
