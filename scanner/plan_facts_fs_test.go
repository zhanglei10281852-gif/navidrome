package scanner_test

import (
	"context"
	"path/filepath"
	"testing/fstest"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/conf/configtest"
	"github.com/navidrome/navidrome/core/storage/storagetest"
	"github.com/navidrome/navidrome/db"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/persistence"
	"github.com/navidrome/navidrome/scanner"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Plan FS fingerprint", Ordered, func() {
	var (
		ctx  context.Context
		ds   *tests.MockDataStore
		lib  model.Library
		fsys storagetest.FakeFS
	)

	BeforeAll(func() {
		ctx = context.Background()
		conf.Server.DbPath = filepath.Join(GinkgoT().TempDir(), "test-fsfp.db?_journal_mode=WAL")
		db.Db().SetMaxOpenConns(1)
	})

	BeforeEach(func() {
		DeferCleanup(configtest.SetupConfig())
		conf.Server.MusicFolder = "fake:///music"
		db.Init(ctx)
		DeferCleanup(func() { Expect(tests.ClearDB()).To(Succeed()) })

		ds = &tests.MockDataStore{RealDS: persistence.New(db.Db())}
		lib = model.Library{ID: 1, Name: "Fake Library", Path: "fake:///music"}
		Expect(ds.Library(ctx).Put(&lib)).To(Succeed())

		templ := storagetest.Template(_t{"albumartist": "The Beatles", "album": "Revolver", "year": 1966})
		fsys = createFS(fstest.MapFS{
			"The Beatles/Revolver/01 - Taxman.mp3":          templ(storagetest.Track(1, "Taxman")),
			"The Beatles/Revolver/02 - Eleanor Rigby.mp3":   templ(storagetest.Track(2, "Eleanor Rigby")),
			"The Beatles/Help!/01 - Help!.mp3":              storagetest.Template(_t{"albumartist": "The Beatles", "album": "Help!", "year": 1965})(storagetest.Track(1, "Help!")),
		})
	})

	fingerprint := func(targets ...model.ScanTarget) string {
		fp, err := scanner.ComputePlanFSFingerprint(ctx, ds, targets, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(fp).ToNot(BeEmpty())
		return fp
	}

	It("is stable across runs", func() {
		Expect(fingerprint()).To(Equal(fingerprint()))
	})

	It("changes when a file is added or removed", func() {
		baseline := fingerprint()

		templ := storagetest.Template(_t{"albumartist": "The Beatles", "album": "Revolver", "year": 1966})
		fsys.Add("The Beatles/Revolver/03 - New Song.mp3", templ(storagetest.Track(3, "New Song")), time.Now())
		withNew := fingerprint()
		Expect(withNew).ToNot(Equal(baseline), "adding a file must change the FS fingerprint")

		fsys.Remove("The Beatles/Revolver/03 - New Song.mp3")
		// Directory mtimes only move forward (like a real FS), so the fingerprint is stable
		// over the new state but need not equal the pre-add baseline.
		afterRemove := fingerprint()
		Expect(afterRemove).ToNot(Equal(withNew), "removing the added file must change the FS fingerprint")
		Expect(fingerprint()).To(Equal(afterRemove), "fingerprint must be stable for an unchanged state")

		fsys.Remove("The Beatles/Help!/01 - Help!.mp3")
		Expect(fingerprint()).ToNot(Equal(afterRemove), "deleting an original file must change the FS fingerprint")
	})

	It("changes when a file is touched (newer mtime)", func() {
		baseline := fingerprint()
		fsys.Touch("The Beatles/Revolver/01 - Taxman.mp3", time.Now().Add(time.Hour))
		Expect(fingerprint()).ToNot(Equal(baseline))
	})

	It("ignores changes outside the targeted scope", func() {
		baseline := fingerprint(model.ScanTarget{LibraryID: 1, FolderPath: "The Beatles/Revolver"})

		// Change inside the scope -> fingerprint must change
		fsys.Touch("The Beatles/Revolver/01 - Taxman.mp3", time.Now().Add(2*time.Hour))
		inScope := fingerprint(model.ScanTarget{LibraryID: 1, FolderPath: "The Beatles/Revolver"})
		Expect(inScope).ToNot(Equal(baseline))

		// Change outside the scope (sibling folder) -> fingerprint of the scoped walk is unchanged
		outOfScopeBaseline := fingerprint(model.ScanTarget{LibraryID: 1, FolderPath: "The Beatles/Help!"})
		fsys.Touch("The Beatles/Revolver/02 - Eleanor Rigby.mp3", time.Now().Add(3*time.Hour))
		Expect(fingerprint(model.ScanTarget{LibraryID: 1, FolderPath: "The Beatles/Help!"})).
			To(Equal(outOfScopeBaseline))
	})

	It("does not extract tags while fingerprinting", func() {
		// The fingerprint walk is stat-only: an audio file with invalid tags must not
		// break it (tag extraction would fail on this double-extension "corrupt" file).
		fsys.Add("The Beatles/Revolver/not-audio.mp3", &fstest.MapFile{Data: []byte("this is not an audio file")})
		Expect(func() { fingerprint() }).ToNot(Panic())
	})
})
