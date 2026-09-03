package subsonic

import (
	"net/http"
	"strings"
	"time"

	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server/public"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	"github.com/navidrome/navidrome/utils/req"
	"github.com/navidrome/navidrome/utils/slice"
)

func (api *Router) GetShares(r *http.Request) (*responses.Subsonic, error) {
	repo := api.share.NewRepository(r.Context()).(model.ShareRepository)
	shares, err := repo.GetAll(model.QueryOptions{Sort: "created_at desc"})
	if err != nil {
		return nil, err
	}

	response := newResponse()
	response.Shares = &responses.Shares{}
	for _, share := range shares {
		response.Shares.Share = append(response.Shares.Share, api.buildShare(r, share))
	}
	return response, nil
}

func (api *Router) buildShare(r *http.Request, share model.Share) responses.Share {
	resp := responses.Share{
		ID:                 share.ID,
		Url:                public.ShareURL(r.Context(), share.ID),
		Description:        share.Description,
		Username:           share.Username,
		Created:            share.CreatedAt,
		Expires:            share.ExpiresAt,
		LastVisited:        share.LastVisitedAt,
		VisitCount:         int32(share.VisitCount),
		Epoch:              int32(share.Epoch),
		MaxConcurrentPlays: int32(share.MaxConcurrentPlays),
		ActivePlays:        int32(share.ActivePlays),
		MaxDownloads:       int32(share.MaxDownloads),
		DownloadCount:      int32(share.DownloadCount),
	}
	if resp.Description == "" {
		resp.Description = share.Contents
	}
	if len(share.Albums) > 0 {
		resp.Entry = slice.MapWithArg(share.Albums, r.Context(), childFromAlbum)
	} else {
		resp.Entry = slice.MapWithArg(share.Tracks, r.Context(), childFromMediaFile)
	}
	return resp
}

func (api *Router) CreateShare(r *http.Request) (*responses.Subsonic, error) {
	p := req.Params(r)
	ids := p.Strings("id")
	if len(ids) == 0 {
		return nil, newError(responses.ErrorMissingParameter, "missing parameter: 'id'")
	}

	description, _ := p.String("description")
	repo := api.share.NewRepository(r.Context())
	share := &model.Share{
		ResourceIDs:        strings.Join(ids, ","),
		Description:        description,
		ExpiresAt:          new(p.TimeOr("expires", time.Time{})),
		MaxConcurrentPlays: p.IntOr("maxConcurrentPlays", 0),
		MaxDownloads:       p.IntOr("maxDownloads", 0),
	}

	id, err := repo.(rest.Persistable).Save(share)
	if err != nil {
		return nil, err
	}

	share, err = repo.(model.ShareRepository).Get(id)
	if err != nil {
		return nil, err
	}

	response := newResponse()
	response.Shares = &responses.Shares{Share: []responses.Share{api.buildShare(r, *share)}}
	return response, nil
}

func (api *Router) UpdateShare(r *http.Request) (*responses.Subsonic, error) {
	p := req.Params(r)
	id, err := p.String("id")
	if err != nil {
		return nil, err
	}

	repo := api.share.NewRepository(r.Context())

	// Rotate first, so a rotate=true request also revokes every link derived
	// before the following field updates.
	if p.BoolOr("rotate", false) {
		if _, err := api.share.Rotate(r.Context(), id); err != nil {
			return nil, err
		}
	}

	description, _ := p.String("description")
	share := &model.Share{
		ID:          id,
		Description: description,
		ExpiresAt:   new(p.TimeOr("expires", time.Time{})),
	}

	// Budget columns are always written by the repository wrapper, so default
	// them to the stored values and only override what the request explicitly
	// provides (absent params must not reset limits to unlimited).
	if existing, gErr := repo.(model.ShareRepository).Get(id); gErr == nil {
		share.MaxConcurrentPlays = existing.MaxConcurrentPlays
		share.MaxDownloads = existing.MaxDownloads
	}
	if _, ok := r.URL.Query()["maxConcurrentPlays"]; ok {
		share.MaxConcurrentPlays = p.IntOr("maxConcurrentPlays", 0)
	}
	if _, ok := r.URL.Query()["maxDownloads"]; ok {
		share.MaxDownloads = p.IntOr("maxDownloads", 0)
	}

	if err = repo.(rest.Persistable).Update(id, share); err != nil {
		return nil, err
	}

	return newResponse(), nil
}

func (api *Router) DeleteShare(r *http.Request) (*responses.Subsonic, error) {
	p := req.Params(r)
	id, err := p.String("id")
	if err != nil {
		return nil, err
	}

	repo := api.share.NewRepository(r.Context())
	err = repo.(rest.Persistable).Delete(id)
	if err != nil {
		return nil, err
	}

	return newResponse(), nil
}
