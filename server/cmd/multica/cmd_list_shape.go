package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/listshape"
	"github.com/spf13/cobra"
)

func addListShapeFlags(cmd *cobra.Command) {
	cmd.Flags().String("fields", "", "Comma-separated public response fields; overrides --compact. JSON output only; projected by the server.")
	cmd.Flags().Bool("compact", false, "Return IDs, names and key status fields only. Recommended for agent reads: --compact --output json.")
}

func listShapeQuery(cmd *cobra.Command, q url.Values, compact []string) ([]string, error) {
	if raw, _ := cmd.Flags().GetString("fields"); cmd.Flags().Changed("fields") {
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("--fields cannot be empty")
		}
		if output, _ := cmd.Flags().GetString("output"); output == "table" {
			return nil, fmt.Errorf("--fields requires --output json")
		}
		q.Set("fields", raw)
	}
	if enabled, _ := cmd.Flags().GetBool("compact"); enabled {
		q.Set("compact", "true")
	}
	return listshape.Fields(q, compact)
}
