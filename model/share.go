package model

import (
	"cmp"
	"strings"
	"time"

	"github.com/navidrome/navidrome/utils/random"
)

type Share struct {
	ID            string     `structs:"id" json:"id,omitempty"`
	UserID        string     `structs:"user_id" json:"userId,omitempty"`
	Username      string     `structs:"-" json:"username,omitempty"`
	Description   string     `structs:"description" json:"description,omitempty"`
	Downloadable  bool       `structs:"downloadable" json:"downloadable"`
	ExpiresAt     *time.Time `structs:"expires_at" json:"expiresAt,omitempty"`
	LastVisitedAt *time.Time `structs:"last_visited_at" json:"lastVisitedAt,omitempty"`
	ResourceIDs   string     `structs:"resource_ids" json:"resourceIds,omitempty"`
	ResourceType  string     `structs:"resource_type" json:"resourceType,omitempty"`
	Contents      string     `structs:"contents" json:"contents,omitempty"`
	Format        string     `structs:"format" json:"format,omitempty"`
	MaxBitRate    int        `structs:"max_bit_rate" json:"maxBitRate,omitempty"`
	VisitCount    int        `structs:"visit_count" json:"visitCount,omitempty"`
	// Epoch is the share's current access generation. Media links (streams,
	// images, downloads) carry the epoch they were minted for; rotating the
	// epoch invalidates every link derived from an earlier page. Server-managed:
	// clients cannot set it through the generic REST API.
	Epoch int `structs:"epoch" json:"epoch,omitempty"`
	// MaxConcurrentPlays caps simultaneous public streams (raw or transcoded)
	// for this share. 0 means unlimited.
	MaxConcurrentPlays int `structs:"max_concurrent_plays" json:"maxConcurrentPlays,omitempty"`
	// MaxDownloads is the cumulative number of fully delivered archive
	// downloads allowed for this share. 0 means unlimited.
	MaxDownloads int `structs:"max_downloads" json:"maxDownloads,omitempty"`
	// DownloadCount is the number of archive downloads settled (fully
	// delivered). Server-managed.
	DownloadCount int `structs:"download_count" json:"downloadCount,omitempty"`
	CreatedAt     time.Time  `structs:"created_at" json:"createdAt"`
	UpdatedAt     time.Time  `structs:"updated_at" json:"updatedAt"`
	Tracks        MediaFiles `structs:"-" json:"tracks,omitempty"`
	Albums        Albums     `structs:"-" json:"albums,omitempty"`
	// ActivePlays is the current in-memory number of active streams for the
	// share, filled by the share service for owner-facing responses.
	ActivePlays   int    `structs:"-" json:"activePlays,omitempty"`
	URL           string `structs:"-" json:"-"`
	ImageURL      string `structs:"-" json:"-"`
	DownloadToken string `structs:"-" json:"-"`
}

func (s Share) CoverArtID() ArtworkID {
	ids := strings.SplitN(s.ResourceIDs, ",", 2)
	if len(ids) == 0 {
		return ArtworkID{}
	}
	switch s.ResourceType {
	case "album":
		return Album{ID: ids[0]}.CoverArtID()
	case "playlist":
		return Playlist{ID: ids[0]}.CoverArtID()
	case "artist":
		return Artist{ID: ids[0]}.CoverArtID()
	}
	rnd := random.Int64N(len(s.Tracks))
	return s.Tracks[rnd].CoverArtID()
}

type Shares []Share

// ToM3U8 exports the share to the Extended M3U8 format.
func (s Share) ToM3U8() string {
	return s.Tracks.ToM3U8(cmp.Or(s.Description, s.ID), false)
}

type ShareRepository interface {
	Exists(id string) (bool, error)
	Get(id string) (*Share, error)
	GetAll(options ...QueryOptions) (Shares, error)
	CountAll(options ...QueryOptions) (int64, error)
	// RotateEpoch atomically increments the share's access generation and
	// returns the new value. It is ownership-restricted, like Update/Delete.
	RotateEpoch(id string) (int, error)
	// SettleDownload atomically increments download_count, but only while the
	// cumulative budget is not exhausted (max_downloads = 0 means unlimited).
	// It is intentionally not ownership-restricted: it is called by the
	// anonymous public download path after a full archive delivery. It reports
	// whether the row was updated.
	SettleDownload(id string) (bool, error)
}
