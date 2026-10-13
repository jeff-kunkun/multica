package featureflags

import (
	"context"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

const (
	// LocalSearchIndex gates the sync endpoints behind Web/Desktop local search
	// (MUL-7754). On by default. Turning it off (FF_LOCAL_SEARCH_INDEX=off)
	// makes those endpoints answer 404, so clients stop syncing and search
	// through the server again without a client release.
	LocalSearchIndex = "local_search_index"
	// resourceLabelsCompat is no longer a release flag. Keep publishing the key
	// as enabled for installed desktop clients from v0.4.0 through at least
	// v0.4.15, every release shipped before this change. The resource-label gate
	// remained in every such client and fails closed (default false) if the key
	// stops being published.
	resourceLabelsCompat = "settings_resource_labels"
)

var frontendPublicFlags = []string{}

func LocalSearchIndexEnabled(ctx context.Context, flags *featureflag.Service) bool {
	return flags.IsEnabled(ctx, LocalSearchIndex, true)
}

func EvaluateFrontendPublicFlags(ctx context.Context, flags *featureflag.Service) map[string]bool {
	out := make(map[string]bool, len(frontendPublicFlags)+1)
	for _, key := range frontendPublicFlags {
		out[key] = flags.IsEnabled(ctx, key, false)
	}
	out[resourceLabelsCompat] = true
	return out
}
