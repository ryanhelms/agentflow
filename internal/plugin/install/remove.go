package install

import (
	"errors"
	"fmt"
	"slices"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// Remove deletes the plugin: its registry entry first, so the console stops
// it, then its installed copies and its data directory. It does not touch
// the personal settings: the caller removes plugins.<id> there.
//
// Remove returns manifest.ErrNotFound when the registry has no such plugin
// any more. When the files cannot be deleted the plugin is already removed
// from the registry and the error names the directory to delete by hand.
func Remove(home string, plan *RemovePlan) error {
	id := plan.Entry.ID
	root := manifest.RootPath(home, id)
	if root == "" {
		return fmt.Errorf("%q is not a plugin ID", id)
	}
	err := manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
		i := slices.IndexFunc(r.Plugins, func(p manifest.Installed) bool { return p.ID == id })
		if i < 0 {
			return fmt.Errorf("%w: %s", manifest.ErrNotFound, id)
		}
		r.Plugins = slices.Delete(r.Plugins, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	if err := removeTree(root); err != nil {
		return errors.Join(fmt.Errorf("plugin %s is removed from the registry, but its files are still in %s: delete that directory yourself", id, root), err)
	}
	return nil
}
