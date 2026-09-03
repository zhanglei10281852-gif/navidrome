package public

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/navidrome/navidrome/core/artwork"
	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("decodeArtworkID", func() {
	BeforeEach(func() {
		auth.PublicTokenAuth = jwtauth.New("HS256", []byte("super secret"), nil)
	})

	It("fails to decode an invalid token", func() {
		_, _, err := decodeArtworkID("xx-123")
		Expect(err).To(HaveOccurred())
	})

	It("fails to decode a token without an id", func() {
		token, _ := auth.CreatePublicToken(auth.Claims{})
		_, _, err := decodeArtworkID(token)
		Expect(err).To(HaveOccurred())
	})
})

type fakeArtwork struct {
	artwork.Artwork
	img *artwork.Image
	err error
}

func (f *fakeArtwork) Get(context.Context, model.ArtworkID, int, bool) (*artwork.Image, error) {
	return f.img, f.err
}

var _ = Describe("handleImages", func() {
	var w *httptest.ResponseRecorder

	newImageRequest := func(claimID string) *http.Request {
		auth.PublicTokenAuth = jwtauth.New("HS256", []byte("super secret"), nil)
		token, _ := auth.CreatePublicToken(auth.Claims{ID: claimID})
		return httptest.NewRequest("GET", "/img?:id="+url.QueryEscape(token), nil)
	}

	// The handler re-checks that the entity behind the token still exists, so "1" must be live.
	var ds *tests.MockDataStore

	BeforeEach(func() {
		w = httptest.NewRecorder()
		albumRepo := tests.CreateMockAlbumRepo()
		albumRepo.SetData(model.Albums{{ID: "1"}})
		ds = &tests.MockDataStore{MockedAlbum: albumRepo}
	})

	It("returns 404 when the artwork is unavailable", func() {
		pub := &Router{ds: ds, artwork: &fakeArtwork{err: artwork.ErrUnavailable}}
		pub.handleImages(w, newImageRequest("al-1"))
		Expect(w.Code).To(Equal(http.StatusNotFound))
	})

	It("returns 404 when the artwork is not found", func() {
		pub := &Router{ds: ds, artwork: &fakeArtwork{err: model.ErrNotFound}}
		pub.handleImages(w, newImageRequest("al-1"))
		Expect(w.Code).To(Equal(http.StatusNotFound))
	})

	It("serves the image immutable when the token asserts the current hash", func() {
		const hash = "0123456789abcdef"
		img := &artwork.Image{ReadCloser: io.NopCloser(bytes.NewReader([]byte("IMG"))), Hash: hash}
		pub := &Router{ds: ds, artwork: &fakeArtwork{img: img}}
		pub.handleImages(w, newImageRequest("al-1_"+hash))
		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header().Get("Cache-Control")).To(Equal("public, max-age=31536000, immutable"))
		Expect(w.Header().Get("ETag")).To(Equal(`"` + hash + `"`))
	})

	Describe("share-bound artwork tokens", func() {
		var shareRepo *tests.MockShareRepo
		var img *artwork.Image

		BeforeEach(func() {
			auth.PublicTokenAuth = jwtauth.New("HS256", []byte("super secret"), nil)
			shareRepo = &tests.MockShareRepo{}
			ds.MockedShare = shareRepo
			img = &artwork.Image{ReadCloser: io.NopCloser(bytes.NewReader([]byte("IMG"))), Hash: "abc"}
		})

		shareRequest := func(claims auth.Claims) *http.Request {
			token, _ := auth.CreatePublicToken(claims)
			return httptest.NewRequest("GET", "/img?:id="+url.QueryEscape(token), nil)
		}

		It("serves artwork when the token generation matches the share", func() {
			shareRepo.ID = "s1"
			shareRepo.Entity = &model.Share{ID: "s1", Epoch: 2}
			pub := &Router{ds: ds, artwork: &fakeArtwork{img: img}}
			pub.handleImages(w, shareRequest(auth.Claims{ID: "al-1", ShareID: "s1", ShareEpoch: 2}))
			Expect(w.Code).To(Equal(http.StatusOK))
		})

		It("returns 410 for artwork from a previous generation", func() {
			shareRepo.ID = "s1"
			shareRepo.Entity = &model.Share{ID: "s1", Epoch: 2}
			pub := &Router{ds: ds, artwork: &fakeArtwork{img: img}}
			pub.handleImages(w, shareRequest(auth.Claims{ID: "al-1", ShareID: "s1", ShareEpoch: 1}))
			Expect(w.Code).To(Equal(http.StatusGone))
		})

		It("returns 404 when the share no longer exists", func() {
			shareRepo.ID = "other"
			pub := &Router{ds: ds, artwork: &fakeArtwork{img: img}}
			pub.handleImages(w, shareRequest(auth.Claims{ID: "al-1", ShareID: "gone", ShareEpoch: 0}))
			Expect(w.Code).To(Equal(http.StatusNotFound))
		})

		It("returns 410 when the share expired", func() {
			shareRepo.ID = "s1"
			expired := time.Now().Add(-time.Hour)
			shareRepo.Entity = &model.Share{ID: "s1", Epoch: 0, ExpiresAt: &expired}
			pub := &Router{ds: ds, artwork: &fakeArtwork{img: img}}
			pub.handleImages(w, shareRequest(auth.Claims{ID: "al-1", ShareID: "s1"}))
			Expect(w.Code).To(Equal(http.StatusGone))
		})
	})

})
