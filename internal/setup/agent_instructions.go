package setup

import (
	"fmt"
	"os"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
)

const agentInstructionsFile = "AGENTS.md"

// AgentInstructions is the same workflow guidance written by Init and Install.
func AgentInstructions() string {
	block := strings.TrimSpace(string(mustTemplate(agentInstructionsFile)))
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(block, blockStart), blockEnd))
}

// AgentProviderSelection reports the resolved roles without embedding settings or credentials.
func AgentProviderSelection(cfg *config.Repo) string {
	return fmt.Sprintf("Devflow providers from .devflow/config.yml: task_management=%s, knowledge_base=%s.\n",
		cfg.Providers.TaskManagement.Provider, cfg.Providers.KnowledgeBase.Provider)
}

// RefreshAgentInstructions updates the managed block without bootstrapping other repo files.
func RefreshAgentInstructions(repoDir string) (*Result, error) {
	res := &Result{}
	return res, writeAgentInstructions(res, repoDir)
}

func writeAgentInstructions(res *Result, repoDir string) error {
	root, err := os.OpenRoot(repoDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return withClientLock(root, agentInstructionsFile, func() error {
		current, err := readProjectFile(root, agentInstructionsFile)
		if err != nil {
			return err
		}
		existing := string(current)
		block := strings.TrimSpace(string(mustTemplate(agentInstructionsFile)))
		first, last := strings.Index(existing, blockStart), strings.Index(existing, blockEnd)
		var next string
		switch {
		case first < 0 && last < 0:
			next = existing
			if next != "" && !strings.HasSuffix(next, "\n\n") {
				if !strings.HasSuffix(next, "\n") {
					next += "\n"
				}
				next += "\n"
			}
			next += block + "\n"
		case first >= 0 && last > first && strings.Count(existing, blockStart) == 1 && strings.Count(existing, blockEnd) == 1:
			next = existing[:first] + block + existing[last+len(blockEnd):]
		default:
			return fmt.Errorf("%s: expected one complete devflow instruction block", agentInstructionsFile)
		}
		if next == existing {
			res.Notes = append(res.Notes, "AGENTS.md guidance is up to date")
			return nil
		}
		if err := publishAfter(root, agentInstructionsFile, []byte(next), func() error {
			latest, err := readProjectFile(root, agentInstructionsFile)
			if err != nil {
				return err
			}
			if string(latest) != existing {
				return fmt.Errorf("%s changed during setup; retry with the current guidance", agentInstructionsFile)
			}
			return nil
		}); err != nil {
			return err
		}
		res.Merged = append(res.Merged, agentInstructionsFile)
		return nil
	})
}
