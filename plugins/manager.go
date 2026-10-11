package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Masterminds/squirrel"
	extism "github.com/extism/go-sdk"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/core/agents"
	"github.com/navidrome/navidrome/core/lyrics"
	"github.com/navidrome/navidrome/core/scrobbler"
	"github.com/navidrome/navidrome/core/sonic"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server/events"
	"github.com/navidrome/navidrome/utils/singleton"
	"github.com/rjeczalik/notify"
	"github.com/tetratelabs/wazero"
)

const (
	// defaultTimeout is the default timeout for plugin function calls
	defaultTimeout = 30 * time.Second

	// maxPluginLoadConcurrency is the maximum number of plugins that can be
	// compiled/loaded in parallel during startup
	maxPluginLoadConcurrency = 3
)

// SubsonicRouter is an http.Handler that serves Subsonic API requests.
type SubsonicRouter = http.Handler

// PluginMetricsRecorder is an interface for recording plugin metrics.
// This is satisfied by core/metrics.Metrics but defined here to avoid import cycles.
type PluginMetricsRecorder interface {
	RecordPluginRequest(ctx context.Context, plugin, method string, ok bool, elapsed int64)
}

// Manager manages loading and lifecycle of WebAssembly plugins.
// It implements both agents.PluginLoader and scrobbler.PluginLoader interfaces.
type Manager struct {
	mu      sync.RWMutex
	plugins map[string]*plugin
	ctx     context.Context //nolint:containedctx // manager lifecycle ctx, cancelled by Stop
	cancel  context.CancelFunc
	cache   wazero.CompilationCache
	stopped atomic.Bool    // Set to true when Stop() is called
	loadWg  sync.WaitGroup // Tracks in-flight plugin load operations

	// genSeq assigns commit generations to published instances. Guarded by mu.
	// Every successful publish increments it; reclaiming an old generation only
	// ever closes the exact pointer that publish replaced.
	genSeq uint64

	// opMu guards opLocks. opLocks serializes enable/disable/settings commits
	// per plugin ID, so a straggling save rebuilds on top of the newest
	// committed row and can never overwrite a newer success or close a newer
	// generation. Different plugins still proceed in parallel.
	opMu    sync.Mutex
	opLocks map[string]*sync.Mutex

	// File watcher fields (used when AutoReload is enabled)
	watcherEvents  chan notify.EventInfo
	watcherDone    chan struct{}
	debounceTimers map[string]*time.Timer
	debounceMu     sync.Mutex

	// transient is set by LoadPlugins, and nil for a server Start.
	transient *transientLoad

	// SubsonicAPI host function dependencies (set once before Start, not modified after)
	subsonicRouter SubsonicRouter
	ds             model.DataStore
	broker         events.Broker
	metrics        PluginMetricsRecorder
}

// GetManager returns a singleton instance of the plugin manager.
// The manager is not started automatically; call Start() to begin loading plugins.
func GetManager(ds model.DataStore, broker events.Broker, m PluginMetricsRecorder) *Manager {
	return singleton.GetInstance(func() *Manager {
		return &Manager{
			plugins: make(map[string]*plugin),
			opLocks: make(map[string]*sync.Mutex),
			ds:      ds,
			broker:  broker,
			metrics: m,
		}
	})
}

// pluginOpLock returns the mutex serializing mutating operations (enable,
// disable, settings commits) for a single plugin ID. Locks are created lazily
// so Managers constructed directly (tests, CLI) need no initialization.
func (m *Manager) pluginOpLock(id string) *sync.Mutex {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.opLocks == nil {
		m.opLocks = make(map[string]*sync.Mutex)
	}
	opLock, ok := m.opLocks[id]
	if !ok {
		opLock = &sync.Mutex{}
		m.opLocks[id] = opLock
	}
	return opLock
}

// activePlugin returns the currently published instance for id, or nil.
func (m *Manager) activePlugin(id string) *plugin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.plugins[id]
}

// sendPluginRefreshEvent broadcasts a refresh event for the plugin resource.
// This notifies connected UI clients that plugin data has changed.
func (m *Manager) sendPluginRefreshEvent(ctx context.Context, pluginIDs ...string) {
	if m.broker == nil {
		return
	}
	event := (&events.RefreshResource{}).With("plugin", pluginIDs...)
	m.broker.SendBroadcastMessage(ctx, event)
}

// SetSubsonicRouter sets the Subsonic router for SubsonicAPI host functions.
// This should be called after the subsonic router is created but before plugins
// that require SubsonicAPI access are loaded.
func (m *Manager) SetSubsonicRouter(router SubsonicRouter) {
	m.subsonicRouter = router
}

// Start initializes the plugin manager and loads plugins from the configured folder.
// It should be called once during application startup when plugins are enabled.
// The startup flow is:
// 1. Sync plugins folder with DB (discover new, update changed, remove deleted)
// 2. Load only enabled plugins from DB
func (m *Manager) Start(ctx context.Context) error {
	if !conf.Server.Plugins.Enabled {
		log.Debug(ctx, "Plugin system is disabled")
		return nil
	}

	if m.subsonicRouter == nil {
		log.Fatal(ctx, "Plugin manager requires the SubsonicAPI router to be configured")
	}

	cacheDir := filepath.Join(conf.Server.CacheFolder.MustPath(), "plugins")
	purgeCacheBySize(ctx, cacheDir, conf.Server.Plugins.CacheSize)

	if err := m.initRuntime(ctx, cacheDir); err != nil {
		return err
	}

	if conf.Server.Plugins.Folder.String() == "" {
		log.Debug(ctx, "No plugins folder configured")
		return nil
	}

	folder := conf.Server.Plugins.Folder.MustPath()

	log.Info(ctx, "Starting plugin manager", "folder", folder)

	// Clear previous error states so plugins can be retried on restart
	adminCtx := adminContext(ctx)
	if err := m.ds.Plugin().ClearErrors(adminCtx); err != nil {
		log.Error(ctx, "Error clearing plugin errors", err)
	}

	// Sync plugins folder with DB
	if err := m.syncPlugins(ctx, folder); err != nil {
		log.Error(ctx, "Error syncing plugins with DB", err)
		// Continue - we can still try to load plugins
	}

	// Load enabled plugins from DB
	if err := m.loadEnabledPlugins(ctx); err != nil {
		log.Error(ctx, "Error loading enabled plugins", err)
		return fmt.Errorf("loading enabled plugins: %w", err)
	}

	// Start file watcher if auto-reload is enabled
	if conf.Server.Plugins.AutoReload {
		if err := m.startWatcher(); err != nil {
			log.Error(ctx, "Failed to start plugin file watcher", err)
			// Non-fatal - plugins are still loaded, just no auto-reload
		}
	}

	return nil
}

// initRuntime prepares the extism/wazero runtime that instantiating a plugin needs.
func (m *Manager) initRuntime(ctx context.Context, cacheDir string) error {
	pluginLogLevel := conf.Server.Plugins.LogLevel
	if pluginLogLevel == "" {
		pluginLogLevel = conf.Server.LogLevel
	}
	extism.SetLogLevel(toExtismLogLevel(log.ParseLogLevel(pluginLogLevel)))

	m.ctx, m.cancel = context.WithCancel(ctx)

	var err error
	m.cache, err = wazero.NewCompilationCacheWithDir(cacheDir)
	if err != nil {
		log.Error(ctx, "Failed to create wazero compilation cache", err)
		return fmt.Errorf("creating wazero compilation cache: %w", err)
	}
	return nil
}

// transientLoad scopes a load that will not outlive the command asking for it; see LoadPlugins.
type transientLoad struct {
	only    []string
	runInit bool
}

// LoadPlugins loads the plugins named in only, so a CLI sees the agents a server would. Each is
// instantiated, creating any KVStore, TaskQueue or Storage it declares; call Stop when done.
func (m *Manager) LoadPlugins(ctx context.Context, only []string, runInit bool) error {
	if !conf.Server.Plugins.Enabled || conf.Server.Plugins.Folder.String() == "" || len(only) == 0 {
		return nil
	}
	m.transient = &transientLoad{only: only, runInit: runInit}

	cacheDir := filepath.Join(conf.Server.CacheFolder.MustPath(), "plugins")
	if err := m.initRuntime(ctx, cacheDir); err != nil {
		return err
	}
	if err := m.loadEnabledPlugins(ctx); err != nil {
		return fmt.Errorf("loading enabled plugins: %w", err)
	}
	return nil
}

// Stop shuts down the plugin manager and releases all resources.
func (m *Manager) Stop() error {
	// Mark as stopped first to prevent new operations
	m.stopped.Store(true)

	// Cancel context to signal all goroutines to stop
	if m.cancel != nil {
		m.cancel()
	}

	// Stop file watcher
	m.stopWatcher()

	// Wait for all in-flight plugin load operations to complete
	// This is critical to avoid races with cache.Close()
	m.loadWg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Close all plugins
	for name, plugin := range m.plugins {
		err := plugin.Close()
		if err != nil {
			log.Error("Error during plugin cleanup", "plugin", name, err)
		}
		if plugin.compiled != nil {
			if err := plugin.compiled.Close(context.Background()); err != nil {
				log.Error("Error closing plugin", "plugin", name, err)
			}
		}
	}
	m.plugins = make(map[string]*plugin)

	// Close compilation cache
	if m.cache != nil {
		if err := m.cache.Close(context.Background()); err != nil {
			log.Error("Error closing wazero cache", err)
		}
		m.cache = nil
	}

	return nil
}

// PluginNames returns the names of all plugins that implement a particular capability.
// This is used by both agents and scrobbler systems to discover available plugins.
// Capabilities are auto-detected from the plugin's exported functions.
func (m *Manager) PluginNames(capability string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var names []string
	cap := Capability(capability)
	for name, plugin := range m.plugins {
		if hasCapability(plugin.capabilities, cap) {
			names = append(names, name)
		}
	}
	return names
}

func (m *Manager) LoadMediaAgent(name string) (agents.Interface, bool) {
	return loadPlugin(m, name, CapabilityMetadataAgent, newMetadataAgent)
}

func (m *Manager) LoadScrobbler(name string) (scrobbler.Scrobbler, bool) {
	return loadPlugin(m, name, CapabilityScrobbler, newScrobblerPlugin)
}

func (m *Manager) LoadLyricsProvider(name string) (lyrics.Provider, bool) {
	return loadPlugin(m, name, CapabilityLyrics, newLyricsPlugin)
}

func (m *Manager) LoadSonicSimilarity(name string) (sonic.Provider, bool) {
	return loadPlugin(m, name, CapabilitySonicSimilarity, newSonicSimilarityPlugin)
}

func loadPlugin[T any](m *Manager, name string, cap Capability, newAdapter func(*plugin) T) (T, bool) {
	m.mu.RLock()
	p, ok := m.plugins[name]
	m.mu.RUnlock()

	var zero T
	if !ok || !hasCapability(p.capabilities, cap) {
		return zero, false
	}
	return newAdapter(p), true
}

// PluginInfo contains basic information about a plugin for metrics/insights.
type PluginInfo struct {
	Name    string
	Version string
}

// GetPluginInfo returns information about all loaded plugins.
func (m *Manager) GetPluginInfo() map[string]PluginInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	info := make(map[string]PluginInfo, len(m.plugins))
	for name, plugin := range m.plugins {
		info[name] = PluginInfo{
			Name:    plugin.manifest.Name,
			Version: plugin.manifest.Version,
		}
	}
	return info
}

// EnablePlugin enables a plugin by loading it and updating the DB.
// The candidate is fully built (Wasm, host services, init) before any state is
// committed; on failure nothing is published and the stored row keeps the
// plugin disabled with a diagnosable LastError.
func (m *Manager) EnablePlugin(ctx context.Context, id string) error {
	if m.ds == nil {
		return fmt.Errorf("datastore not configured")
	}

	// Serialize against concurrent saves/disable of the same plugin.
	opLock := m.pluginOpLock(id)
	opLock.Lock()
	defer opLock.Unlock()

	adminCtx := adminContext(ctx)
	repo := m.ds.Plugin()

	plugin, err := repo.Get(adminCtx, id)
	if err != nil {
		return fmt.Errorf("getting plugin from DB: %w", err)
	}

	if plugin.Enabled {
		return nil // Already enabled
	}

	// Check permission gates before enabling
	if err := m.checkPermissionGates(plugin); err != nil {
		return err
	}

	expected := m.activePlugin(id)

	candidate, err := m.buildPluginCandidate(plugin)
	if err != nil {
		plugin.LastError = err.Error()
		plugin.UpdatedAt = time.Now()
		_ = repo.Put(adminCtx, plugin)
		return fmt.Errorf("loading plugin: %w", err)
	}

	initCtx := log.NewContext(m.ctx, "plugin", id)
	// Match loadPluginWithConfig: a transient inspection that did not ask for
	// init must not run the plugin's arbitrary code.
	if m.transient == nil || m.transient.runInit {
		if err := callPluginInit(initCtx, candidate.p); err != nil {
			candidate.close(initCtx)
			plugin.LastError = err.Error()
			plugin.UpdatedAt = time.Now()
			_ = repo.Put(adminCtx, plugin)
			return fmt.Errorf("loading plugin: %w", err)
		}
	}

	// Commit DB row and active instance together; on failure the candidate is
	// cleaned up by commitCandidate and nothing is left running.
	plugin.Enabled = true
	plugin.UpdatedAt = time.Now()
	if err := m.commitCandidate(adminCtx, repo, plugin, candidate, expected); err != nil {
		return err
	}

	log.Info(ctx, "Enabled plugin", "plugin", id, "generation", candidate.p.generation)
	m.sendPluginRefreshEvent(ctx, id)
	return nil
}

// DisablePlugin disables a plugin by unloading it and updating the DB.
// Returns an error if the plugin is not found in DB.
func (m *Manager) DisablePlugin(ctx context.Context, id string) error {
	if m.ds == nil {
		return fmt.Errorf("datastore not configured")
	}

	opLock := m.pluginOpLock(id)
	opLock.Lock()
	defer opLock.Unlock()

	adminCtx := adminContext(ctx)
	repo := m.ds.Plugin()

	plugin, err := repo.Get(adminCtx, id)
	if err != nil {
		return fmt.Errorf("getting plugin from DB: %w", err)
	}

	if !plugin.Enabled {
		return nil // Already disabled
	}

	// Unload the plugin
	if err := m.unloadPlugin(id); err != nil {
		log.Debug(ctx, "Plugin was not loaded", "plugin", id)
	}

	// Update DB
	plugin.Enabled = false
	plugin.UpdatedAt = time.Now()
	if err := repo.Put(adminCtx, plugin); err != nil {
		return fmt.Errorf("updating plugin in DB: %w", err)
	}

	log.Info(ctx, "Disabled plugin", "plugin", id)
	m.sendPluginRefreshEvent(ctx, id)
	return nil
}

// ValidatePluginConfig validates a config JSON string against the plugin's config schema.
// If the plugin has no config schema defined, it returns an error.
// Returns nil if validation passes, or an error describing the validation failure.
func (m *Manager) ValidatePluginConfig(ctx context.Context, id, configJSON string) error {
	if m.ds == nil {
		return fmt.Errorf("datastore not configured")
	}

	adminCtx := adminContext(ctx)
	repo := m.ds.Plugin()

	plugin, err := repo.Get(adminCtx, id)
	if err != nil {
		return fmt.Errorf("getting plugin from DB: %w", err)
	}

	manifest, err := ReadManifest(plugin.Path)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}

	return ValidateConfig(manifest, configJSON)
}

// UpdatePluginConfig updates the configuration for a plugin.
// If the plugin is enabled, it will be reloaded with the new config.
func (m *Manager) UpdatePluginConfig(ctx context.Context, id, configJSON string) error {
	return m.updatePluginSettings(ctx, id, func(p *model.Plugin) {
		p.Config = configJSON
	})
}

// UpdatePluginUsers updates the users permission settings for a plugin.
// If the plugin is enabled, it will be reloaded with the new settings.
// If the plugin requires users permission and no users are configured (and allUsers is false),
// the plugin will be automatically disabled.
func (m *Manager) UpdatePluginUsers(ctx context.Context, id, usersJSON string, allUsers bool) error {
	return m.updatePluginSettings(ctx, id, func(p *model.Plugin) {
		p.Users = usersJSON
		p.AllUsers = allUsers
	})
}

// UpdatePluginLibraries updates the libraries permission settings for a plugin.
// If the plugin is enabled, it will be reloaded with the new settings.
// If the plugin requires library permission and no libraries are configured (and allLibraries is false),
// the plugin will be automatically disabled.
func (m *Manager) UpdatePluginLibraries(ctx context.Context, id, librariesJSON string, allLibraries, allowWriteAccess bool) error {
	return m.updatePluginSettings(ctx, id, func(p *model.Plugin) {
		p.Libraries = librariesJSON
		p.AllLibraries = allLibraries
		p.AllowWriteAccess = allowWriteAccess
	})
}

// RescanPlugins triggers a manual rescan of the plugins folder.
// This synchronizes the database with the filesystem, discovering new plugins,
// updating changed ones, and removing deleted ones.
func (m *Manager) RescanPlugins(ctx context.Context) error {
	folder := conf.Server.Plugins.Folder.String()
	if folder == "" {
		return fmt.Errorf("plugins folder not configured")
	}
	log.Info(ctx, "Manual plugin rescan requested", "folder", folder)
	return m.syncPlugins(ctx, folder)
}

// updatePluginSettings is a common implementation for updating plugin settings.
//
// Settings updates use a candidate commit: the new settings are applied to a
// private copy of the row, a full candidate is built with them (Wasm, all host
// services, init) and only after it proves runnable are the persisted row and
// the active instance swapped together, after which the replaced generation is
// reclaimed. A candidate failure (or a database failure) discards only the
// candidate: the last successfully committed settings stay in the database and
// keep serving via the old instance, and a diagnosable error is returned.
//
// A disabled plugin only stores the settings without starting. Removing a
// permission the plugin requires keeps the normal disable semantics.
func (m *Manager) updatePluginSettings(ctx context.Context, id string, updateFn func(*model.Plugin)) error {
	if m.ds == nil {
		return fmt.Errorf("datastore not configured")
	}

	// Serialize concurrent saves of the same plugin: the lock is held across
	// build and commit, so a request that arrives later reads the newest
	// committed row after acquiring it and can never overwrite a newer success.
	opLock := m.pluginOpLock(id)
	opLock.Lock()
	defer opLock.Unlock()

	adminCtx := adminContext(ctx)
	repo := m.ds.Plugin()

	plugin, err := repo.Get(adminCtx, id)
	if err != nil {
		return fmt.Errorf("getting plugin from DB: %w", err)
	}
	// Work on a private copy: a repository may hand back a pointer it still
	// owns, and a rejected save must not leave the in-memory row half-edited.
	rowCopy := *plugin
	plugin = &rowCopy

	wasEnabled := plugin.Enabled

	// Apply the specific updates to a private, not-yet-persisted copy
	updateFn(plugin)
	plugin.UpdatedAt = time.Now()

	// Check if plugin requires a permission that is no longer satisfied: normal disable semantics.
	if wasEnabled {
		if reason := permissionLossReason(plugin); reason != "" {
			// Disable the plugin since permission is no longer satisfied
			if err := m.unloadPlugin(id); err != nil {
				log.Debug(ctx, "Plugin was not loaded", "plugin", id)
			}
			plugin.Enabled = false
			if err := repo.Put(adminCtx, plugin); err != nil {
				return fmt.Errorf("updating plugin in DB: %w", err)
			}
			log.Info(ctx, "Disabled plugin due to "+reason, "plugin", id)
			m.sendPluginRefreshEvent(ctx, id)
			return nil
		}
	}

	// Disabled plugins only persist the new settings; they never start an instance.
	if !wasEnabled {
		if err := repo.Put(adminCtx, plugin); err != nil {
			return fmt.Errorf("updating plugin in DB: %w", err)
		}
		log.Info(ctx, "Updated plugin settings while disabled", "plugin", id)
		m.sendPluginRefreshEvent(ctx, id)
		return nil
	}

	// Enabled plugin: build and validate the whole candidate with the new
	// settings BEFORE touching the database or the active instance.
	expected := m.activePlugin(id)

	candidate, err := m.buildPluginCandidate(plugin)
	if err != nil {
		saveErr := fmt.Errorf("reloading plugin with new settings: %w", err)
		m.recordCandidateError(adminCtx, id, saveErr)
		return saveErr
	}

	initCtx := log.NewContext(m.ctx, "plugin", id)
	// Init belongs to the validated candidate; a transient inspection that did
	// not ask for init keeps skipping it (see loadPluginWithConfig).
	if m.transient == nil || m.transient.runInit {
		if err := callPluginInit(initCtx, candidate.p); err != nil {
			candidate.close(initCtx)
			saveErr := fmt.Errorf("reloading plugin with new settings: %w", err)
			m.recordCandidateError(adminCtx, id, saveErr)
			return saveErr
		}
	}

	// Commit the row and swap the active instance as one step. On any failure
	// commitCandidate discards the candidate and leaves the old instance and
	// row authoritative.
	if err := m.commitCandidate(adminCtx, repo, plugin, candidate, expected); err != nil {
		// A conflict means a newer operation already won; its result stands, so
		// don't overwrite that row's LastError. Surface the error for retry.
		if !errors.Is(err, errPluginGenerationConflict) {
			m.recordCandidateError(adminCtx, id, err)
		}
		return err
	}

	log.Info(ctx, "Updated plugin settings", "plugin", id, "generation", candidate.p.generation)
	m.sendPluginRefreshEvent(ctx, id)
	return nil
}

// permissionLossReason reports why an enabled plugin must be disabled after a
// settings change. It returns "" when every permission the manifest requires
// remains configured (or when the manifest cannot be read, which other flows
// surface as load errors).
func permissionLossReason(p *model.Plugin) string {
	manifest, err := ReadManifest(p.Path)
	if err != nil || manifest.Permissions == nil {
		return ""
	}
	if manifest.Permissions.Users != nil && !hasValidUsersConfig(p.Users, p.AllUsers) {
		return "users permission removal"
	}
	if manifest.Permissions.Library != nil && !hasValidLibrariesConfig(p.Libraries, p.AllLibraries) {
		return "library permission removal"
	}
	return ""
}

// recordCandidateError attaches a rejected save's diagnostic to the still
// committed row, re-reading it so the failed settings themselves are never
// persisted. The old configuration and enabled state are untouched and the old
// instance keeps serving; only LastError (plus UpdatedAt) changes.
func (m *Manager) recordCandidateError(ctx context.Context, id string, cause error) {
	repo := m.ds.Plugin()
	stored, err := repo.Get(ctx, id)
	if err != nil {
		log.Debug(ctx, "Could not reload plugin row to record settings error", "plugin", id, err)
		return
	}
	stored.LastError = cause.Error()
	stored.UpdatedAt = time.Now()
	if err := repo.Put(ctx, stored); err != nil {
		log.Error(ctx, "Failed to record plugin settings error", "plugin", id, err)
		return
	}
	m.sendPluginRefreshEvent(ctx, id)
}

// unloadPlugin removes a plugin from the manager and closes its resources.
// Returns an error if the plugin is not found.
func (m *Manager) unloadPlugin(name string) error {
	m.mu.Lock()
	plugin, ok := m.plugins[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("plugin %q not found", name)
	}
	delete(m.plugins, name)
	m.mu.Unlock()

	destroyPlugin(name, plugin)
	log.Info(m.ctx, "Unloaded plugin", "plugin", name, "generation", plugin.generation)
	return nil
}

// UnloadDisabledPlugins checks for plugins that are disabled in the database
// but still loaded in memory, and unloads them. This is called after user or
// library deletion to clean up plugins that were auto-disabled due to
// permission loss.
func (m *Manager) UnloadDisabledPlugins(ctx context.Context) {
	if m.ds == nil {
		return
	}

	adminCtx := adminContext(ctx)
	repo := m.ds.Plugin()

	// Get all disabled plugins from the database
	plugins, err := repo.GetAll(adminCtx, model.QueryOptions{
		Filters: squirrel.Eq{"enabled": false},
	})
	if err != nil {
		log.Error(ctx, "Failed to get disabled plugins", err)
		return
	}

	// Check each disabled plugin and unload if still in memory
	var unloaded []string
	for _, p := range plugins {
		m.mu.RLock()
		_, loaded := m.plugins[p.ID]
		m.mu.RUnlock()

		if loaded {
			if err := m.unloadPlugin(p.ID); err != nil {
				log.Warn(ctx, "Failed to unload disabled plugin", "plugin", p.ID, err)
			} else {
				unloaded = append(unloaded, p.ID)
				log.Info(ctx, "Unloaded disabled plugin", "plugin", p.ID)
			}
		}
	}

	// Send refresh events for unloaded plugins
	if len(unloaded) > 0 {
		m.sendPluginRefreshEvent(ctx, unloaded...)
	}
}

// checkPermissionGates validates that all permission-based requirements are met
// before a plugin can be enabled. Returns an error if any gate condition fails.
func (m *Manager) checkPermissionGates(p *model.Plugin) error {
	// Parse manifest to check permissions
	manifest, err := ReadManifest(p.Path)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}

	// Check users permission gate
	if manifest.Permissions != nil && manifest.Permissions.Users != nil {
		if !hasValidUsersConfig(p.Users, p.AllUsers) {
			return fmt.Errorf("users permission requires configuration: select users or enable 'all users' access")
		}
	}

	// Check library permission gate
	if manifest.Permissions != nil && manifest.Permissions.Library != nil {
		if !hasValidLibrariesConfig(p.Libraries, p.AllLibraries) {
			return fmt.Errorf("library permission requires configuration: select libraries or enable 'all libraries' access")
		}
	}

	return nil
}

// hasValidUsersConfig checks if a plugin has valid users configuration.
// Returns true if allUsers is true, or if usersJSON contains at least one user.
func hasValidUsersConfig(usersJSON string, allUsers bool) bool {
	if allUsers {
		return true
	}
	if usersJSON == "" {
		return false
	}
	var users []string
	if err := json.Unmarshal([]byte(usersJSON), &users); err != nil {
		return false
	}
	return len(users) > 0
}

// hasValidLibrariesConfig checks if a plugin has valid libraries configuration.
// Returns true if allLibraries is true, or if librariesJSON contains at least one library.
func hasValidLibrariesConfig(librariesJSON string, allLibraries bool) bool {
	if allLibraries {
		return true
	}
	if librariesJSON == "" {
		return false
	}
	var libraries []int
	if err := json.Unmarshal([]byte(librariesJSON), &libraries); err != nil {
		return false
	}
	return len(libraries) > 0
}
