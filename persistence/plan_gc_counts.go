package persistence

import (
	"context"

	. "github.com/Masterminds/squirrel"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
)

// GCPlanCounts returns read-only counts of the rows GC would remove during a scan.
// It mirrors the conditions in GC() but issues SELECT COUNT queries only: it never
// modifies the database, which makes it safe to use from scan-plan previews.
func (s *SQLStore) GCPlanCounts(ctx context.Context, libraryIDs ...int) (model.PlanGCSummary, error) {
	r := sqlRepository{ctx: ctx, db: s.getDBXBuilder()}

	count := func(sq SelectBuilder) int64 {
		var res struct {
			Count int64
		}
		if err := r.queryOne(sq, &res); err != nil {
			log.Warn(ctx, "Scanner: Error counting GC candidates for plan preview", err)
			return 0
		}
		return res.Count
	}
	scoped := func(sq SelectBuilder) SelectBuilder {
		if len(libraryIDs) > 0 {
			sq = sq.Where(Eq{"library_id": libraryIDs})
		}
		return sq
	}

	var summary model.PlanGCSummary

	// purge empty albums (albumRepository.purgeEmpty)
	summary.EmptyAlbums = count(scoped(
		Select("count(*) as count").From("album").
			Where("id not in (select distinct(album_id) from media_file)")))

	// purge empty artists (artistRepository.purgeEmpty)
	summary.EmptyArtists = count(
		Select("count(*) as count").From("artist").
			Where("id not in (select artist_id from album_artists)"))

	// purge empty folders (folderRepository.purgeEmpty)
	summary.EmptyFolders = count(scoped(
		Select("count(*) as count").From("folder").
			Where(And{
				Eq{"num_audio_files": 0},
				Eq{"num_playlists":   0},
				Eq{"image_files":     "[]"},
				ConcatExpr("id not in (select parent_id from folder)"),
				ConcatExpr("id not in (select folder_id from media_file)"),
			})))

	// clean orphan annotations (sqlRepository.cleanAnnotations for album/artist/media_file/playlist)
	for _, table := range []string{"album", "artist", "media_file", "playlist"} {
		summary.OrphanAnnotations += count(
			Select("count(*) as count").From("annotation").
				Where(Eq{"item_type": table}).
				Where("item_id not in (select id from " + table + ")"))
	}

	// purge unused tags (tagRepository.purgeUnused)
	summary.UnusedTags = count(
		Select("count(*) as count").From("tag").
			Where(`
id not in (select jt.value
from album left join json_tree(album.tags, '$') as jt
where atom is not null
  and key = 'id'
UNION
select jt.value
from media_file left join json_tree(media_file.tags, '$') as jt
where atom is not null
  and key = 'id')
`))

	// remove orphan playlist tracks (playlistRepository.removeOrphans)
	summary.OrphanPlaylistTracks = count(
		Select("count(*) as count").From("playlist_tracks").
			LeftJoin("media_file on playlist_tracks.media_file_id = media_file.id").
			Where(Eq{"media_file.id": nil}))

	return summary, nil
}
