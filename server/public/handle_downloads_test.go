package public

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/navidrome/navidrome/core"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type mockArchiver struct {
	called bool
	err    error
}

func (m *mockArchiver) ZipAlbum(context.Context, string, string, int, io.Writer) error {
	return nil
}

func (m *mockArchiver) ZipArtist(context.Context, string, string, int, io.Writer) error {
	return nil
}

func (m *mockArchiver) ZipPlaylist(context.Context, string, string, int, io.Writer) error {
	return nil
}

func (m *mockArchiver) ZipShare(_ context.Context, _ *model.Share, w io.Writer) error {
	m.called = true
	if m.err != nil {
		return m.err
	}
	_, _ = w.Write([]byte("zip-contents"))
	return nil
}

var _ = Describe("handleDownloads", func() {
	var ds *tests.MockDataStore
	var shareRepo *tests.MockShareRepo
	var archiver *mockArchiver
	var pub *Router

	BeforeEach(func() {
		ds = &tests.MockDataStore{}
		shareRepo = &tests.MockShareRepo{}
		ds.MockedShare = shareRepo
		archiver = &mockArchiver{}
		pub = &Router{ds: ds, archiver: archiver, share: core.NewShare(ds)}
	})

	shareIs := func(s *model.Share) {
		shareRepo.ID = s.ID
		shareRepo.Entity = s
	}

	makeRequest := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/public/d/"+id+"?%3Aid="+id, nil)
		w := httptest.NewRecorder()
		pub.handleDownloads(w, r)
		return w
	}

	It("sets a Content-Disposition filename from the share description", func() {
		shareIs(&model.Share{ID: "abc123", Description: "My Mixtape", Downloadable: true})

		w := makeRequest("abc123")

		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(w.Header().Get("Content-Disposition")).To(Equal(`attachment; filename="My Mixtape.zip"`))
		Expect(w.Header().Get("Content-Type")).To(Equal("application/zip"))
		Expect(archiver.called).To(BeTrue())
		Expect(w.Body.String()).To(Equal("zip-contents"))
	})

	It("falls back to the share ID when there is no description", func() {
		shareIs(&model.Share{ID: "abc123", Downloadable: true})

		w := makeRequest("abc123")

		Expect(w.Header().Get("Content-Disposition")).To(Equal(`attachment; filename="abc123.zip"`))
	})

	It("sanitizes characters that are unsafe in a filename", func() {
		shareIs(&model.Share{ID: "abc123", Description: `AC/DC: Live, 1979`, Downloadable: true})

		w := makeRequest("abc123")

		Expect(w.Header().Get("Content-Disposition")).To(Equal(`attachment; filename="AC_DC_ Live_ 1979.zip"`))
	})

	It("returns 403 without invoking the archiver when the share is not downloadable", func() {
		shareIs(&model.Share{ID: "abc123", Description: "No Download", Downloadable: false})

		w := makeRequest("abc123")

		Expect(w.Code).To(Equal(http.StatusForbidden))
		Expect(archiver.called).To(BeFalse())
		Expect(w.Header().Get("Content-Disposition")).To(BeEmpty())
	})

	It("returns 404 when the share does not exist", func() {
		shareIs(&model.Share{ID: "other", Downloadable: true})

		w := makeRequest("missing")

		Expect(w.Code).To(Equal(http.StatusNotFound))
		Expect(archiver.called).To(BeFalse())
	})

	It("returns 410 when the share has expired", func() {
		shareIs(&model.Share{ID: "abc123", Downloadable: true, ExpiresAt: new(time.Now().Add(-time.Hour))})

		w := makeRequest("abc123")

		Expect(w.Code).To(Equal(http.StatusGone))
		Expect(archiver.called).To(BeFalse())
	})

	It("returns 500 when the share lookup fails", func() {
		shareRepo.Error = errors.New("db error")

		w := makeRequest("abc123")

		Expect(w.Code).To(Equal(http.StatusInternalServerError))
		Expect(archiver.called).To(BeFalse())
	})

	It("settles a delivered download bound to a signed token", func() {
		s := &model.Share{ID: "tok1", Description: "Tok", Downloadable: true}
		shareIs(s)
		token := encodeShareToken(*s)

		w := makeRequest(token)

		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(archiver.called).To(BeTrue())
		Expect(shareRepo.SettleCallCount).To(Equal(1))
		Expect(shareRepo.SettleCalledWith).To(Equal("tok1"))
	})

	It("returns 410 for a download token from a previous generation", func() {
		shareIs(&model.Share{ID: "tok2", Downloadable: true, Epoch: 3})
		stale := encodeShareToken(model.Share{ID: "tok2", Epoch: 2})

		w := makeRequest(stale)

		Expect(w.Code).To(Equal(http.StatusGone))
		Expect(archiver.called).To(BeFalse())
		Expect(shareRepo.SettleCallCount).To(BeZero())
	})

	It("rejects legacy raw ids once the share was rotated", func() {
		shareIs(&model.Share{ID: "legacy", Downloadable: true, Epoch: 1})

		w := makeRequest("legacy")

		Expect(w.Code).To(Equal(http.StatusGone))
		Expect(archiver.called).To(BeFalse())
	})

	It("returns 400 for a malformed token-like id", func() {
		shareIs(&model.Share{ID: "abc123", Downloadable: true})

		w := makeRequest("not-a-valid.jwt-token")

		Expect(w.Code).To(Equal(http.StatusBadRequest))
		Expect(archiver.called).To(BeFalse())
	})

	It("does not settle when the archive fails to build", func() {
		shareIs(&model.Share{ID: "fail", Downloadable: true})
		archiver.err = errors.New("zip boom")

		w := makeRequest("fail")

		Expect(w.Code).To(Equal(http.StatusInternalServerError))
		Expect(archiver.called).To(BeTrue())
		Expect(shareRepo.SettleCallCount).To(BeZero())
	})

	It("returns 403 when the cumulative download budget is exhausted", func() {
		shareIs(&model.Share{ID: "maxed", Downloadable: true, MaxDownloads: 1, DownloadCount: 1})

		w := makeRequest("maxed")

		Expect(w.Code).To(Equal(http.StatusForbidden))
		Expect(archiver.called).To(BeFalse())
		Expect(shareRepo.SettleCallCount).To(BeZero())
	})

	It("returns 429 while the last slot is reserved in flight", func() {
		s := &model.Share{ID: "busy", Downloadable: true, MaxDownloads: 1}
		shareIs(s)
		lease, err := pub.share.ReserveDownload(context.Background(), s)
		Expect(err).ToNot(HaveOccurred())
		defer lease.Release()

		w := makeRequest("busy")

		Expect(w.Code).To(Equal(http.StatusTooManyRequests))
		Expect(w.Header().Get("Retry-After")).To(Equal("10"))
		Expect(archiver.called).To(BeFalse())
	})

	It("admits a new download after the in-flight reservation is released", func() {
		s := &model.Share{ID: "retry", Downloadable: true, MaxDownloads: 1}
		shareIs(s)
		lease, err := pub.share.ReserveDownload(context.Background(), s)
		Expect(err).ToNot(HaveOccurred())
		lease.Release()

		w := makeRequest("retry")

		Expect(w.Code).To(Equal(http.StatusOK))
		Expect(shareRepo.SettleCallCount).To(Equal(1))
	})
})
