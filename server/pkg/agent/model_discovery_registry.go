package agent

// modelDiscoveryKind is how a runtime says its model list is found.
// Every runtime in SupportedTypes, and every built-in runtime identity, has
// exactly one entry in modelDiscoveryByProvider. Adding a runtime without an
// entry fails TestEveryRuntimeDeclaresModelDiscovery.
type modelDiscoveryKind int

const (
	// modelDiscoveryDedicated means ListModels already has a discoverer for
	// this runtime (a switch case, or BuiltinRuntime.ModelDiscovery). That
	// result is returned as-is. The fallback chain does not replace it, so
	// the runtimes that already discover models keep their current behavior.
	modelDiscoveryDedicated modelDiscoveryKind = iota
	// modelDiscoveryChain means there is no dedicated discoverer. ListModels
	// walks the shared chain: the endpoint named by this runtime's own
	// config, then a readonly list command registered for it, then manual
	// entry. A step that is not registered is skipped. A step that finds a
	// list — including a confirmed empty one — stops the walk.
	modelDiscoveryChain
	// modelDiscoveryManual means the runtime cannot be given a per-task model.
	// Reason is required. The picker shows "managed by the runtime" rather than
	// an empty catalog; ModelSelectionSupported must agree.
	modelDiscoveryManual
)

type modelDiscoveryDecl struct {
	Kind   modelDiscoveryKind
	Reason string
}

// modelDiscoveryByProvider is the declaration table. Dedicated runtimes are
// still looked up by the switch in ListModels. Chain and manual runtimes, and
// any provider this table does not know, go through walkModelDiscoveryChain.
// A runtime added to SupportedTypes or BuiltinRuntimes without an entry fails
// TestEveryRuntimeDeclaresModelDiscovery instead of shipping as a silent
// empty catalog.
var modelDiscoveryByProvider = map[string]modelDiscoveryDecl{
	"claude":      {Kind: modelDiscoveryDedicated},
	"codebuddy":   {Kind: modelDiscoveryDedicated},
	"codex":       {Kind: modelDiscoveryDedicated},
	"copilot":     {Kind: modelDiscoveryDedicated},
	"opencode":    {Kind: modelDiscoveryDedicated},
	"codearts":    {Kind: modelDiscoveryDedicated},
	"deveco":      {Kind: modelDiscoveryDedicated},
	"openclaw":    {Kind: modelDiscoveryDedicated},
	"hermes":      {Kind: modelDiscoveryDedicated},
	"pi":          {Kind: modelDiscoveryDedicated},
	"cursor":      {Kind: modelDiscoveryDedicated},
	"kimi":        {Kind: modelDiscoveryDedicated},
	"reasonix":    {Kind: modelDiscoveryDedicated},
	"dsh":         {Kind: modelDiscoveryDedicated},
	"kiro":        {Kind: modelDiscoveryDedicated},
	"antigravity": {Kind: modelDiscoveryDedicated},
	"qoder":       {Kind: modelDiscoveryDedicated},
	"qoderclicn":  {Kind: modelDiscoveryDedicated},
	"traecli":     {Kind: modelDiscoveryDedicated},
	"grok":        {Kind: modelDiscoveryDedicated},
	"dim":         {Kind: modelDiscoveryDedicated},
	"devin":       {Kind: modelDiscoveryDedicated},
	"omp":         {Kind: modelDiscoveryDedicated},
	"qwen":        {Kind: modelDiscoveryChain},
	"qwenpaw":     {Kind: modelDiscoveryManual, Reason: "session/set_model writes the shared agent profile, not the task"},
	"mcode":       {Kind: modelDiscoveryManual, Reason: "MCode ACP exposes no session-scoped model selection"},
	"zeroclaw":    {Kind: modelDiscoveryManual, Reason: "ZeroClaw has no session/set_model; the model comes from its agent profile"},
}

// undeclaredModelDiscovery returns the ids that have no discovery declaration,
// in the order given. A new runtime added to SupportedTypes or BuiltinRuntimes
// shows up here until someone records how its models are found.
func undeclaredModelDiscovery(ids []string) []string {
	var missing []string
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, ok := modelDiscoveryByProvider[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}
