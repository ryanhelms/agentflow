---
name: setup
description: Plan provider readiness and register a project client
argument-hint: "[plan | verify | apply] [--client NAME]"
---
Run `devflow setup plan` or call `devflow_setup_plan` to inspect the selected
provider requirements. Use the current client or the one requested by the user.
Explain any missing prerequisites. When the user approves client registration,
use `devflow_setup_apply` or `devflow setup apply --client NAME`.
