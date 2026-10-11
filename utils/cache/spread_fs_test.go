package cache

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/djherbis/stream"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Spread FS", func() {
	var fs *spreadFS
	var rootDir string

	BeforeEach(func() {
		var err error
		rootDir, _ = os.MkdirTemp("", "spread_fs")
		fs, err = NewSpreadFS(rootDir, 0755)
		Expect(err).To(BeNil())
	})
	AfterEach(func() {
		_ = os.RemoveAll(rootDir)
	})

	Describe("KeyMapper", func() {
		It("creates a file with proper name format", func() {
			mapped := fs.KeyMapper("abc")
			Expect(mapped).To(HavePrefix(fs.root))
			mapped = strings.TrimPrefix(mapped, fs.root)
			parts := strings.Split(mapped, string(filepath.Separator))
			Expect(parts).To(HaveLen(4))
			Expect(parts[3]).To(HaveLen(40))
		})
		It("returns the unmodified key if it is a cache file path", func() {
			mapped := fs.KeyMapper("abc")
			Expect(mapped).To(HavePrefix(fs.root))
			Expect(fs.KeyMapper(mapped)).To(Equal(mapped))
		})
	})

	Describe("Create", func() {
		It("leaves an already-open reader's bytes intact", func() {
			// Shrinking a file an older stream still serves spins its reader at a premature EOF.
			if runtime.GOOS == "windows" {
				Skip("Windows cannot unlink a file with open handles, so Create reuses the inode")
			}
			name := filepath.Join(rootDir, "aa", "bb", "data")
			s, err := stream.NewStream(name, fs)
			Expect(err).To(BeNil())
			_, err = s.Write([]byte("PARTIAL"))
			Expect(err).To(BeNil())
			r, err := s.NextReader()
			Expect(err).To(BeNil())
			Expect(s.Close()).To(Succeed())

			f, err := fs.Create(name)
			Expect(err).To(BeNil())
			_, err = f.Write([]byte("GOOD"))
			Expect(err).To(BeNil())
			Expect(f.Close()).To(Succeed())

			done := make(chan []byte, 1)
			go func() {
				b, _ := io.ReadAll(r)
				done <- b
			}()
			Eventually(done).Should(Receive(Equal([]byte("PARTIAL"))))
			Expect(r.Close()).To(Succeed())

			Expect(os.ReadFile(name)).To(Equal([]byte("GOOD")))
		})
	})

	Describe("MarkComplete / Remove markers", func() {
		It("creates a .complete marker for a data file", func() {
			data := fs.KeyMapper("song1")
			f, err := fs.Create(data)
			Expect(err).To(BeNil())
			_, _ = f.Write([]byte("ok"))
			_ = f.Close()

			Expect(fs.MarkComplete(data)).To(Succeed())
			_, statErr := os.Stat(data + ".complete")
			Expect(statErr).To(BeNil())
		})

		It("records the generation payload in the marker", func() {
			data := fs.KeyMapper("song-gen")
			f, err := fs.Create(data)
			Expect(err).To(BeNil())
			_, _ = f.Write([]byte("ok"))
			_ = f.Close()

			Expect(fs.MarkCompleteWith(data, "gabc123")).To(Succeed())
			payload, err := os.ReadFile(data + ".complete")
			Expect(err).To(BeNil())
			Expect(string(payload)).To(Equal("gabc123"))
		})

		It("removes the sibling marker when the data file is removed", func() {
			data := fs.KeyMapper("song2")
			f, err := fs.Create(data)
			Expect(err).To(BeNil())
			_, _ = f.Write([]byte("ok"))
			_ = f.Close()
			Expect(fs.MarkComplete(data)).To(Succeed())

			Expect(fs.Remove(data)).To(Succeed())
			_, dataErr := os.Stat(data)
			Expect(os.IsNotExist(dataErr)).To(BeTrue())
			_, markErr := os.Stat(data + ".complete")
			Expect(os.IsNotExist(markErr)).To(BeTrue())
		})
	})

	Describe("Reload with a generation policy", func() {
		// After the migration sentinel exists, every marker is evaluated.
		makeCompleted := func(key, generation string) string {
			data := fs.KeyMapper(key)
			f, err := fs.Create(data)
			Expect(err).To(BeNil())
			_, _ = f.Write([]byte(key))
			Expect(f.Close()).To(Succeed())
			if generation == "" {
				Expect(fs.MarkComplete(data)).To(Succeed())
			} else {
				Expect(fs.MarkCompleteWith(data, generation)).To(Succeed())
			}
			return data
		}

		BeforeEach(func() {
			Expect(os.WriteFile(filepath.Join(rootDir, ".nd-migrated"), nil, 0600)).To(Succeed())
		})

		It("adopts only entries whose generation is committed and prunes the rest", func() {
			current := makeCompleted("current", "g-current")
			stale := makeCompleted("stale", "g-stale")
			unknown := makeCompleted("unknown", "")

			fs.SetAdoptionPolicy(func(payload string) AdoptionDecision {
				if payload == "g-current" {
					return AdoptCurrent
				}
				return PruneEntry
			})

			var adopted []string
			Expect(fs.Reload(func(key, name string) { adopted = append(adopted, name) })).To(Succeed())
			Expect(adopted).To(ConsistOf(current))

			_, err := os.Stat(stale)
			Expect(os.IsNotExist(err)).To(BeTrue())
			_, err = os.Stat(stale + ".complete")
			Expect(os.IsNotExist(err)).To(BeTrue())
			_, err = os.Stat(unknown)
			Expect(os.IsNotExist(err)).To(BeTrue())
		})

		It("retains entries on disk without adopting them when eligibility is undecided", func() {
			retained := makeCompleted("retained", "g-retained")
			current := makeCompleted("current", "g-current")

			fs.SetAdoptionPolicy(func(payload string) AdoptionDecision {
				if payload == "g-retained" {
					return RetainEntry
				}
				return AdoptCurrent
			})

			var adopted []string
			Expect(fs.Reload(func(key, name string) { adopted = append(adopted, name) })).To(Succeed())
			Expect(adopted).To(ConsistOf(current))
			_, err := os.Stat(retained)
			Expect(err).To(Succeed(), "retained entries stay on disk for the next startup")
		})

		It("adopts every marked entry when no policy is installed", func() {
			a := makeCompleted("a", "g1")
			b := makeCompleted("b", "g2")
			var adopted []string
			Expect(fs.Reload(func(key, name string) { adopted = append(adopted, name) })).To(Succeed())
			Expect(adopted).To(ConsistOf(a, b))
		})
	})

	Describe("Reload", func() {
		makeData := func(content string) string {
			file := fs.KeyMapper(content)
			f, err := fs.Create(file)
			Expect(err).To(BeNil())
			_, _ = f.Write([]byte(content))
			_ = f.Close()
			return file
		}

		It("migrates all existing files on first run and writes the sentinel", func() {
			for _, c := range []string{"aaaaa", "bbbbb", "ccccc"} {
				makeData(c) // no markers, simulating a pre-upgrade cache
			}

			var actual []string
			err := fs.Reload(func(key, name string) {
				Expect(key).To(Equal(name))
				data, _ := os.ReadFile(name)
				actual = append(actual, string(data))
			})
			Expect(err).To(BeNil())
			Expect(actual).To(ContainElements("aaaaa", "bbbbb", "ccccc"))
			Expect(actual).To(HaveLen(3))

			_, sentinelErr := os.Stat(filepath.Join(rootDir, ".nd-migrated"))
			Expect(sentinelErr).To(BeNil())
		})

		It("after migration, adopts only marked files and deletes unmarked partials", func() {
			// Pretend migration already happened.
			Expect(os.WriteFile(filepath.Join(rootDir, ".nd-migrated"), nil, 0600)).To(Succeed())

			good := makeData("good")
			Expect(fs.MarkComplete(good)).To(Succeed())
			bad := makeData("bad") // partial: no marker

			var actual []string
			err := fs.Reload(func(key, name string) { actual = append(actual, name) })
			Expect(err).To(BeNil())
			Expect(actual).To(ConsistOf(good))

			_, badErr := os.Stat(bad)
			Expect(os.IsNotExist(badErr)).To(BeTrue()) // partial deleted
		})

		It("ignores and cleans orphan markers", func() {
			Expect(os.WriteFile(filepath.Join(rootDir, ".nd-migrated"), nil, 0600)).To(Succeed())
			orphan := fs.KeyMapper("orphan") + ".complete"
			Expect(os.MkdirAll(filepath.Dir(orphan), 0755)).To(Succeed())
			Expect(os.WriteFile(orphan, nil, 0600)).To(Succeed())

			var actual []string
			err := fs.Reload(func(key, name string) { actual = append(actual, name) })
			Expect(err).To(BeNil())
			Expect(actual).To(BeEmpty())
			_, orphanErr := os.Stat(orphan)
			Expect(os.IsNotExist(orphanErr)).To(BeTrue())
		})
	})
})
