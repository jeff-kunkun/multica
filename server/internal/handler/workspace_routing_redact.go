package handler

// Automatic routing is gone, but workspaces that configured it still carry a
// gateway key (plaintext `api_key` or sealed `api_key_enc`) in the `routing`
// settings block, and in its nested `analysis` block. The settings column is
// returned to clients verbatim, so these fields are stripped on every read and
// write: nothing reads them any more, and none of them may leave the server.
const (
	legacyRoutingSettingsKey = "routing"
	legacyRoutingAnalysisKey = "analysis"
	legacyRoutingPlainKey    = "api_key"
	legacyRoutingSealedKey   = "api_key_enc"
)

// redactRoutingSettings returns settings without the legacy routing key
// fields. It mutates nothing the caller owns.
func redactRoutingSettings(settings any) any {
	root, ok := settings.(map[string]any)
	if !ok {
		return settings
	}
	block, ok := root[legacyRoutingSettingsKey].(map[string]any)
	if !ok {
		return settings
	}
	analysis, hasAnalysis := block[legacyRoutingAnalysisKey].(map[string]any)
	if !hasKeyField(block) && !(hasAnalysis && hasKeyField(analysis)) {
		return settings
	}
	cleanBlock := withoutKeyFields(block)
	if hasAnalysis {
		cleanBlock[legacyRoutingAnalysisKey] = withoutKeyFields(analysis)
	}
	cleanRoot := make(map[string]any, len(root))
	for k, v := range root {
		cleanRoot[k] = v
	}
	cleanRoot[legacyRoutingSettingsKey] = cleanBlock
	return cleanRoot
}

func hasKeyField(block map[string]any) bool {
	_, plain := block[legacyRoutingPlainKey]
	_, sealed := block[legacyRoutingSealedKey]
	return plain || sealed
}

// withoutKeyFields copies a block minus both key fields.
func withoutKeyFields(block map[string]any) map[string]any {
	clean := make(map[string]any, len(block))
	for k, v := range block {
		if k == legacyRoutingSealedKey || k == legacyRoutingPlainKey {
			continue
		}
		clean[k] = v
	}
	return clean
}
