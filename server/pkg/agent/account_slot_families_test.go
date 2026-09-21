package agent

import (
	"reflect"
	"testing"
)

// The agy family in the shared table must describe exactly what agy_quota.go
// rotates over. agy_quota.go keeps its own constants on purpose (its key name
// and rotation are frozen: workspaces already store `agy_slots`), so this is
// the guard that the table the UI is generated from cannot drift from them.
func TestAccountSlotFamiliesAgyMatchesQuotaRotation(t *testing.T) {
	family, ok := AccountSlotFamilyFor("agy")
	if !ok {
		t.Fatal("agy family missing from account_slot_families.json")
	}
	if family.RuntimeConfigKey != agySlotsRuntimeKey {
		t.Errorf("runtime_config_key = %q, rotation reads %q", family.RuntimeConfigKey, agySlotsRuntimeKey)
	}
	if MaxAccountSlotNumber() != maxAgyAccountNumber {
		t.Errorf("max_account_number = %d, rotation caps at %d", MaxAccountSlotNumber(), maxAgyAccountNumber)
	}
	if family.BaseDir != agyAccount1Dir {
		t.Errorf("base_dir = %q, rotation uses %q", family.BaseDir, agyAccount1Dir)
	}
	if got, want := family.AccountDirPrefix+"7", agyAccountDirLeaf(7); got != want {
		t.Errorf("slot 7 directory = %q, rotation uses %q", got, want)
	}
	if got := ParseAgySlotAccounts(nil, ""); !reflect.DeepEqual(got, family.DefaultAccounts) {
		t.Errorf("default_accounts = %v, rotation seeds %v", family.DefaultAccounts, got)
	}
	if !family.RotatesOnQuota {
		t.Error("agy must be marked rotates_on_quota")
	}
}

// Rotation is agy-only: no other family has a quota signal the daemon can
// detect, and the UI tells the user so based on this flag.
func TestAccountSlotFamiliesOnlyAgyRotates(t *testing.T) {
	keys := map[string]string{}
	for _, family := range AccountSlotFamilies() {
		if family.RotatesOnQuota && family.CLI != "agy" {
			t.Errorf("%s is marked rotates_on_quota but has no rotation backend", family.CLI)
		}
		if other, dup := keys[family.RuntimeConfigKey]; dup {
			t.Errorf("%s and %s share runtime_config key %q", other, family.CLI, family.RuntimeConfigKey)
		}
		keys[family.RuntimeConfigKey] = family.CLI
		if len(family.DefaultAccounts) == 0 || family.DefaultAccounts[0] != 1 {
			t.Errorf("%s default_accounts = %v, must start with slot 1", family.CLI, family.DefaultAccounts)
		}
	}
}

func TestAccountSlotFamilyEnvLeverKey(t *testing.T) {
	cases := map[string]string{"agy": "", "dsh": "DSH_HOME", "claude": "CLAUDE_CONFIG_DIR"}
	for cli, want := range cases {
		family, ok := AccountSlotFamilyFor(cli)
		if !ok {
			t.Fatalf("%s family missing", cli)
		}
		if got := family.EnvLeverKey(); got != want {
			t.Errorf("%s EnvLeverKey() = %q, want %q", cli, got, want)
		}
	}
	if _, ok := AccountSlotFamilyFor("codex"); ok {
		t.Error("codex must not own a slot registry: CODEX_HOME is rewritten per task")
	}
	if _, ok := AccountSlotFamilyFor("cursor"); ok {
		t.Error("cursor must not own a slot registry: CURSOR_DATA_DIR is rewritten per task")
	}
}
