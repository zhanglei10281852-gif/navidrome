package stream

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"

	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/model"
)

// generationPrefix namespaces generation hashes from any other cache payload.
const generationPrefix = "g"

// TranscodingGeneration returns the content hash of one committed transcoding
// configuration. It identifies the configuration's "generation": any change to
// the command, target format or default bitrate produces a different
// generation, which selects a different on-disk cache namespace and a different
// decision snapshot.
//
// The hash is content-addressed on purpose: the generation switches exactly
// when the committed DB row changes (or the built-in fallback changes in a new
// release), while an unchanged profile keeps a stable generation and therefore
// stable cache hits. A failed DB commit never publishes a new generation, so
// players keep being served from the old configuration and old cache until the
// new snapshot is durably committed.
func TranscodingGeneration(t model.Transcoding) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d", t.TargetFormat, t.Command, t.DefaultBitRate)
	return generationPrefix + hex.EncodeToString(h.Sum(nil))
}

// defaultTranscodingFor returns the built-in transcoding definition for format.
func defaultTranscodingFor(format string) (model.Transcoding, bool) {
	for _, d := range consts.DefaultTranscodings {
		if d.TargetFormat == format {
			return model.Transcoding{
				Name:           d.Name,
				TargetFormat:   d.TargetFormat,
				Command:        d.Command,
				DefaultBitRate: d.DefaultBitRate,
			}, true
		}
	}
	return model.Transcoding{}, false
}

// EffectiveTranscoding resolves the committed transcoding snapshot for format:
// the DB row when it defines a command, otherwise the built-in default for the
// format. ok is false when no committed configuration can produce format.
//
// Every playback step (profile selection, the token snapshot and the ffmpeg
// command launch) resolves through this function, so a playback always runs the
// snapshot of one committed configuration.
func EffectiveTranscoding(ctx context.Context, ds model.DataStore, format string) (model.Transcoding, bool) {
	snapshot := model.Transcoding{TargetFormat: format}
	if ds != nil {
		if t, err := ds.Transcoding().FindByFormat(ctx, format); err == nil && t != nil {
			snapshot = *t
		}
	}

	if snapshot.Command == "" {
		d, ok := defaultTranscodingFor(format)
		if !ok {
			return model.Transcoding{}, false
		}
		snapshot.TargetFormat = d.TargetFormat
		snapshot.Command = d.Command
		if snapshot.DefaultBitRate == 0 {
			snapshot.DefaultBitRate = d.DefaultBitRate
		}
	}
	return snapshot, true
}

// EffectiveGeneration resolves the committed snapshot for format and returns
// its generation. ok is false when the format has no transcoding command.
func EffectiveGeneration(ctx context.Context, ds model.DataStore, format string) (string, bool) {
	t, ok := EffectiveTranscoding(ctx, ds, format)
	if !ok {
		return "", false
	}
	return TranscodingGeneration(t), true
}

// CurrentTranscodingGenerations returns the generations of every snapshot a
// new playback can resolve to right now: committed DB rows (with the built-in
// command fallback applied) and built-in defaults. The transcoding cache uses
// it on startup to adopt only complete entries produced by a still-committed
// generation.
func CurrentTranscodingGenerations(ctx context.Context, ds model.DataStore) (map[string]struct{}, error) {
	gens := map[string]struct{}{}

	rows, err := ds.Transcoding().All(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		t := rows[i]
		if t.Command == "" {
			d, ok := defaultTranscodingFor(t.TargetFormat)
			if !ok {
				// No effective command for this row: no playback can resolve
				// to it, so it has no current generation.
				continue
			}
			t.Command = d.Command
			if t.DefaultBitRate == 0 {
				t.DefaultBitRate = d.DefaultBitRate
			}
		}
		gens[TranscodingGeneration(t)] = struct{}{}
	}

	for _, d := range consts.DefaultTranscodings {
		gens[TranscodingGeneration(model.Transcoding{
			Name:           d.Name,
			TargetFormat:   d.TargetFormat,
			Command:        d.Command,
			DefaultBitRate: d.DefaultBitRate,
		})] = struct{}{}
	}

	return gens, nil
}
