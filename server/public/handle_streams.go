package public

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/navidrome/navidrome/core"
	"github.com/navidrome/navidrome/core/auth"
	streampkg "github.com/navidrome/navidrome/core/stream"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	. "github.com/navidrome/navidrome/utils/gg"
	"github.com/navidrome/navidrome/utils/req"
)

func (pub *Router) handleStream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := req.Params(r)
	tokenId, _ := p.String(":id")
	info, err := decodeStreamInfo(tokenId)
	if err != nil {
		log.Error(ctx, "Error parsing shared stream info", err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	share, err := pub.ds.Share(ctx).Get(info.shareID)
	if err != nil {
		checkShareError(ctx, w, err, info.shareID)
		return
	}
	if expiresAt := V(share.ExpiresAt); !expiresAt.IsZero() && expiresAt.Before(time.Now()) {
		checkShareError(ctx, w, model.ErrExpired, info.shareID)
		return
	}
	// The link must belong to the share's current access generation. Tokens
	// minted before a rotation carry a stale generation and are rejected even
	// though their signature is valid.
	if info.epoch != share.Epoch {
		log.Warn(ctx, "Rejecting shared stream from a previous generation",
			"share", info.shareID, "tokenEpoch", info.epoch, "currentEpoch", share.Epoch)
		http.Error(w, "Share not available anymore", http.StatusGone)
		return
	}
	shareOwner, err := pub.ds.User(ctx).Get(share.UserID)
	if err != nil {
		log.Error(ctx, "Error retrieving share owner for shared stream", "share", info.shareID, "owner", share.UserID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	mf, err := pub.ds.MediaFile(ctx).Get(info.id)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			log.Error(ctx, "Error retrieving media file for shared stream", "id", info.id, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	// 404 rather than 403 so the response doesn't reveal whether the id exists.
	// The track must belong to the share AND be within the owner's libraries.
	if !shareContainsTrack(share, mf.ID) || !shareOwner.HasLibraryAccess(mf.LibraryID) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Reserve a concurrent-play slot for this share. This covers raw and
	// transcoded streams alike; the slot is released on every exit path
	// (normal end, client disconnect, startup failure) and is tied to a context
	// that rotation cancels to cut in-flight streams immediately.
	playCtx, release, err := pub.share.AcquirePlay(ctx, share)
	if err != nil {
		if errors.Is(err, core.ErrSharePlayLimit) {
			w.Header().Set("Retry-After", strconv.Itoa(playRetryAfterSeconds))
			http.Error(w, "too many concurrent listeners for this share, please retry shortly", http.StatusTooManyRequests)
			return
		}
		log.Error(ctx, "Error acquiring shared play slot", "share", info.shareID, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer release()

	stream, err := pub.streamer.NewStream(playCtx, mf, streampkg.Request{
		Format: info.format, BitRate: info.bitrate,
	})
	if err != nil {
		if errors.Is(err, streampkg.ErrTooManyTranscodes) {
			w.Header().Set("Retry-After", strconv.Itoa(streampkg.RetryAfterSeconds))
			http.Error(w, "too many concurrent transcodes, please retry shortly", http.StatusTooManyRequests)
			return
		}
		log.Error(ctx, "Error starting shared stream", err)
		http.Error(w, "invalid request", http.StatusInternalServerError)
		return
	}
	// Abort reads promptly when rotation cancels playCtx (raw streams otherwise
	// only notice a client disconnect, not a server-side revocation).
	stream.ReadCloser = &playCtxReader{ReadCloser: stream.ReadCloser, ctx: playCtx}

	// Make sure the stream will be closed at the end, to avoid leakage
	defer func() {
		if err := stream.Close(); err != nil && log.IsGreaterOrEqualTo(log.LevelDebug) {
			log.Error("Error closing shared stream", "id", info.id, "file", stream.Name(), err)
		}
	}()

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Content-Duration", strconv.FormatFloat(float64(stream.Duration()), 'G', -1, 32))

	n, err := stream.Serve(playCtx, w, r)
	if err != nil || n == 0 {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// playRetryAfterSeconds mirrors the transcode limiter's hint: listeners that
// hit the per-share play cap are told to back off briefly.
const playRetryAfterSeconds = 5

// playCtxReader wraps a stream so reads fail as soon as the play context is
// canceled (client disconnect or share generation rotation), aborting even
// raw (non-transcoded) streams instead of draining to the end.
type playCtxReader struct {
	io.ReadCloser
	ctx context.Context
}

func (r *playCtxReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

type shareTrackInfo struct {
	id      string
	format  string
	bitrate int
	shareID string
	epoch   int
}

func shareContainsTrack(share *model.Share, mediaFileID string) bool {
	return slices.ContainsFunc(share.Tracks, func(mf model.MediaFile) bool {
		return mf.ID == mediaFileID
	})
}

// decodeStreamInfo decodes the signed share-link token. This is a scoped
// public-share capability, not an auth credential; see encodeMediafileShare for
// why a JWT is used here.
func decodeStreamInfo(tokenString string) (shareTrackInfo, error) {
	c, err := auth.ValidatePublic(tokenString)
	if err != nil {
		return shareTrackInfo{}, err
	}
	if c.ID == "" {
		return shareTrackInfo{}, errors.New("required claim \"id\" not found")
	}
	if c.ShareID == "" {
		return shareTrackInfo{}, errors.New("required claim \"sid\" not found")
	}
	return shareTrackInfo{
		id:      c.ID,
		format:  c.Format,
		bitrate: c.BitRate,
		shareID: c.ShareID,
		epoch:   c.ShareEpoch,
	}, nil
}
