package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"
	"time"

	extism "github.com/extism/go-sdk"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/plugins/host"
	"github.com/navidrome/navidrome/scheduler"
	"github.com/tetratelabs/wazero"
	"golang.org/x/sync/errgroup"
)

// serviceContext provides dependencies needed by host service factories.
type serviceContext struct {
	pluginName       string
	manager          *Manager
	permissions      *Permissions
	config           map[string]string
	allowedUsers     []string // User IDs this plugin can access
	allUsers         bool     // If true, plugin can access all users
	allowedLibraries []int    // Library IDs this plugin can access
	allLibraries     bool     // If true, plugin can access all libraries
}

// baseCtx returns the manager's lifecycle context, for host services that
// outlive the plugin call that created them. It falls back to
// context.Background() when the manager was never started, which is the case
// for CLI commands (e.g. `navidrome plugin enable`) that load plugins without
// calling Start.
func (c *serviceContext) baseCtx() context.Context {
	if c.manager.ctx == nil {
		return context.Background()
	}
	return c.manager.ctx
}

// hostServiceEntry defines a host service for table-driven registration.
type hostServiceEntry struct {
	name          string
	hasPermission func(*Permissions) bool
	create        func(*serviceContext) ([]extism.HostFunction, io.Closer, error)
}

// hostServices defines all available host services.
// Adding a new host service only requires adding an entry here.
var hostServices = []hostServiceEntry{
	{
		name:          "Config",
		hasPermission: func(p *Permissions) bool { return true }, // Always available, no permission required
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newConfigService(ctx.pluginName, ctx.config)
			return host.RegisterConfigHostFunctions(service), nil, nil
		},
	},
	{
		name:          "SubsonicAPI",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Subsonicapi != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newSubsonicAPIService(ctx.pluginName, ctx.manager.subsonicRouter, ctx.manager.ds, newUserAccess(ctx.allowedUsers, ctx.allUsers))
			return host.RegisterSubsonicAPIHostFunctions(service), nil, nil
		},
	},
	{
		name:          "Scheduler",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Scheduler != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newSchedulerService(ctx.pluginName, ctx.manager, scheduler.GetInstance())
			return host.RegisterSchedulerHostFunctions(service), service, nil
		},
	},
	{
		name:          "WebSocket",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Websocket != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			perm := ctx.permissions.Websocket
			service := newWebSocketService(ctx.baseCtx(), ctx.pluginName, ctx.manager, perm)
			return host.RegisterWebSocketHostFunctions(service), service, nil
		},
	},
	{
		name:          "Artwork",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Artwork != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newArtworkService()
			return host.RegisterArtworkHostFunctions(service), nil, nil
		},
	},
	{
		name:          "Cache",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Cache != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newCacheService(ctx.pluginName)
			return host.RegisterCacheHostFunctions(service), service, nil
		},
	},
	{
		name:          "Library",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Library != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			perm := ctx.permissions.Library
			service := newLibraryService(ctx.manager.ds, perm, ctx.allowedLibraries, ctx.allLibraries)
			return host.RegisterLibraryHostFunctions(service), nil, nil
		},
	},
	{
		name:          "KVStore",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Kvstore != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			perm := ctx.permissions.Kvstore
			service, err := newKVStoreService(ctx.baseCtx(), ctx.pluginName, perm)
			if err != nil {
				return nil, nil, err
			}
			return host.RegisterKVStoreHostFunctions(service), service, nil
		},
	},
	{
		name:          "Users",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Users != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newUsersService(ctx.manager.ds, ctx.allowedUsers, ctx.allUsers)
			return host.RegisterUsersHostFunctions(service), nil, nil
		},
	},
	{
		name:          "Matcher",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Matcher != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			hasFilesystemPerm := ctx.permissions.Library != nil && ctx.permissions.Library.Filesystem
			service := newMatcherService(
				ctx.manager.ds, hasFilesystemPerm,
				newUserAccess(ctx.allowedUsers, ctx.allUsers),
				newLibraryAccess(ctx.allowedLibraries, ctx.allLibraries),
			)
			return host.RegisterMatcherHostFunctions(service), nil, nil
		},
	},
	{
		name:          "HTTP",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Http != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			perm := ctx.permissions.Http
			service := newHTTPService(ctx.pluginName, perm)
			return host.RegisterHTTPHostFunctions(service), service, nil
		},
	},
	{
		name:          "Task",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Taskqueue != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			perm := ctx.permissions.Taskqueue
			maxConcurrency := int32(1)
			if perm.MaxConcurrency > 0 {
				maxConcurrency = int32(perm.MaxConcurrency)
			}
			service, err := newTaskQueueService(ctx.baseCtx(), ctx.pluginName, ctx.manager, maxConcurrency)
			if err != nil {
				return nil, nil, err
			}
			return host.RegisterTaskHostFunctions(service), service, nil
		},
	},
	{
		name:          "Storage",
		hasPermission: func(p *Permissions) bool { return p != nil && p.Storage != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service, err := newStorageService(ctx.pluginName)
			if err != nil {
				return nil, nil, err
			}
			return host.RegisterStorageHostFunctions(service), nil, nil
		},
	},
	{
		name:          "ScrobbleRetriever",
		hasPermission: func(p *Permissions) bool { return p != nil && p.ScrobbleRetriever != nil },
		create: func(ctx *serviceContext) ([]extism.HostFunction, io.Closer, error) {
			service := newScrobbleRetrieverService(ctx.manager.ds, newUserAccess(ctx.allowedUsers, ctx.allUsers))
			return host.RegisterScrobbleRetrieverHostFunctions(service), nil, nil
		},
	},
}

// extractManifest reads manifest from an .ndp package and computes its SHA-256 hash.
// This is a lightweight operation used for plugin discovery and change detection.
// Unlike the old implementation, this does NOT compile the wasm - just reads the manifest JSON.
func (m *Manager) extractManifest(ndpPath string) (*PluginMetadata, error) {
	if m.stopped.Load() {
		return nil, fmt.Errorf("manager is stopped")
	}

	manifest, err := ReadManifest(ndpPath)
	if err != nil {
		return nil, err
	}

	sha256Hash, err := ComputeFileSHA256(ndpPath)
	if err != nil {
		return nil, fmt.Errorf("computing hash: %w", err)
	}

	return &PluginMetadata{
		Manifest: manifest,
		SHA256:   sha256Hash,
	}, nil
}

// loadEnabledPlugins loads all enabled plugins from the database.
func (m *Manager) loadEnabledPlugins(ctx context.Context) error {
	if m.ds == nil {
		return fmt.Errorf("datastore not configured")
	}

	adminCtx := adminContext(ctx)
	repo := m.ds.Plugin()

	plugins, err := repo.GetAll(adminCtx)
	if err != nil {
		return fmt.Errorf("reading plugins from DB: %w", err)
	}

	g := errgroup.Group{}
	g.SetLimit(maxPluginLoadConcurrency)

	for _, p := range plugins {
		if !p.Enabled {
			continue
		}
		// Instantiating a plugin creates its host services, so a transient load takes only the
		// ones it may actually consult.
		if m.transient != nil && !slices.Contains(m.transient.only, p.ID) {
			continue
		}

		plugin := p // Capture for goroutine
		g.Go(func() error {
			start := time.Now()
			log.Debug(ctx, "Loading enabled plugin", "plugin", plugin.ID, "path", plugin.Path)

			// Panic recovery
			defer func() {
				if r := recover(); r != nil {
					log.Error(ctx, "Panic while loading plugin", "plugin", plugin.ID, "panic", r)
				}
			}()

			if err := m.loadPluginWithConfig(&plugin); err != nil {
				// A transient load must not disable the user's plugin just for looking at it.
				if m.transient == nil {
					plugin.LastError = err.Error()
					plugin.Enabled = false
					plugin.UpdatedAt = time.Now()
					if putErr := repo.Put(adminCtx, &plugin); putErr != nil {
						log.Error(ctx, "Failed to update plugin error in DB", "plugin", plugin.ID, putErr)
					}
				}
				log.Error(ctx, "Failed to load plugin", "plugin", plugin.ID, err)
				return nil
			}

			// Clear any previous error
			if plugin.LastError != "" && m.transient == nil {
				plugin.LastError = ""
				plugin.UpdatedAt = time.Now()
				if putErr := repo.Put(adminCtx, &plugin); putErr != nil {
					log.Error(ctx, "Failed to clear plugin error in DB", "plugin", plugin.ID, putErr)
				}
			}

			m.mu.RLock()
			loadedPlugin := m.plugins[plugin.ID]
			m.mu.RUnlock()
			if loadedPlugin != nil {
				log.Info(ctx, "Loaded plugin", "plugin", plugin.ID, "manifest", loadedPlugin.manifest.Name,
					"capabilities", loadedPlugin.capabilities, "duration", time.Since(start))
			}
			return nil
		})
	}

	return g.Wait()
}

// loadPluginWithConfig builds a plugin from its stored configuration, runs its
// init hook, and publishes it as the active instance, replacing any instance
// already published for the same ID. Startup/CLI callers use it directly.
//
// Settings saves do NOT use it: they go through buildPluginCandidate →
// callPluginInit → commitCandidate, so a candidate that cannot compile, whose
// host services cannot be created, or whose init fails is discarded without
// disturbing the running instance or persisting the unusable settings.
func (m *Manager) loadPluginWithConfig(p *model.Plugin) error {
	candidate, err := m.buildPluginCandidate(p)
	if err != nil {
		return err
	}

	ctx := log.NewContext(m.ctx, "plugin", p.ID)
	// Init is the plugin's first chance to run arbitrary code: open sockets,
	// create task queues, schedule work. Only a caller that already intends to
	// reach the network asks for it. A failure invalidates the whole candidate.
	// Use the local: startup loads run concurrently, so reading the map back
	// here would race the writes.
	if m.transient == nil || m.transient.runInit {
		if err := callPluginInit(ctx, candidate.p); err != nil {
			candidate.close(ctx)
			return err
		}
	}

	m.publishPlugin(ctx, candidate)
	return nil
}

// pluginCandidate is a fully built plugin instance — compiled Wasm plus every
// host service its permissions grant — that has NOT been published. It owns
// its resources until they are either committed (publishPlugin /
// commitCandidate) or released by close. A rejected commit must call close.
type pluginCandidate struct {
	id string
	p  *plugin
}

// close releases every resource owned by a candidate that was never published.
// It must not be called after a successful commit; those resources belong to
// the active instance or to the generation being reclaimed.
func (c *pluginCandidate) close(ctx context.Context) {
	if c == nil || c.p == nil {
		return
	}
	destroyPlugin(c.id, c.p)
	log.Debug(ctx, "Discarded plugin candidate", "plugin", c.id)
}

// buildPluginCandidate compiles the Wasm for p, creates all host services its
// permissions grant, detects capabilities and validates the manifest against
// them, without registering anything in the manager and without touching the
// database. The returned candidate must be committed or closed. On any failure
// all resources accumulated for it are released and the active instance is
// never affected.
func (m *Manager) buildPluginCandidate(p *model.Plugin) (_ *pluginCandidate, err error) {
	// NewContext falls back to context.Background() when m.ctx is nil (unstarted manager)
	ctx := log.NewContext(m.ctx, "plugin", p.ID)

	if m.stopped.Load() {
		return nil, fmt.Errorf("manager is stopped")
	}

	if !validPluginID(p.ID) {
		return nil, fmt.Errorf("invalid plugin ID %q", p.ID)
	}

	// Track this operation
	m.loadWg.Add(1)
	defer m.loadWg.Done()

	if m.stopped.Load() {
		return nil, fmt.Errorf("manager is stopped")
	}

	// Parse config from JSON
	pluginConfig, err := parsePluginConfig(p.Config)
	if err != nil {
		return nil, err
	}

	// Parse users from JSON
	var allowedUsers []string
	if p.Users != "" {
		if err := json.Unmarshal([]byte(p.Users), &allowedUsers); err != nil {
			return nil, fmt.Errorf("parsing plugin users: %w", err)
		}
	}

	// Parse libraries from JSON
	var allowedLibraries []int
	if p.Libraries != "" {
		if err := json.Unmarshal([]byte(p.Libraries), &allowedLibraries); err != nil {
			return nil, fmt.Errorf("parsing plugin libraries: %w", err)
		}
	}

	// Open the .ndp package to get manifest and wasm bytes
	pkg, err := openPackage(p.Path)
	if err != nil {
		return nil, fmt.Errorf("opening package: %w", err)
	}

	pluginManifest := buildExtismManifest(pkg, pluginConfig)

	// Configure filesystem access for library permission, applied per instance
	var fsConfig wazero.FSConfig
	if pkg.Manifest.HasLibraryFilesystemPermission() || pkg.Manifest.HasStoragePermission() {
		mounts := []mount{}

		if pkg.Manifest.HasLibraryFilesystemPermission() {
			adminCtx := adminContext(ctx)
			libraries, err := m.ds.Library().GetAll(adminCtx)
			if err != nil {
				return nil, fmt.Errorf("failed to get libraries for filesystem access: %w", err)
			}
			mounts = buildMounts(ctx, libraries, allowedLibraries, p.AllLibraries, p.AllowWriteAccess)
		}

		if pkg.Manifest.HasStoragePermission() {
			pluginStore := getHostStoragePath(p.ID)
			log.Info(ctx, "Granting read-write filesystem access to plugin storage", "path", pluginStore, "id", p.ID)

			mounts = append(mounts, mount{
				hostPath:  pluginStore,
				guestPath: storageMount,
			})
		}

		fsConfig = buildFSConfig(mounts)
	}

	// Build host functions based on permissions from manifest. Resources are owned by the
	// candidate on success; the deferred cleanup below releases them on any failure, along
	// with the compiled module once it exists, so a rejected candidate leaks nothing.
	var hostFunctions []extism.HostFunction
	var closers []io.Closer
	var compiled *extism.CompiledPlugin
	committed := false
	defer func() {
		if committed {
			return
		}
		closeAll(closers)
		if compiled != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if closeErr := compiled.Close(closeCtx); closeErr != nil {
				log.Error(ctx, "Error closing compiled candidate", "plugin", p.ID, closeErr)
			}
		}
		runtime.GC()
	}()

	svcCtx := &serviceContext{
		pluginName:       p.ID,
		manager:          m,
		permissions:      pkg.Manifest.Permissions,
		config:           pluginConfig,
		allowedUsers:     allowedUsers,
		allUsers:         p.AllUsers,
		allowedLibraries: allowedLibraries,
		allLibraries:     p.AllLibraries,
	}
	for _, entry := range hostServices {
		if entry.hasPermission(pkg.Manifest.Permissions) {
			funcs, closer, err := entry.create(svcCtx)
			if err != nil {
				return nil, fmt.Errorf("creating %s service: %w", entry.name, err)
			}
			hostFunctions = append(hostFunctions, funcs...)
			if closer != nil {
				closers = append(closers, closer)
			}
		}
	}

	// Compile the plugin with all host functions
	runtimeConfig := wazero.NewRuntimeConfig().
		WithCompilationCache(m.cache).
		WithCloseOnContextDone(true)

	extismConfig := extism.PluginConfig{
		EnableWasi:                true,
		RuntimeConfig:             runtimeConfig,
		EnableHttpResponseHeaders: true,
	}
	compiled, err = extism.NewCompiledPlugin(ctx, pluginManifest, extismConfig, hostFunctions)
	if err != nil {
		return nil, fmt.Errorf("compiling plugin: %w", err)
	}

	// Create instance to detect capabilities
	instance, err := compiled.Instance(ctx, instanceConfig(fsConfig))
	if err != nil {
		return nil, fmt.Errorf("creating instance: %w", err)
	}
	instance.SetLogger(extismLogger(p.ID))
	capabilities := detectCapabilities(instance)
	instance.Close(ctx)

	// Validate manifest against detected capabilities
	if err := ValidateWithCapabilities(pkg.Manifest, capabilities); err != nil {
		return nil, fmt.Errorf("manifest validation: %w", err)
	}

	loadedPlugin := &plugin{
		name:           p.ID,
		path:           p.Path,
		manifest:       pkg.Manifest,
		compiled:       compiled,
		capabilities:   capabilities,
		closers:        closers,
		metrics:        m.metrics,
		allowedUserIDs: allowedUsers,
		allUsers:       p.AllUsers,
		libraries:      newLibraryAccess(allowedLibraries, p.AllLibraries),
		fsConfig:       fsConfig,
		lyricsSem:      make(chan struct{}, maxConcurrentLyricsCalls),
	}
	committed = true
	return &pluginCandidate{id: p.ID, p: loadedPlugin}, nil
}

// errPluginGenerationConflict means the active instance for a plugin changed
// while a candidate was being built (file rescan, unload, disable, shutdown).
// The save must be retried against the current state instead of being forced
// over the newer committed generation.
var errPluginGenerationConflict = errors.New("plugin state changed while saving settings")

// publishPlugin installs candidate as the single active instance for its ID
// under the manager lock, assigns the next generation, and fully reclaims the
// generation it replaced. Startup loads (row already persisted) call it
// directly; settings saves must go through commitCandidate so the database row
// and the active instance switch together.
func (m *Manager) publishPlugin(ctx context.Context, candidate *pluginCandidate) *plugin {
	m.mu.Lock()
	m.genSeq++
	candidate.p.generation = m.genSeq
	old := m.plugins[candidate.id]
	m.plugins[candidate.id] = candidate.p
	m.mu.Unlock()

	if old != nil {
		destroyPlugin(candidate.id, old)
		log.Debug(ctx, "Reclaimed superseded plugin generation",
			"plugin", candidate.id, "oldGeneration", old.generation, "newGeneration", candidate.p.generation)
	}
	return old
}

// commitCandidate makes a validated candidate the active instance together
// with its persisted row. Under the manager lock it first verifies that the
// active instance is still the exact one (expected) the candidate was built
// against, then upserts row and swaps the map entry; only then is the lock
// released and the replaced generation reclaimed. A generation mismatch or a
// database failure destroys the candidate and leaves the previous instance and
// row serving, returning a diagnosable error.
func (m *Manager) commitCandidate(ctx context.Context, repo model.PluginRepository, row *model.Plugin, candidate *pluginCandidate, expected *plugin) error {
	m.mu.Lock()
	if m.plugins[row.ID] != expected {
		m.mu.Unlock()
		candidate.close(ctx)
		return fmt.Errorf("%w for %q: a newer instance is active, retry the save", errPluginGenerationConflict, row.ID)
	}

	row.LastError = ""
	if err := repo.Put(ctx, row); err != nil {
		m.mu.Unlock()
		candidate.close(ctx)
		return fmt.Errorf("updating plugin in DB: %w", err)
	}

	m.genSeq++
	candidate.p.generation = m.genSeq
	m.plugins[row.ID] = candidate.p
	old := expected
	m.mu.Unlock()

	if old != nil {
		destroyPlugin(row.ID, old)
		log.Debug(ctx, "Reclaimed superseded plugin generation after settings commit",
			"plugin", row.ID, "oldGeneration", old.generation, "newGeneration", candidate.p.generation)
	}
	return nil
}

// destroyPlugin releases the host services and compiled Wasm of a plugin
// instance. Callers always pass the exact instance they detached (or never
// published), so a slow request can never close a newer generation than the
// one it replaced.
func destroyPlugin(name string, p *plugin) {
	if err := p.Close(); err != nil {
		log.Error("Error during plugin cleanup", "plugin", name, err)
	}

	if p.compiled != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.compiled.Close(ctx); err != nil {
			log.Error("Error closing plugin", "plugin", name, err)
		}
	}

	runtime.GC()
}

// closeAll closes host service closers accumulated before a load failure,
// so partially-created services don't leak goroutines or file handles.
func closeAll(closers []io.Closer) {
	for _, c := range closers {
		_ = c.Close()
	}
}

// parsePluginConfig parses a JSON config string into a map of string values.
// For Extism, all config values must be strings, so non-string values are serialized as JSON.
func parsePluginConfig(configJSON string) (map[string]string, error) {
	if configJSON == "" {
		return nil, nil
	}
	var rawConfig map[string]any
	if err := json.Unmarshal([]byte(configJSON), &rawConfig); err != nil {
		return nil, fmt.Errorf("parsing plugin config: %w", err)
	}
	pluginConfig := make(map[string]string)
	for key, value := range rawConfig {
		switch v := value.(type) {
		case string:
			pluginConfig[key] = v
		default:
			// Serialize non-string values as JSON
			jsonBytes, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("serializing config value %q: %w", key, err)
			}
			pluginConfig[key] = string(jsonBytes)
		}
	}
	return pluginConfig, nil
}

// buildExtismManifest describes the plugin to extism. It must never set AllowedPaths (extism would replace our
// jailed FSConfig) nor AllowedHosts (extism's http_request has no SSRF guard; plugins must use host.HTTPSend).
func buildExtismManifest(pkg *ndpPackage, pluginConfig map[string]string) extism.Manifest {
	return extism.Manifest{
		Wasm:    []extism.Wasm{extism.WasmData{Data: pkg.WasmBytes, Name: "main"}},
		Config:  pluginConfig,
		Timeout: uint64(defaultTimeout.Milliseconds()),
	}
}
