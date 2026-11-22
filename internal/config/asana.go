package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nscaledev/devflow/internal/provider/asana"
)

// Project is the default scope for work and bare task GIDs. Transitions are
// keyed first by project GID, then by event, since a task can be multi-homed.
type AsanaSettings struct {
	Project     string                       `yaml:"project"`
	Transitions map[string]map[string]string `yaml:"transitions"`
}

func (s *AsanaSettings) HasProject(project string) bool {
	_, mapped := s.Transitions[project]
	return project != "" && (project == s.Project || mapped)
}

func (s *AsanaSettings) validate(sel Selection) error {
	if s.Project != "" && !asana.ValidGID(s.Project) {
		return sel.settingError("task_management", "project", errors.New("expected an Asana project GID"))
	}
	for _, project := range slices.Sorted(maps.Keys(s.Transitions)) {
		events := s.Transitions[project]
		path := "transitions." + escapeSettingKey.Replace(project)
		if !asana.ValidGID(project) {
			return sel.settingError("task_management", path, fmt.Errorf("invalid project GID %q", project))
		}
		for _, event := range slices.Sorted(maps.Keys(events)) {
			target := events[event]
			if !asana.ValidGID(target) || target == asana.StateOpen {
				return sel.settingError("task_management", path+"."+escapeSettingKey.Replace(event), errors.New("expected a section GID, completed or none"))
			}
		}
	}
	return nil
}

func (s *AsanaSettings) AdapterConfig() asana.Config {
	sections := map[string][]string{}
	if s.Project != "" {
		sections[s.Project] = nil
	}
	for project, events := range s.Transitions {
		var ids []string
		for _, target := range events {
			if target != "none" && target != asana.StateCompleted && !slices.Contains(ids, target) {
				ids = append(ids, target)
			}
		}
		slices.Sort(ids)
		sections[project] = ids
	}
	return asana.Config{Instance: "asana", Sections: sections}
}
