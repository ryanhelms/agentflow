package settings

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/ryanhelms/agentflow/internal/consolecfg"
)

var pluginIDRe = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)

// ClearPlugin deletes plugins.<id> from the personal layer: the part of
// plugin remove that belongs to the settings files (ADR "Phase 5 install",
// "Remove"). It reports whether the block was there.
//
// It is not a settings save. plugin remove has already asked the person, and
// removing a plugin's settings can only turn things off, so there is no second
// confirmation and no journal entry. It takes the personal settings lock, keeps
// comments, key order and every other key, writes the file atomically with mode
// 0600, writes through a link to a dotfiles checkout like a save does, and
// removes plugins itself when it ends up empty. A file that cannot be edited
// is left as it was.
func (s *Service) ClearPlugin(id string) (bool, error) {
	if !pluginIDRe.MatchString(id) {
		return false, fmt.Errorf("%q is not a plugin ID", id)
	}
	unlock, err := s.lock()
	if err != nil {
		return false, err
	}
	defer unlock()
	l, err := readPersonal(s.personalPath())
	if err != nil {
		return false, errors.New(safeMessage(err))
	}
	if !l.Exists {
		return false, nil
	}
	keys := []string{"plugins", id}
	if _, set, err := consolecfg.LayerValue(l.Content, keys); err != nil || !set {
		return false, err
	}
	next, err := consolecfg.EditLayer(l.Content, keys, nil, true)
	if err != nil {
		return false, errors.New(safeMessage(err))
	}
	if v, set, err := consolecfg.LayerValue(next, []string{"plugins"}); err == nil && set && (string(v) == "{}" || string(v) == "null") {
		if empty, err := consolecfg.EditLayer(next, []string{"plugins"}, nil, true); err == nil {
			next = empty
		}
	}
	if err := writePersonalFile(l.abs, next); err != nil {
		return false, errors.New("could not write the personal settings")
	}
	return true, nil
}
