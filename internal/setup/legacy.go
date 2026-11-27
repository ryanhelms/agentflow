package setup

// Releases before the plugin marketplace was renamed to "agentflow" merged these
// names into .claude/settings.json. They stay here so remove can still undo those
// installations and init can say that they are left over.
const (
	legacyMarketplace = "nscale"
	legacyPlugin      = "agentflow@" + legacyMarketplace
	claudeSettings    = ".claude/settings.json"
)

// legacyOverlay returns what an earlier template merged into rel, or nil.
func legacyOverlay(rel string) map[string]any {
	if rel != claudeSettings {
		return nil
	}
	return map[string]any{
		"extraKnownMarketplaces": map[string]any{
			legacyMarketplace: map[string]any{
				"source": map[string]any{"source": "github", "repo": "ryanhelms/agentflow"},
			},
		},
		"enabledPlugins": map[string]any{legacyPlugin: true},
	}
}

// legacyNote names the old entries still present in doc. Init never deletes them.
func legacyNote(rel string, doc map[string]any) string {
	for key, entries := range legacyOverlay(rel) {
		current, _ := doc[key].(map[string]any)
		for name := range entries.(map[string]any) {
			if _, found := current[name]; found {
				return rel + " still names the old " + legacyMarketplace + " marketplace: delete the " +
					legacyMarketplace + " and " + legacyPlugin + " entries, then reinstall the plugin (see the agentflow README)"
			}
		}
	}
	return ""
}
