package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// settingsCmd is the AI-native escape hatch for workspace settings. Keys are
// intentionally small and stable; the server remains the source of truth for
// authorization and validation.
var settingsCmd = &cobra.Command{Use: "settings", Short: "Read and change workspace settings"}
var settingsGetCmd = &cobra.Command{Use: "get <key>", Short: "Read a setting", Args: exactArgs(1), RunE: runSettingsGet}
var settingsSetCmd = &cobra.Command{Use: "set <key>", Short: "Change a setting", Args: exactArgs(1), RunE: runSettingsSet}

func init() {
	settingsGetCmd.Flags().String("output", "json", "Output format: json or table")
	settingsGetCmd.Flags().String("value-json", "", "JSON query value (used by repo.shares; may contain url)")
	settingsSetCmd.Flags().String("value-json", "", "JSON value (use --value-file or --value-stdin for secrets)")
	settingsSetCmd.Flags().String("value-file", "", "Read the JSON value from a file")
	settingsSetCmd.Flags().Bool("value-stdin", false, "Read the JSON value from stdin")
	settingsSetCmd.Flags().String("output", "json", "Output format: json or table")
	settingsCmd.AddCommand(settingsGetCmd, settingsSetCmd)
}

func settingsPath(key string) (string, error) {
	parts := strings.Split(key, ".")
	switch {
	case key == "modules.visibility":
		return "/api/modules", nil
	case len(parts) == 3 && parts[0] == "module" && parts[2] == "visibility":
		return "/api/modules/" + parts[1] + "/visibility", nil
	case key == "repo.visibility":
		return "/api/repos/visibility", nil
	case key == "repo.shares":
		return "/api/repos/shares", nil
	case len(parts) == 3 && parts[0] == "runtime" && parts[2] == "visibility":
		return "/api/runtimes/" + parts[1], nil
	case len(parts) == 3 && parts[0] == "agent" && parts[2] == "access-passes":
		return "/api/agents/" + parts[1] + "/access-passes", nil
	case len(parts) == 3 && parts[0] == "agent" && parts[2] == "runtime-skill":
		return "/api/agents/" + parts[1] + "/runtime-skills/enabled", nil
	default:
		return "", fmt.Errorf("unknown settings key %q; run 'multica settings --help'", key)
	}
}

func settingsClient(cmd *cobra.Command) (*cli.APIClient, error) {
	if _, err := requireWorkspaceID(cmd); err != nil {
		return nil, err
	}
	return newAPIClient(cmd)
}

func runSettingsGet(cmd *cobra.Command, args []string) error {
	client, err := settingsClient(cmd)
	if err != nil {
		return err
	}
	path, err := settingsPath(args[0])
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if args[0] == "repo.shares" { // URL is supplied as a query in --value-json for symmetry.
		path += "?url=" + url.QueryEscape(urlQueryValue(cmd))
	}
	var out any
	if err := client.GetJSON(ctx, path, &out); err != nil {
		return fmt.Errorf("get setting %s: %w", args[0], err)
	}
	return cli.PrintJSON(os.Stdout, out)
}

func urlQueryValue(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("value-json")
	var body map[string]any
	if json.Unmarshal([]byte(v), &body) == nil {
		if u, ok := body["url"].(string); ok {
			return u
		}
	}
	return v
}

func readSettingValue(cmd *cobra.Command) (any, error) {
	jsonValue, _ := cmd.Flags().GetString("value-json")
	file, _ := cmd.Flags().GetString("value-file")
	stdin, _ := cmd.Flags().GetBool("value-stdin")
	if boolToInt(jsonValue != "")+boolToInt(file != "")+boolToInt(stdin) != 1 {
		return nil, fmt.Errorf("provide exactly one of --value-json, --value-file, or --value-stdin")
	}
	var data []byte
	var err error
	if file != "" {
		data, err = os.ReadFile(file)
	} else if stdin {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data = []byte(jsonValue)
	}
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("value must be valid JSON: %w", err)
	}
	return value, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func runSettingsSet(cmd *cobra.Command, args []string) error {
	client, err := settingsClient(cmd)
	if err != nil {
		return err
	}
	path, err := settingsPath(args[0])
	if err != nil {
		return err
	}
	value, err := readSettingValue(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out any
	switch {
	case args[0] == "repo.shares":
		if err := client.PostJSON(ctx, path, value, &out); err != nil {
			return fmt.Errorf("set setting %s: %w", args[0], err)
		}
	case strings.HasPrefix(args[0], "agent.") && strings.HasSuffix(args[0], "access-passes"):
		if body, ok := value.(map[string]any); ok && body["revoke_id"] != nil {
			return client.DeleteJSON(ctx, path+"/"+fmt.Sprint(body["revoke_id"]))
		}
		if err := client.PostJSON(ctx, path, value, &out); err != nil {
			return fmt.Errorf("set setting %s: %w", args[0], err)
		}
	case strings.HasPrefix(args[0], "agent."):
		if err := client.PutJSON(ctx, path, value, &out); err != nil {
			return fmt.Errorf("set setting %s: %w", args[0], err)
		}
	case strings.HasPrefix(args[0], "module."):
		if err := client.PutJSON(ctx, path, value, &out); err != nil {
			return fmt.Errorf("set setting %s: %w", args[0], err)
		}
	default:
		if err := client.PatchJSON(ctx, path, value, &out); err != nil {
			return fmt.Errorf("set setting %s: %w", args[0], err)
		}
	}
	if out == nil {
		return nil
	}
	return cli.PrintJSON(os.Stdout, out)
}
