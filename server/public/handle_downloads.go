package public

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/navidrome/navidrome/core"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/utils/req"
	"github.com/navidrome/navidrome/utils/str"
)

// errDownloadRevoked is returned by the revocation-aware writer once the share
// has been rotated mid-delivery; it aborts the archive without settling.
var errDownloadRevoked = errors.New("share generation rotated during download")

func (pub *Router) handleDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := req.Params(r).String(":id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var s *model.Share
	// A download link is a signed, generation-bound token. Links minted before
	// this feature are raw share IDs (nanoid, no JWT dots): keep them working
	// for shares that were never rotated.
	shareID, epoch, tokenErr := decodeShareToken(id)
	switch {
	case tokenErr == nil:
		s, err = pub.share.Load(ctx, shareID)
		if err != nil {
			checkShareError(ctx, w, err, shareID)
			return
		}
		if epoch != s.Epoch {
			log.Warn(ctx, "Rejecting shared download from a previous generation",
				"share", shareID, "tokenEpoch", epoch, "currentEpoch", s.Epoch)
			http.Error(w, "Share not available anymore", http.StatusGone)
			return
		}
	case strings.Contains(id, "."):
		// Looks like a malformed/forged token rather than a legacy share id.
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	default:
		s, err = pub.share.Load(ctx, id)
		if err != nil {
			checkShareError(ctx, w, err, id)
			return
		}
		// Legacy links stop working the first time the owner rotates.
		if s.Epoch != 0 {
			log.Warn(ctx, "Rejecting legacy shared download link after rotation",
				"share", id, "currentEpoch", s.Epoch)
			http.Error(w, "Share not available anymore", http.StatusGone)
			return
		}
	}

	if !s.Downloadable {
		checkShareError(ctx, w, model.ErrNotAuthorized, s.ID)
		return
	}

	// Reserve one slot of the cumulative download budget before any byte is
	// written. The reservation is atomic per share, so concurrent requests can
	// never over-issue the last remaining slot.
	lease, err := pub.share.ReserveDownload(ctx, s)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrShareDownloadLimit):
			http.Error(w, "Download limit reached for this share", http.StatusForbidden)
		case errors.Is(err, core.ErrShareDownloadsBusy):
			w.Header().Set("Retry-After", strconv.Itoa(downloadRetryAfterSeconds))
			http.Error(w, "too many concurrent downloads for this share, please retry shortly", http.StatusTooManyRequests)
		default:
			http.Error(w, "Error retrieving share", http.StatusInternalServerError)
		}
		return
	}

	name := str.SanitizeFilename(cmp.Or(s.Description, s.ID))
	name = strings.ReplaceAll(name, ",", "_")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".zip"))
	w.Header().Set("Content-Type", "application/zip")

	// Load the share before streaming: once ZipShare writes its first byte the
	// status is locked at 200, so errors could no longer be reported.
	dw := &downloadWriter{ResponseWriter: w, lease: lease}
	zipErr := pub.archiver.ZipShare(ctx, s, dw)

	// Settle only when the archive was fully delivered: archiver succeeded and
	// every response write reached the client. Rotation, a mid-flight write
	// failure (client disconnect), or an archiver error frees the reservation
	// without incrementing the cumulative counter.
	if zipErr == nil && dw.writeErr == nil && !lease.Revoked() {
		lease.Commit()
	} else {
		lease.Release()
		// A rotation-driven abort is intentional, not a server error; and once
		// bytes are on the wire no status can be sent anyway. Only surface
		// archiver failures that happened before the first byte was written.
		if zipErr != nil && !errors.Is(zipErr, errDownloadRevoked) && !dw.wroteBytes {
			checkShareError(ctx, w, zipErr, s.ID)
		}
	}
}

const downloadRetryAfterSeconds = 10

// downloadWriter wraps the response writer so an in-flight download can be
// aborted (and never settled) when the share is rotated mid-delivery, and so
// write errors from a disconnecting client are captured for the settlement
// decision.
type downloadWriter struct {
	http.ResponseWriter
	lease      *core.DownloadLease
	writeErr   error
	wroteBytes bool
}

func (dw *downloadWriter) Write(p []byte) (int, error) {
	if dw.writeErr != nil {
		return 0, dw.writeErr
	}
	if dw.lease != nil && dw.lease.Revoked() {
		dw.writeErr = errDownloadRevoked
		return 0, errDownloadRevoked
	}
	n, err := dw.ResponseWriter.Write(p)
	if n > 0 {
		dw.wroteBytes = true
	}
	if err != nil && dw.writeErr == nil {
		dw.writeErr = err
	}
	return n, err
}
