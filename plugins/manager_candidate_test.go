package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/navidrome/navidrome/core/agents"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// metadataBiography calls the running test-metadata-agent instance, proving the
// active generation is published and callable.
func metadataBiography(ctx context.Context, mgr *Manager, id string) (string, error) {
	a, ok := mgr.LoadMediaAgent(id)
	Expect(ok).To(BeTrue())
	bio, err := a.(agents.ArtistBiographyRetriever).GetArtistBiography(ctx, "artist-1", "Artist One", "")
	return bio, err
}

// failingPutPluginRepo wraps a plugin repo and can force every Put to fail,
// simulating a database outage at commit time.
type failingPutPluginRepo struct {
	model.PluginRepository
	failPuts atomic.Bool
}

func (r *failingPutPluginRepo) Put(ctx context.Context, p *model.Plugin) error {
	if r.failPuts.Load() {
		return errors.New("simulated database outage")
	}
	return r.PluginRepository.Put(ctx, p)
}

var _ = Describe("Candidate plugin settings commit", Ordered, func() {
	const id = "test-metadata-agent"

	var (
		ctx     context.Context
		manager *Manager
		repo    *tests.MockPluginRepo
		ndpPath string
	)

	BeforeEach(func() {
		ctx = GinkgoT().Context()
		manager, _ = createTestManagerWithPlugins(nil, id+PackageExtension)
		repo = manager.ds.Plugin().(*tests.MockPluginRepo)
		ndpPath = repo.Data[id].Path

		Expect(manager.PluginNames(string(CapabilityMetadataAgent))).To(ContainElement(id))
	})

	Describe("candidate build failure", func() {
		It("keeps the old instance and the last committed settings", func() {
			oldInstance := manager.activePlugin(id)
			Expect(oldInstance).ToNot(BeNil())
			oldGen := oldInstance.generation

			// Make any new candidate fail to open/compile while the old instance stays warm.
			Expect(os.WriteFile(ndpPath, []byte("not-an-ndp-package"), 0600)).To(Succeed())

			err := manager.UpdatePluginConfig(ctx, id, `{"region":"new"}`)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reloading plugin with new settings"))

			// The active instance is the exact same generation, still serving.
			Expect(manager.activePlugin(id)).To(BeIdenticalTo(oldInstance))
			Expect(manager.PluginNames(string(CapabilityMetadataAgent))).To(ContainElement(id))
			Expect(manager.activePlugin(id).generation).To(Equal(oldGen))

			bio, callErr := metadataBiography(ctx, manager, id)
			Expect(callErr).ToNot(HaveOccurred())
			Expect(bio).To(ContainSubstring("Biography for Artist"))

			// The unusable settings were never persisted; the row stays enabled with a diagnostic.
			stored, getErr := repo.Get(ctx, id)
			Expect(getErr).ToNot(HaveOccurred())
			Expect(stored.Enabled).To(BeTrue())
			Expect(stored.Config).To(BeEmpty())
			Expect(stored.LastError).To(ContainSubstring("reloading plugin with new settings"))
		})
	})

	Describe("database commit failure", func() {
		It("discards only the candidate and keeps the old generation serving", func() {
			oldInstance := manager.activePlugin(id)

			wrapped := &failingPutPluginRepo{PluginRepository: repo}
			wrapped.failPuts.Store(true)
			manager.ds = &tests.MockDataStore{MockedPlugin: wrapped}

			err := manager.UpdatePluginConfig(ctx, id, `{"region":"new"}`)
			Expect(err).To(MatchError(ContainSubstring("updating plugin in DB")))

			// No swap happened: old settings, old instance, single active generation.
			Expect(manager.activePlugin(id)).To(BeIdenticalTo(oldInstance))
			stored, getErr := repo.Get(ctx, id)
			Expect(getErr).ToNot(HaveOccurred())
			Expect(stored.Config).To(BeEmpty())
			Expect(stored.Enabled).To(BeTrue())

			manager.mu.RLock()
			Expect(manager.plugins).To(HaveLen(1))
			manager.mu.RUnlock()

			bio, callErr := metadataBiography(ctx, manager, id)
			Expect(callErr).ToNot(HaveOccurred())
			Expect(bio).To(ContainSubstring("Biography for Artist"))
		})
	})

	Describe("disabled plugin", func() {
		It("stores settings without starting an instance", func() {
			Expect(manager.DisablePlugin(ctx, id)).To(Succeed())
			Expect(manager.activePlugin(id)).To(BeNil())

			Expect(manager.UpdatePluginConfig(ctx, id, `{"region":"saved"}`)).To(Succeed())

			Expect(manager.PluginNames(string(CapabilityMetadataAgent))).ToNot(ContainElement(id))
			stored, err := repo.Get(ctx, id)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored.Enabled).To(BeFalse())
			Expect(stored.Config).To(Equal(`{"region":"saved"}`))
		})
	})

	Describe("successful commit", func() {
		It("publishes exactly one new generation and reclaims the old one", func() {
			oldInstance := manager.activePlugin(id)

			Expect(manager.UpdatePluginConfig(ctx, id, `{"region":"us"}`)).To(Succeed())

			newInstance := manager.activePlugin(id)
			Expect(newInstance).ToNot(BeIdenticalTo(oldInstance))
			Expect(newInstance.generation).To(BeNumerically(">", oldInstance.generation))

			manager.mu.RLock()
			Expect(manager.plugins).To(HaveLen(1))
			Expect(manager.plugins[id]).To(BeIdenticalTo(newInstance))
			manager.mu.RUnlock()

			stored, err := repo.Get(ctx, id)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored.Config).To(Equal(`{"region":"us"}`))
			Expect(stored.Enabled).To(BeTrue())
			Expect(stored.LastError).To(BeEmpty())

			// The new generation serves; the reclaimed one can no longer be reached by name.
			bio, callErr := metadataBiography(ctx, manager, id)
			Expect(callErr).ToNot(HaveOccurred())
			Expect(bio).To(ContainSubstring("Biography for Artist"))
		})
	})

	Describe("generation conflict", func() {
		It("refuses to commit when a newer generation won the race", func() {
			row, err := repo.Get(ctx, id)
			Expect(err).ToNot(HaveOccurred())
			expected := manager.activePlugin(id)

			candidate, err := manager.buildPluginCandidate(row)
			Expect(err).ToNot(HaveOccurred())

			// A concurrent operation (file rescan, disable...) changed the active generation.
			Expect(manager.unloadPlugin(id)).To(Succeed())

			commitErr := manager.commitCandidate(adminContext(ctx), repo, row, candidate, expected)
			Expect(commitErr).To(MatchError(ContainSubstring("state changed while saving settings")))
			Expect(manager.activePlugin(id)).To(BeNil())

			// The rejected row was never written.
			stored, getErr := repo.Get(ctx, id)
			Expect(getErr).ToNot(HaveOccurred())
			Expect(stored.Config).To(BeEmpty())
		})
	})

	Describe("concurrent saves of the same plugin", func() {
		It("serializes commits so a late save never closes the wrong generation", func() {
			const saves = 24

			initialGen := manager.activePlugin(id).generation

			var start sync.WaitGroup
			start.Add(1)
			var wg sync.WaitGroup
			var mu sync.Mutex
			gens := []uint64{}

			for i := range saves {
				wg.Add(1)
				go func(i int) {
					defer GinkgoRecover()
					defer wg.Done()
					start.Wait()
					cfg := fmt.Sprintf(`{"n":%d}`, i)
					err := manager.UpdatePluginConfig(ctx, id, cfg)
					Expect(err).ToNot(HaveOccurred())
					mu.Lock()
					gens = append(gens, manager.activePlugin(id).generation)
					mu.Unlock()
				}(i)
			}
			start.Done()
			wg.Wait()

			// Every save published one generation, in commit order, no duplicates or regressions.
			Expect(gens).To(HaveLen(saves))
			for i := 1; i < len(gens); i++ {
				Expect(gens[i]).To(BeNumerically(">", gens[i-1]))
			}
			Expect(gens[len(gens)-1]).To(Equal(initialGen + saves))

			// Exactly one active instance remains, matching the last committed row and still working.
			manager.mu.RLock()
			Expect(manager.plugins).To(HaveLen(1))
			active := manager.plugins[id]
			manager.mu.RUnlock()
			Expect(active.generation).To(Equal(initialGen + saves))

			stored, err := repo.Get(ctx, id)
			Expect(err).ToNot(HaveOccurred())
			Expect(stored.Enabled).To(BeTrue())
			Expect(stored.LastError).To(BeEmpty())
			Expect(stored.Config).To(MatchRegexp(`^\{"n":\d+\}$`))

			bio, callErr := metadataBiography(ctx, manager, id)
			Expect(callErr).ToNot(HaveOccurred())
			Expect(bio).To(ContainSubstring("Biography for Artist"))
		})
	})

	Describe("permission loss", func() {
		It("reports the disable reason from the manifest without instantiating", func() {
			usersPluginPath := filepath.Join(testdataDir, "test-users"+PackageExtension)

			row := &model.Plugin{Path: usersPluginPath, Users: "", AllUsers: false}
			Expect(permissionLossReason(row)).To(Equal("users permission removal"))

			row.Users = `["user-1"]`
			Expect(permissionLossReason(row)).To(BeEmpty())

			row.Users = ""
			row.AllUsers = true
			Expect(permissionLossReason(row)).To(BeEmpty())
		})
	})
})
