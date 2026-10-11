package plugins

import (
	"context"
	"fmt"

	"github.com/navidrome/navidrome/log"
)

// CapabilityLifecycle indicates the plugin has lifecycle callback functions.
// Detected when the plugin exports the nd_on_init function.
const CapabilityLifecycle Capability = "Lifecycle"

const FuncOnInit = "nd_on_init"

func init() {
	registerCapability(
		CapabilityLifecycle,
		FuncOnInit,
	)
}

// callPluginInit calls the plugin's nd_on_init function if it has the Lifecycle capability.
// This is run while the plugin is still an unpublished candidate, so an init error
// invalidates the candidate: the caller must not publish it and must close its resources.
// A nil error means the plugin either has no init hook or its hook completed successfully.
func callPluginInit(ctx context.Context, instance *plugin) error {
	if !hasCapability(instance.capabilities, CapabilityLifecycle) {
		return nil
	}

	log.Debug(ctx, "Calling plugin init function", "plugin", instance.name)

	if err := callPluginFunctionNoInput(ctx, instance, FuncOnInit); err != nil {
		return fmt.Errorf("plugin init function failed: %w", err)
	}

	log.Debug(ctx, "Plugin init function completed", "plugin", instance.name)
	return nil
}
