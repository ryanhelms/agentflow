package config

import (
	"fmt"
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

func (s *AsanaSettings) validate() error {
	if s.Project != "" && !asana.ValidGID(s.Project) {
		return fmt.Errorf("task_management.settings.project: expected an Asana project GID")
	}
	for project, events := range s.Transitions {
		if !asana.ValidGID(project) {
			return fmt.Errorf("task_management.settings.transitions: invalid project GID %q", project)
		}
		for event, target := range events {
			if !asana.ValidGID(target) || target == asana.StateOpen {
				return fmt.Errorf("task_management.settings.transitions.%s.%s: expected a section GID, completed or none", project, event)
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
