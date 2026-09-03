package public

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/navidrome/navidrome/core/artwork"
	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/server/imghttp"
	"github.com/navidrome/navidrome/utils/req"
)

func (pub *Router) handleImages(w http.ResponseWriter, r *http.Request) {
	// If context is already canceled, discard request without further processing
	if r.Context().Err() != nil {
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	p := req.Params(r)
	id, _ := p.String(":id")
	if id == "" {
		log.Warn(r, "No id provided")
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	artId, claims, err := decodeArtworkID(id)
	if err != nil {
		log.Error(r, "Error decoding artwork id", "id", id, err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	// Share-bound artwork tokens must reference a live share at the exact
	// generation they were minted for. Rotating the share invalidates cover
	// links derived from earlier pages, just like streams and downloads.
	// Tokens without a share claim (Subsonic image links, plugins) keep the
	// legacy token-as-authorization behavior.
	if claims.ShareID != "" {
		share, err := pub.ds.Share(ctx).Get(claims.ShareID)
		if err != nil {
			checkShareError(ctx, w, err, claims.ShareID)
			return
		}
		if expiresAt := share.ExpiresAt; expiresAt != nil && expiresAt.Before(time.Now()) {
			checkShareError(ctx, w, model.ErrExpired, claims.ShareID)
			return
		}
		if claims.ShareEpoch != share.Epoch {
			log.Warn(ctx, "Rejecting shared artwork from a previous generation",
				"share", claims.ShareID, "tokenEpoch", claims.ShareEpoch, "currentEpoch", share.Epoch)
			http.Error(w, "Share not available anymore", http.StatusGone)
			return
		}
	}
	// The token is the authorization, so the entity check must ask "is it still there", not
	// "may this user see it" -- the latter would hide a shared private playlist.
	ctx = request.WithUser(ctx, model.User{IsAdmin: true})
	size := p.IntOr("size", 0)
	square := p.BoolOr("square", false)

	img, err := pub.artwork.Get(ctx, artId, size, square)
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, model.ErrNotFound):
		log.Warn(r, "Couldn't find coverArt", "id", id, err)
		http.Error(w, "Artwork not found", http.StatusNotFound)
		return
	case errors.Is(err, artwork.ErrUnavailable):
		log.Debug(r, "Item does not have artwork", "id", id, err)
		http.Error(w, "Artwork not found", http.StatusNotFound)
		return
	case err != nil:
		log.Error(r, "Error retrieving coverArt", "id", id, err)
		http.Error(w, "Error retrieving coverArt", http.StatusInternalServerError)
		return
	}

	defer img.Close()
	if imghttp.WriteImageHeaders(w, r, img, artId.Hash) {
		return
	}
	cnt, err := io.Copy(w, img)
	if err != nil {
		log.Warn(ctx, "Error sending image", "count", cnt, err)
	}
}

func decodeArtworkID(tokenString string) (model.ArtworkID, auth.Claims, error) {
	token, err := auth.PublicTokenAuth.Decode(tokenString)
	if err != nil {
		return model.ArtworkID{}, auth.Claims{}, err
	}
	if token == nil {
		return model.ArtworkID{}, auth.Claims{}, errors.New("unauthorized")
	}
	c := auth.ClaimsFromToken(token)
	if c.ID == "" {
		return model.ArtworkID{}, auth.Claims{}, errors.New("required claim \"id\" not found")
	}
	artID, err := model.ParseArtworkID(c.ID)
	if err == nil {
		return artID, c, nil
	}
	// Try to default to mediafile artworkId (if used with a mediafileShare token)
	artID, err = model.ParseArtworkID("mf-" + c.ID)
	return artID, c, err
}
