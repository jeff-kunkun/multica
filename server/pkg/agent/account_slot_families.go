package agent

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// AccountSlotFamily is one CLI family that owns a numbered account-slot
// registry: slot 1 is the CLI's own directory (BaseDir), slot N is
// "<AccountDirPrefix>N", and an agent's registered slots live under
// runtime_config[RuntimeConfigKey] as {"accounts": [1, 2, …]}.
//
// The table is loaded from account_slot_families.json (embedded at build time),
// the single source of truth shared with the TypeScript side: the daemon's
// account probe takes its directory layout and lever from here, and
// packages/core/agents/account-slot-families.ts is regenerated from the same
// file by `pnpm generate:account-slot-families`. CI fails on any drift.
//
// A family belongs here only when its lever really rebinds the CLI. Codex and
// Cursor are deliberately absent: CODEX_HOME / CURSOR_DATA_DIR are refused by
// the daemon's env blocklist and rewritten per task, so a slot registered for
// them would be a switch that silently does nothing.
type AccountSlotFamily struct {
	CLI              string `json:"cli"`
	BaseDir          string `json:"base_dir"`
	AccountDirPrefix string `json:"account_dir_prefix"`
	// Lever is "custom_args:<flag>" or "env:<KEY>" — how a bound slot reaches
	// the CLI at launch.
	Lever            string `json:"lever"`
	RuntimeConfigKey string `json:"runtime_config_key"`
	// DefaultAccounts seeds the list for an agent that never saved one.
	DefaultAccounts []int `json:"default_accounts"`
	// RotatesOnQuota is true only where the daemon can detect an exhausted quota
	// and move to the next slot (agy_quota.go). Every other family's slots are a
	// manual registry: the bound directory takes effect, nothing rotates.
	RotatesOnQuota bool `json:"rotates_on_quota"`
}

// AccountGlob is the family's additional-account directory pattern, relative
// to the host home.
func (f AccountSlotFamily) AccountGlob() string {
	return f.AccountDirPrefix + "*"
}

// EnvLeverKey returns the env var an "env:<KEY>" lever writes, or "" for any
// other lever shape.
func (f AccountSlotFamily) EnvLeverKey() string {
	key, ok := strings.CutPrefix(f.Lever, "env:")
	if !ok {
		return ""
	}
	return strings.TrimSpace(key)
}

type accountSlotFamiliesFile struct {
	MaxAccountNumber int                 `json:"max_account_number"`
	Families         []AccountSlotFamily `json:"families"`
}

//go:embed account_slot_families.json
var accountSlotFamiliesJSON []byte

var accountSlotFamilies = loadAccountSlotFamilies()

func loadAccountSlotFamilies() accountSlotFamiliesFile {
	var data accountSlotFamiliesFile
	if err := json.Unmarshal(accountSlotFamiliesJSON, &data); err != nil {
		// The JSON is checked in and embedded; a parse failure is a programming
		// error, so the binary refuses to start instead of probing with a
		// half-loaded table.
		panic("agent: parse account_slot_families.json: " + err.Error())
	}
	if data.MaxAccountNumber < 1 {
		panic("agent: account_slot_families.json: max_account_number must be >= 1")
	}
	seen := make(map[string]struct{}, len(data.Families))
	for _, family := range data.Families {
		if family.CLI == "" || family.BaseDir == "" || family.AccountDirPrefix == "" ||
			family.Lever == "" || family.RuntimeConfigKey == "" {
			panic(fmt.Sprintf("agent: account_slot_families.json: incomplete family %+v", family))
		}
		if _, dup := seen[family.CLI]; dup {
			panic("agent: account_slot_families.json: duplicate cli " + family.CLI)
		}
		seen[family.CLI] = struct{}{}
	}
	return data
}

// AccountSlotFamilies returns every CLI family with a numbered slot registry,
// in the file's order. The slice is a copy; callers may not mutate the table.
func AccountSlotFamilies() []AccountSlotFamily {
	return append([]AccountSlotFamily(nil), accountSlotFamilies.Families...)
}

// AccountSlotFamilyFor returns the family for a CLI id.
func AccountSlotFamilyFor(cli string) (AccountSlotFamily, bool) {
	for _, family := range accountSlotFamilies.Families {
		if family.CLI == cli {
			return family, true
		}
	}
	return AccountSlotFamily{}, false
}

// MaxAccountSlotNumber is the highest slot number any family may register.
func MaxAccountSlotNumber() int {
	return accountSlotFamilies.MaxAccountNumber
}
