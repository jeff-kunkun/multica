package agent

import (
	"context"
	"strings"
	"time"
)

// modelListCommandTimeout bounds one registered readonly list command.
// 15s matches the pi and opencode discovery caps: long enough for a CLI that
// reads a local config, short enough that a hung command does not pin the
// picker.
const modelListCommandTimeout = 15 * time.Second

// modelEndpointSource reads the base URL, key, and default model id from a
// runtime's own config. The key is for the local probe only.
type modelEndpointSource func(ctx context.Context) (baseURL, apiKey, defaultModel string, err error)

// modelListCommand is a readonly catalog command a runtime has explicitly
// registered. The chain never invents args or a parser: a runtime absent
// from modelListCommands is not probed with `models` or `--list-models`.
type modelListCommand struct {
	Args  []string
	Parse func([]byte) ([]Model, error)
}

// modelEndpointSources are the runtimes whose fallback step is
// GET {base}/models against the endpoint their own config names.
// Qwen Code is the first. A runtime that is not here has no endpoint step.
var modelEndpointSources = map[string]modelEndpointSource{
	"qwen": qwenModelEndpoint,
}

// modelListCommands are the readonly list commands the chain is allowed to
// run. Empty until a runtime registers one. Blindly trying `models` or
// `--list-models` is how omp's discovery used to fail: the flag is not
// universal, and a wrong command is worse than leaving the field manual.
var modelListCommands = map[string]modelListCommand{}

// walkModelDiscoveryChain tries the registered steps in order and stops at
// the first one that obtains a list. A confirmed empty list is an answer and
// stops the walk. A step that is not registered is skipped. A step that fails
// is remembered and the next registered step is tried; when a later step
// obtains a list, that list wins, and when none do, the first failure is
// returned so the picker can say the list is temporarily unavailable.
//
// Nothing here is cached. A tunnel that is down must not be remembered as an
// empty catalog, and a successful read must not hide the next outage: the
// endpoint probe is one HTTP GET, and the caller refreshes to see the tunnel
// as it is now.
func walkModelDiscoveryChain(
	ctx context.Context,
	providerType string,
	runtimeCmd Command,
	sources map[string]modelEndpointSource,
	commands map[string]modelListCommand,
) (Catalog, error) {
	var firstFail error
	if source, ok := sources[providerType]; ok && source != nil {
		baseURL, apiKey, defaultModel, err := source(ctx)
		if err != nil {
			firstFail = err
		} else {
			models, err := discoverCompatibleEndpointModels(ctx, baseURL, apiKey, defaultModel)
			if err != nil {
				firstFail = err
			} else {
				return Catalog{Models: models}, nil
			}
		}
	}
	if cmd, ok := commands[providerType]; ok && listCommandRegistered(cmd) {
		models, err := runRegisteredModelListCommand(ctx, runtimeCmd, cmd)
		if err != nil {
			if firstFail != nil {
				return Catalog{}, firstFail
			}
			return Catalog{}, err
		}
		return Catalog{Models: models}, nil
	}
	if firstFail != nil {
		return Catalog{}, firstFail
	}
	return Catalog{}, errModelsListUnavailable("这个运行时没有登记发现方式")
}

func listCommandRegistered(cmd modelListCommand) bool {
	return len(cmd.Args) > 0 && cmd.Parse != nil
}

// runRegisteredModelListCommand runs a command the runtime registered as
// readonly. The exit status and the parser decide the outcome. Stdout is not
// copied into the error: a confused CLI can echo a config line, and that
// must not reach the picker.
func runRegisteredModelListCommand(ctx context.Context, runtimeCmd Command, cmd modelListCommand) ([]Model, error) {
	if strings.TrimSpace(runtimeCmd.Path) == "" {
		return nil, errModelsListUnavailable("没有可执行的列表命令")
	}
	runCtx, cancel := context.WithTimeout(ctx, modelListCommandTimeout)
	defer cancel()
	proc := runtimeCmd.exec(runCtx, cmd.Args...)
	hideAgentWindow(proc)
	stdout, err := outputOwned(proc, runtimeCmd.logger)
	if err != nil {
		return nil, errModelsListUnavailable("列表命令没有返回模型清单")
	}
	models, err := cmd.Parse(stdout)
	if err != nil {
		return nil, errModelsListUnavailable("列表命令没有返回模型清单")
	}
	return models, nil
}
