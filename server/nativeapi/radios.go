package nativeapi

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/deluan/rest"
	"github.com/go-chi/chi/v5"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/artwork"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server"
)

func (api *Router) addRadioRoute(r chi.Router) {
	repo := api.ds.Radio()
	r.Route("/radio", func(r chi.Router) {
		r.Get("/", rest.GetAll(repo))
		r.Post("/", rest.Post(repo))
		r.Route("/{id}", func(r chi.Router) {
			r.Use(server.URLParamsMiddleware)
			r.Get("/", rest.Get(repo))
			r.Put("/", rest.Put(repo))
			r.Delete("/", rest.Delete(repo))
			r.Post("/image", api.uploadRadioImage())
			r.Delete("/image", api.deleteRadioImage())
		})
	})
}

func (api *Router) uploadRadioImage() http.HandlerFunc {
	return handleImageUpload(func(ctx context.Context, reader io.Reader, ext string) error {
		radioID := chi.URLParamFromCtx(ctx, "id")
		radio, err := api.ds.Radio().Get(ctx, radioID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return model.ErrNotFound
			}
			return err
		}
		commit := artwork.CoverCommit{
			Current: func(ctx context.Context) (string, error) {
				current, err := api.ds.Radio().Get(ctx, radioID)
				if err != nil {
					return "", err
				}
				return current.UploadedImage, nil
			},
			Persist: func(ctx context.Context, filename string) error {
				radio.UploadedImage = filename
				return api.ds.Radio().Put(ctx, radio, "UploadedImage")
			},
		}
		return api.imgUpload.SetImage(ctx, consts.EntityRadio, radio.ID, radio.Name, reader, ext, commit)
	})
}

func (api *Router) deleteRadioImage() http.HandlerFunc {
	return handleImageDelete(func(ctx context.Context) error {
		radioID := chi.URLParamFromCtx(ctx, "id")
		radio, err := api.ds.Radio().Get(ctx, radioID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return model.ErrNotFound
			}
			return err
		}
		commit := artwork.CoverCommit{
			Current: func(ctx context.Context) (string, error) {
				current, err := api.ds.Radio().Get(ctx, radioID)
				if err != nil {
					return "", err
				}
				return current.UploadedImage, nil
			},
			Persist: func(ctx context.Context, _ string) error {
				radio.UploadedImage = ""
				return api.ds.Radio().Put(ctx, radio, "UploadedImage")
			},
		}
		return api.imgUpload.RemoveImage(ctx, consts.EntityRadio, radio.ID, commit)
	})
}
