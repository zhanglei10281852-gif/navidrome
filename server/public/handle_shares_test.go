package public

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/model"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("mapShareInfo", func() {
	BeforeEach(func() {
		auth.PublicTokenAuth = jwtauth.New("HS256", []byte("test-secret"), nil)
	})

	pub := &Router{}

	It("mints a generation-bound download token and cover image", func() {
		s := model.Share{
			ID:          "shareABC",
			Epoch:       2,
			Downloadable: true,
			Tracks:      model.MediaFiles{{ID: "mf-1", Title: "T"}},
		}
		r := httptest.NewRequest(http.MethodGet, "/share/shareABC", nil)

		mapped := pub.mapShareInfo(r, s)

		// Download token is share-bound and generation-bound.
		Expect(mapped.DownloadToken).ToNot(BeEmpty())
		sid, epoch, err := decodeShareToken(mapped.DownloadToken)
		Expect(err).ToNot(HaveOccurred())
		Expect(sid).To(Equal("shareABC"))
		Expect(epoch).To(Equal(2))

		// The OG cover image token carries sid + sep.
		imgURL, err := url.Parse(mapped.ImageURL)
		Expect(err).ToNot(HaveOccurred())
		imgToken := path.Base(imgURL.Path)
		imgClaims, err := auth.ValidatePublic(imgToken)
		Expect(err).ToNot(HaveOccurred())
		Expect(imgClaims.ShareID).To(Equal("shareABC"))
		Expect(imgClaims.ShareEpoch).To(Equal(2))

		// Track ids became stream tokens carrying the generation.
		Expect(mapped.Tracks).To(HaveLen(1))
		info, err := decodeStreamInfo(mapped.Tracks[0].ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(info.shareID).To(Equal("shareABC"))
		Expect(info.epoch).To(Equal(2))
	})

	It("mints non-expiring capabilities for a share without expiry", func() {
		s := model.Share{ID: "shareNoExp", Epoch: 1, Tracks: model.MediaFiles{{ID: "mf-2", Title: "T"}}}
		r := httptest.NewRequest(http.MethodGet, "/share/shareNoExp", nil)

		mapped := pub.mapShareInfo(r, s)
		sid, epoch, err := decodeShareToken(mapped.DownloadToken)
		Expect(err).ToNot(HaveOccurred())
		Expect(sid).To(Equal("shareNoExp"))
		Expect(epoch).To(Equal(1))

		// Re-decoding after the token age must still pass (no exp claim).
		time.Sleep(10 * time.Millisecond)
		_, _, err = decodeShareToken(mapped.DownloadToken)
		Expect(err).ToNot(HaveOccurred())
	})
})
