---
name: setup
description: Check provider readiness, install pinned gh, or register a project client
argument-hint: "[plan | verify | apply] [--client NAME] [--install-gh]"
---
Run `devflow setup plan` or call `devflow_setup_plan` to inspect the selected
provider requirements. Use the current client or the one requested by the user.
Explain any missing prerequisites. If the plan offers `install_gh`, show the
pinned artifact details. On request, use `devflow_setup_apply` with
`install_gh: true` or `devflow setup apply --install-gh`. Obtain approval for
the installation summary before retrying with its confirmation token.
When the user approves client registration,
use `devflow_setup_apply` or `devflow setup apply --client NAME`.
