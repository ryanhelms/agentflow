package cli

import "testing"

// TestGoldenConfigSettings pins the output of the settings commands: config
// set (preview, confirmed save, refusal), config undo and config fields.
func TestGoldenConfigSettings(t *testing.T) {
	g := goldenConfigRepo(t)
	g.check("config_settings/set_dry_run.txt", g.run("", "config", "set", "--dry-run", "autonomy.local=auto", "approval_window=10m"))
	g.check("config_settings/set_invalid.txt", g.run("", "config", "set", "--yes", "personal:autonomy.remote=always"))
	g.check("config_settings/set_protected.txt", g.run("", "config", "set", "--yes", "--mode", "worktree", "commit.subject_max=60"))
	g.check("config_settings/set.txt", g.run("", "config", "set", "--yes", "autonomy.remote=suggest", "approval_window=10m"))
	g.check("config_settings/fields_modified.txt", g.run("", "config", "fields", "--modified"))
	g.check("config_settings/undo_json.txt", g.run("", "config", "undo", "--yes", "--json"))
	g.check("config_settings/undo_redo.txt", g.run("", "config", "undo", "--yes"))
}
