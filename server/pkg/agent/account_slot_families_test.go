package agent

import "testing"

func TestAccountSlotFamiliesAreDistinct(t *testing.T) {
	keys := map[string]string{}
	for _, family := range AccountSlotFamilies() {
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
	cases := map[string]string{"dsh": "DSH_HOME", "claude": "CLAUDE_CONFIG_DIR"}
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
	if _, ok := AccountSlotFamilyFor("agy"); ok {
		t.Error("agy must not own a slot registry: its directory is chosen in custom args")
	}
	if _, ok := AccountSlotFamilyFor("cursor"); ok {
		t.Error("cursor must not own a slot registry: CURSOR_DATA_DIR is rewritten per task")
	}
}
