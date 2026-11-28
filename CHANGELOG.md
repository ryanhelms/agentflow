# Changelog

All notable changes to agentflow. This file is generated on release from
Conventional Commit messages by `scripts/changelog.sh`; do not edit it by hand.
Features, fixes, performance changes, reverts and breaking changes are listed;
docs, tests, CI and chores are not.

## [0.62.0](https://github.com/ryanhelms/agentflow/compare/v0.61.0...v0.62.0) - 2026-11-28

### Features

- rename the marketplace to agentflow; reinstall plugins (#144) ([f563980](https://github.com/ryanhelms/agentflow/commit/f563980b0e6aa81338783c9387716e9c42379a6b))
- add the design-system plugin to the marketplace (#146) ([7bc76a0](https://github.com/ryanhelms/agentflow/commit/7bc76a04b43ae384c267e6fc400de2ff8cc28388))
- ask setup questions on first init; no default init.orgs (#149) ([e7855e0](https://github.com/ryanhelms/agentflow/commit/e7855e0a710e0dd2763059b6d95da920aaf128ce))

### Fixes

- ignore input that reaches ai run as a prompt prints (#150) ([456b1dc](https://github.com/ryanhelms/agentflow/commit/456b1dc67d142e9155c9d73f3d311cfe0faa20b3))

## [0.61.0](https://github.com/ryanhelms/agentflow/compare/v0.60.0...v0.61.0) - 2026-11-27

### Features

- add the phase 7 ACP session contract (#128) ([fa4fcaa](https://github.com/ryanhelms/agentflow/commit/fa4fcaa68131816986ff87d74cc95052fb9de988))
- add the phase 7 aisession contract (#132) ([b8fe103](https://github.com/ryanhelms/agentflow/commit/b8fe103a9680957b673a62416af1f9af6b363e1d))
- add ACP sessions behind a permission chain (#138) ([a739659](https://github.com/ryanhelms/agentflow/commit/a7396598e72ec077c921ab7a6c1c1b85094d8ecd))

### Fixes

- block repository-supplied binaries; pin updates launchers (#139) ([be33bcc](https://github.com/ryanhelms/agentflow/commit/be33bcc450ecee838ffbabeb65b8897d3c8d60ff))
- stop finish deleting unmerged work and close merge gaps (#145) ([c918281](https://github.com/ryanhelms/agentflow/commit/c918281d6427b215663d640c597dbad05a3b381a))

## [0.60.0](https://github.com/ryanhelms/agentflow/compare/v0.59.0...v0.60.0) - 2026-11-27

### Features

- add the harness contract, test guard and jsonrpc.Run (#108) ([5c59dbc](https://github.com/ryanhelms/agentflow/commit/5c59dbc41524bfbaa49c2e721e1a511107ae1940))
- integrate phase 6 harness detection (#118) ([fdf0eca](https://github.com/ryanhelms/agentflow/commit/fdf0eca7c179012ee74979fc86dfbc05967edf98))

### Fixes

- wait for approved plugin writes when the host stops (#99) ([f037424](https://github.com/ryanhelms/agentflow/commit/f037424ee57f22ac83c31b7b4b6f6abad6aa2689))
- ignore an agentflow home inside a repository (#106) ([ab89596](https://github.com/ryanhelms/agentflow/commit/ab8959652545c491d3e6bb4f652f070d03045fab))
- refuse versioned launchers and show plugin probe commands (#126) ([b4389ac](https://github.com/ryanhelms/agentflow/commit/b4389accd8d85dae92a06079a265d223c20ee3b5))

## [0.59.0](https://github.com/ryanhelms/agentflow/compare/v0.58.0...v0.59.0) - 2026-11-27

### Features

- add shared stdio JSON-RPC and a scrubbed launcher (#83) ([e690f16](https://github.com/ryanhelms/agentflow/commit/e690f16f15bc3b7dbfa199bc9638e7d5defa62c9))
- add the plugin manifest package and plugin settings (#85) ([4baff6d](https://github.com/ryanhelms/agentflow/commit/4baff6d06de3ba16ebfebfe8fc8d15ee49d71dab))
- add plugin commands, samples and docs (#86) ([5be29a2](https://github.com/ryanhelms/agentflow/commit/5be29a297b31b4e0fe9266eb36e698106e87c57c))
- run plugins in the daemon with deny-only permission rules (#90) ([74baa10](https://github.com/ryanhelms/agentflow/commit/74baa10a136d9fe5105cd00291d79bd10425ffaf))
- show installed plugins and their health live (#84) ([89591f6](https://github.com/ryanhelms/agentflow/commit/89591f6160f6d25ee3234ac283829b5658829eb8))
- approve a plugin's write and run it once afterwards (#92) ([cdcfa14](https://github.com/ryanhelms/agentflow/commit/cdcfa14cfc760884193ad49956c38da7155ded6b))

### Fixes

- repeat the plugin and command right before approve asks (#96) ([013e2f4](https://github.com/ryanhelms/agentflow/commit/013e2f4cfc7aa3d98bfe95fb3305344ce82cfda7))

## [0.58.0](https://github.com/ryanhelms/agentflow/compare/v0.57.0...v0.58.0) - 2026-11-26

### Features

- describe every config field for structured controls (#73) ([6202a27](https://github.com/ryanhelms/agentflow/commit/6202a277cce54b5ac76f5a52941eddacb651c23a))
- save settings as one previewed change set (#74) ([f70669b](https://github.com/ryanhelms/agentflow/commit/f70669b344f5d9a596b4a7fa5e7c3b8f26a5af68))
- edit every setting with controls and one Save bar (#76) ([33f5ffd](https://github.com/ryanhelms/agentflow/commit/33f5ffd9c89233d3c808afb9859dce0e69b71fcd))

## [0.57.0](https://github.com/ryanhelms/agentflow/compare/v0.56.1...v0.57.0) - 2026-11-26

### Features

- wire the live event pipeline end to end (#31) ([5cf3e6d](https://github.com/ryanhelms/agentflow/commit/5cf3e6d5b04559afa8b5a72c1a627ab53352a5d7))
- make the console fully live (#51) ([a5648e3](https://github.com/ryanhelms/agentflow/commit/a5648e3cb07278bc74733942dd2ad650d5b97285))
- run every operation through commands with a live timeline (#59) ([c555ac4](https://github.com/ryanhelms/agentflow/commit/c555ac4a6215957a7900968cbd6b668cafdc4f0c))

## [0.56.1](https://github.com/ryanhelms/agentflow/compare/v0.56.0...v0.56.1) - 2026-11-26

### Fixes

- keep delegate plans stable between preview and apply (#30) ([b6ced0c](https://github.com/ryanhelms/agentflow/commit/b6ced0c4ef900025b3ee58cca34d1713d6f49f4d))

## [0.56.0](https://github.com/ryanhelms/agentflow/compare/v0.55.1...v0.56.0) - 2026-11-26

### Features

- keep each agent session inside its own repository (#9) ([33ce2c8](https://github.com/ryanhelms/agentflow/commit/33ce2c82b533b33c4a310ddd8ec2442d8ff420ff))

### Fixes

- **release:** resume a release whose tag push failed (#11) ([4e47090](https://github.com/ryanhelms/agentflow/commit/4e470900294e949c77b64ed9c03eb58dd3731125))

## [0.55.1](https://github.com/ryanhelms/agentflow/compare/v0.55.0...v0.55.1) - 2026-11-26

### Fixes

- list breaking changes once in the changelog (#5) ([bdd8cd6](https://github.com/ryanhelms/agentflow/commit/bdd8cd67ab445d9f17703a7d16ad978777a441ac))

## [0.55.0](https://github.com/ryanhelms/agentflow/compare/v0.54.0...v0.55.0) - 2026-11-26

### Breaking changes

- add the task worktree lifecycle and local enforcement (#2) ([aa737dc](https://github.com/ryanhelms/agentflow/commit/aa737dcddd619278e58683c839a9024996ea843d))

### Features

- add the task worktree lifecycle and local enforcement (#2) ([aa737dc](https://github.com/ryanhelms/agentflow/commit/aa737dcddd619278e58683c839a9024996ea843d))

## [0.54.0](https://github.com/ryanhelms/agentflow/compare/v0.53.0...v0.54.0) - 2026-11-26

### Breaking changes

- install and update agentflow from ryanhelms/agentflow (#1) ([34422dd](https://github.com/ryanhelms/agentflow/commit/34422dd7b25da9c8e2b0038cd2a27aaafe4d1a2e))

### Fixes

- install and update agentflow from ryanhelms/agentflow (#1) ([34422dd](https://github.com/ryanhelms/agentflow/commit/34422dd7b25da9c8e2b0038cd2a27aaafe4d1a2e))
- **release:** skip build attestation while the repo is private (#4) ([bdb8317](https://github.com/ryanhelms/agentflow/commit/bdb8317c8ed821528ea44ebef0cee3ac72154a5d))

## [0.53.0](https://github.com/nscaledev/agentflow/compare/v0.52.2...v0.53.0) - 2026-11-25

### Breaking changes

- rename devflow to agentflow ([ba469a2](https://github.com/nscaledev/agentflow/commit/ba469a25b126e0e686060b42dc41593335b9bfc0))

### Features

- rename devflow to agentflow ([ba469a2](https://github.com/nscaledev/agentflow/commit/ba469a25b126e0e686060b42dc41593335b9bfc0))

## [0.52.2](https://github.com/nscaledev/devflow/compare/v0.52.1...v0.52.2) - 2026-11-24

### Fixes

- isolate reviewed workflow checkouts (#123) ([908ab0f](https://github.com/nscaledev/devflow/commit/908ab0f47783e9e0db58a32a6f02db7e41b9bea9))

## [0.52.1](https://github.com/nscaledev/devflow/compare/v0.52.0...v0.52.1) - 2026-11-24

### Fixes

- resolve codeql findings (#122) ([ceeda41](https://github.com/nscaledev/devflow/commit/ceeda419a1ad19ce1e4ad82dac3c1b963444d45c))

## [0.52.0](https://github.com/nscaledev/devflow/compare/v0.51.0...v0.52.0) - 2026-11-23

### Features

- scope automatic pushes to pull request publication (#120) ([4e79bf7](https://github.com/nscaledev/devflow/commit/4e79bf7a054c6079aa3dc1bf18c68fe8463c227e))

## [0.51.0](https://github.com/nscaledev/devflow/compare/v0.50.0...v0.51.0) - 2026-11-23

### Features

- add type-aware configuration editors (#100) ([6e114ea](https://github.com/nscaledev/devflow/commit/6e114ea049bfb5ce1816d2333a5d320721a63c47))

## [0.50.0](https://github.com/nscaledev/devflow/compare/v0.49.0...v0.50.0) - 2026-11-23

### Features

- **asana:** bind desktop task writes to oauth (#81) ([61d6572](https://github.com/nscaledev/devflow/commit/61d657218985ecf93a9f90acb8057769e065d08d))

## [0.49.0](https://github.com/nscaledev/devflow/compare/v0.48.0...v0.49.0) - 2026-11-23

### Features

- redesign the repository console with nscale themes (#115) ([dd1d7d9](https://github.com/nscaledev/devflow/commit/dd1d7d9726d067f6d3d256d826850df4a8b7f477))

## [0.48.0](https://github.com/nscaledev/devflow/compare/v0.47.0...v0.48.0) - 2026-11-23

### Breaking changes

- make task and knowledge providers opt-in (#118) ([d17aff2](https://github.com/nscaledev/devflow/commit/d17aff28afdf812d92d76f2eeaf44f717bf10b76))

### Features

- make task and knowledge providers opt-in (#118) ([d17aff2](https://github.com/nscaledev/devflow/commit/d17aff28afdf812d92d76f2eeaf44f717bf10b76))

## [0.47.0](https://github.com/nscaledev/devflow/compare/v0.46.1...v0.47.0) - 2026-11-23

### Features

- manage console configuration through MCP (#116) ([839dac0](https://github.com/nscaledev/devflow/commit/839dac0fc2e35ecd53457d98686db44a0720e18f))

## [0.46.1](https://github.com/nscaledev/devflow/compare/v0.46.0...v0.46.1) - 2026-11-23

### Fixes

- **setup:** prepare provider clis for standalone reads (#110) ([680cfca](https://github.com/nscaledev/devflow/commit/680cfca8f21bdf0445e89bbb9aed92006af0f254))

## [0.46.0](https://github.com/nscaledev/devflow/compare/v0.45.0...v0.46.0) - 2026-11-23

### Features

- scope and share action approvals across agents (#105) ([8aeb8c5](https://github.com/nscaledev/devflow/commit/8aeb8c56c2d16a1923fae4e6fdf5ff4edfabb068))

## [0.45.0](https://github.com/nscaledev/devflow/compare/v0.44.1...v0.45.0) - 2026-11-23

### Features

- **setup:** prepare provider cli fallbacks automatically (#106) ([b61f818](https://github.com/nscaledev/devflow/commit/b61f818d971b6d92f366381fe970c95083829a1f))

## [0.44.1](https://github.com/nscaledev/devflow/compare/v0.44.0...v0.44.1) - 2026-11-23

### Fixes

- **setup:** use browser oauth for github mcp in claude (#101) ([849e048](https://github.com/nscaledev/devflow/commit/849e048e55e8ac9162ef5572de9b872d9a09b170))

## [0.44.0](https://github.com/nscaledev/devflow/compare/v0.43.0...v0.44.0) - 2026-11-23

### Features

- install and manage devflow from terminals and AI clients (#102) ([426c95f](https://github.com/nscaledev/devflow/commit/426c95fa2571801ed0c8df5d3615ad72e39453f1))

## [0.43.0](https://github.com/nscaledev/devflow/compare/v0.42.0...v0.43.0) - 2026-11-23

### Features

- add a local repository console (#98) ([8c26c14](https://github.com/nscaledev/devflow/commit/8c26c140500c2e2f8ef38d3d29ec0bbaab4102ad))

## [0.42.0](https://github.com/nscaledev/devflow/compare/v0.41.0...v0.42.0) - 2026-11-23

### Features

- **setup:** offer missing provider mcp connections (#97) ([341a5d7](https://github.com/nscaledev/devflow/commit/341a5d70051d5403fa6be34985f71a9abae504dd))

## [0.41.0](https://github.com/nscaledev/devflow/compare/v0.40.0...v0.41.0) - 2026-11-23

### Features

- **setup:** generate config-driven agent guidance (#94) ([fef9219](https://github.com/nscaledev/devflow/commit/fef921903d387b1ca208a8225a499790ca8517c4))

## [0.40.0](https://github.com/nscaledev/devflow/compare/v0.39.0...v0.40.0) - 2026-11-23

### Features

- **knowledge:** adopt the devflow okf bundle (#92) ([0e8f271](https://github.com/nscaledev/devflow/commit/0e8f27141e4cd05ecff4d4ed3b04ab91f9f2e6e2))

## [0.39.0](https://github.com/nscaledev/devflow/compare/v0.38.0...v0.39.0) - 2026-11-23

### Features

- **knowledge:** publish and link okf documents (#91) ([2adba25](https://github.com/nscaledev/devflow/commit/2adba259841240c550edbce20238b617781f6e89))

## [0.38.0](https://github.com/nscaledev/devflow/compare/v0.37.0...v0.38.0) - 2026-11-23

### Features

- **knowledge:** read okf alongside legacy documents (#90) ([2615790](https://github.com/nscaledev/devflow/commit/26157900ab216d8892d86f7d1bc7210986628958))

## [0.37.0](https://github.com/nscaledev/devflow/compare/v0.36.0...v0.37.0) - 2026-11-23

### Features

- **slack:** add a devflow desktop mcp connection (#77) ([f31c465](https://github.com/nscaledev/devflow/commit/f31c465afa1e8fa74e2814d3979f34d4667243cb))

## [0.36.0](https://github.com/nscaledev/devflow/compare/v0.35.0...v0.36.0) - 2026-11-23

### Features

- **jira:** fall back to teamwork graph for creation (#82) ([7b7a18c](https://github.com/nscaledev/devflow/commit/7b7a18c3baac0225ddda7a55eecb1e97d1811043))

## [0.35.0](https://github.com/nscaledev/devflow/compare/v0.34.0...v0.35.0) - 2026-11-23

### Features

- **asana:** guide desktop mcp sign-in (#80) ([d3f7a38](https://github.com/nscaledev/devflow/commit/d3f7a387a3184a0f33b3b097b4d3a890e5e3b175))

## [0.34.0](https://github.com/nscaledev/devflow/compare/v0.33.1...v0.34.0) - 2026-11-22

### Features

- **jira:** verify desktop workflow writes (#79) ([bb123f4](https://github.com/nscaledev/devflow/commit/bb123f4d59eb139d60aa2b878729eaf83edbc113))

## [0.33.1](https://github.com/nscaledev/devflow/compare/v0.33.0...v0.33.1) - 2026-11-22

### Fixes

- **jira:** honor optional keys in pr readiness (#78) ([b93b8f6](https://github.com/nscaledev/devflow/commit/b93b8f61bf43a8ee2d8e04f1dced1d5313d81f1f))

## [0.33.0](https://github.com/nscaledev/devflow/compare/v0.32.0...v0.33.0) - 2026-11-22

### Features

- **jira:** use signed-in teamwork graph for comments (#76) ([aabcbf3](https://github.com/nscaledev/devflow/commit/aabcbf3f3adacf18cc42e2d5fd6161bd141c110f))

## [0.32.0](https://github.com/nscaledev/devflow/compare/v0.31.0...v0.32.0) - 2026-11-22

### Features

- **jira:** create issues from claude code desktop (#75) ([3fba552](https://github.com/nscaledev/devflow/commit/3fba552c4646b103ec4c26216bb8e796b07efa9f))

## [0.31.0](https://github.com/nscaledev/devflow/compare/v0.30.0...v0.31.0) - 2026-11-22

### Features

- preview jira comments through desktop rovo (#74) ([55dc663](https://github.com/nscaledev/devflow/commit/55dc663e28e1f6269ff2e2156b52b7d249998950))

## [0.30.0](https://github.com/nscaledev/devflow/compare/v0.29.0...v0.30.0) - 2026-11-22

### Features

- guide slack mcp sign-in in claude code desktop (#73) ([bcb46a5](https://github.com/nscaledev/devflow/commit/bcb46a5ccd890e0f458f25af90b35901a06d699c))

## [0.29.0](https://github.com/nscaledev/devflow/compare/v0.28.1...v0.29.0) - 2026-11-22

### Features

- verify confluence writes in claude code desktop (#72) ([a8bdc9b](https://github.com/nscaledev/devflow/commit/a8bdc9b18abd0719ecdafa7ee844986f901a8b65))

## [0.28.1](https://github.com/nscaledev/devflow/compare/v0.28.0...v0.28.1) - 2026-11-22

### Fixes

- share saved github cli login with desktop setup (#71) ([65659ea](https://github.com/nscaledev/devflow/commit/65659ea002c4b204db5594a8ff23f935e7873c34))

## [0.28.0](https://github.com/nscaledev/devflow/compare/v0.27.0...v0.28.0) - 2026-11-22

### Features

- add layered provider definitions with release constraints (#67) ([af1aeaf](https://github.com/nscaledev/devflow/commit/af1aeaf2093d28672a6fc4d92a1efc564fa45fa6))

## [0.27.0](https://github.com/nscaledev/devflow/compare/v0.26.0...v0.27.0) - 2026-11-22

### Features

- enable jira web links in claude code desktop (#70) ([0296645](https://github.com/nscaledev/devflow/commit/02966453ae2f788307310afb82eedce68c737665))

## [0.26.0](https://github.com/nscaledev/devflow/compare/v0.25.0...v0.26.0) - 2026-11-22

### Features

- add cli commands for every mcp tool (#65) ([d43dacf](https://github.com/nscaledev/devflow/commit/d43dacff997c8517540f216f908fc6329709b97a))

## [0.25.0](https://github.com/nscaledev/devflow/compare/v0.24.0...v0.25.0) - 2026-11-22

### Features

- list available providers with devflow setup providers (#64) ([0078f74](https://github.com/nscaledev/devflow/commit/0078f749c8ca15414b3c54282af22ab266f290d3))

## [0.24.0](https://github.com/nscaledev/devflow/compare/v0.23.0...v0.24.0) - 2026-11-22

### Features

- route interactive operations through provider tools (#69) ([2864165](https://github.com/nscaledev/devflow/commit/2864165f1b2d6da0047f9b08426f1126937bd8fc))

## [0.23.0](https://github.com/nscaledev/devflow/compare/v0.22.0...v0.23.0) - 2026-11-22

### Features

- start provider sign-in through native tools (#66) ([a282ea4](https://github.com/nscaledev/devflow/commit/a282ea43ea4d545b244160a411c48903635a83d5))

## [0.22.0](https://github.com/nscaledev/devflow/compare/v0.21.0...v0.22.0) - 2026-11-22

### Features

- register devflow in claude code desktop ([7367d7a](https://github.com/nscaledev/devflow/commit/7367d7a99406833e0cedff8450a744b80849c82b))

## [0.21.0](https://github.com/nscaledev/devflow/compare/v0.20.0...v0.21.0) - 2026-11-22

### Features

- complete provider verification and cleanup ([b2e1df0](https://github.com/nscaledev/devflow/commit/b2e1df08913f0cf9419527e63ad75372508a1b56))

## [0.20.0](https://github.com/nscaledev/devflow/compare/v0.19.0...v0.20.0) - 2026-11-22

### Features

- integrate provider delivery and workflow replay ([6351c00](https://github.com/nscaledev/devflow/commit/6351c0037f27fdb422820858ee32eb8ebafa92d8))

## [0.19.0](https://github.com/nscaledev/devflow/compare/v0.18.0...v0.19.0) - 2026-11-22

### Features

- verify and register declared mcp companions ([1f20738](https://github.com/nscaledev/devflow/commit/1f20738ce21171ca43dc3df06c05dbf3f4e7083a))

## [0.18.0](https://github.com/nscaledev/devflow/compare/v0.17.0...v0.18.0) - 2026-11-22

### Features

- add asana and shared task operations ([5fbf40a](https://github.com/nscaledev/devflow/commit/5fbf40a701ab7ab0806ca88416cf42a6eb5288bc))

## [0.17.0](https://github.com/nscaledev/devflow/compare/v0.16.0...v0.17.0) - 2026-11-22

### Features

- support github wiki as a knowledge provider ([6e83dde](https://github.com/nscaledev/devflow/commit/6e83ddea8657791f720a64394275499d7f870959))

## [0.16.0](https://github.com/nscaledev/devflow/compare/v0.15.0...v0.16.0) - 2026-11-22

### Features

- wire file-backed task provider operations ([2b79d09](https://github.com/nscaledev/devflow/commit/2b79d09ecb9733fdf9ac6acaca68833caa3edd62))

## [0.15.0](https://github.com/nscaledev/devflow/compare/v0.14.0...v0.15.0) - 2026-11-22

### Features

- install verified github cli during setup ([6df129a](https://github.com/nscaledev/devflow/commit/6df129ab686e51d913a7be602ed92a7e7feb260d))

## [0.14.0](https://github.com/nscaledev/devflow/compare/v0.13.0...v0.14.0) - 2026-11-22

### Features

- guide provider setup from cli and mcp ([395ae04](https://github.com/nscaledev/devflow/commit/395ae04afe278d62abf4aede803f6fcea1844dc8))

## [0.13.0](https://github.com/nscaledev/devflow/compare/v0.12.0...v0.13.0) - 2026-11-22

### Features

- apply task templates to github issues ([9224024](https://github.com/nscaledev/devflow/commit/92240243e715c369363d270e85a677282c6a7554))

## [0.12.0](https://github.com/nscaledev/devflow/compare/v0.11.0...v0.12.0) - 2026-11-22

### Features

- publish repository knowledge through devflow ([aab09ca](https://github.com/nscaledev/devflow/commit/aab09ca22df057d0dd8a2170809544e85492c011))

## [0.11.0](https://github.com/nscaledev/devflow/compare/v0.10.0...v0.11.0) - 2026-11-22

### Features

- process github task events in ci ([8e05504](https://github.com/nscaledev/devflow/commit/8e055042c5a85425da6a432738213f0c3f6634cb))

## [0.10.0](https://github.com/nscaledev/devflow/compare/v0.9.0...v0.10.0) - 2026-11-22

### Features

- add git-backed repository providers ([07cb468](https://github.com/nscaledev/devflow/commit/07cb468d654fca809142148822cdd3f2d8f0472f))

## [0.9.0](https://github.com/nscaledev/devflow/compare/v0.8.0...v0.9.0) - 2026-11-22

### Features

- support the github issue task lifecycle ([95501fb](https://github.com/nscaledev/devflow/commit/95501fb58139cf2814d19259bebbbef3a4c2eeee))

## [0.8.0](https://github.com/nscaledev/devflow/compare/v0.7.0...v0.8.0) - 2026-11-22

### Features

- implement the filesystem provider ([a1a82d8](https://github.com/nscaledev/devflow/commit/a1a82d841a9355f25ec18ffbb56e7d68faa720ad))

## [0.7.0](https://github.com/nscaledev/devflow/compare/v0.6.0...v0.7.0) - 2026-11-22

### Features

- define provider contracts and configuration ([b994d5f](https://github.com/nscaledev/devflow/commit/b994d5fc721b3b5b7583ab028814093d5d2cc153))

## [0.6.0](https://github.com/nscaledev/devflow/compare/v0.5.2...v0.6.0) - 2026-11-21

### Features

- add devflow pin and make the github app optional (#14) ([4152877](https://github.com/nscaledev/devflow/commit/41528774899692577d57ede57746c93bba6972f9))

## [0.5.2](https://github.com/nscaledev/devflow/compare/v0.5.1...v0.5.2) - 2026-11-20

### Fixes

- **release:** push protected release tags with a deploy key (#13) ([9ae335c](https://github.com/nscaledev/devflow/commit/9ae335cd74192020017a1d5acdca62b61f8302e2))

## [0.5.1](https://github.com/nscaledev/devflow/compare/v0.5.0...v0.5.1) - 2026-11-20

### Fixes

- kill switch gates every job; atomic launcher install (#12) ([708b2d6](https://github.com/nscaledev/devflow/commit/708b2d67019302df5fd86ce9008326486ef33d0d))

## [0.5.0](https://github.com/nscaledev/devflow/compare/v0.4.2...v0.5.0) - 2026-11-20

### Features

- pin caller workflows to the release commit with Dependabot (#11) ([e6da822](https://github.com/nscaledev/devflow/commit/e6da822a8b2cdd49e4b20d630c8c2947b22da5ca))

## [0.4.2](https://github.com/nscaledev/devflow/compare/v0.4.1...v0.4.2) - 2026-11-20

### Fixes

- pin attestation signer, scope secrets, honour require_key (#10) ([f474655](https://github.com/nscaledev/devflow/commit/f474655951ab7beef4c864ebfcecd1b74b6972c5))

## [0.4.1](https://github.com/nscaledev/devflow/compare/v0.4.0...v0.4.1) - 2026-11-20

### Fixes

- key-check applies the repo's commit rules and require_key (#9) ([236d876](https://github.com/nscaledev/devflow/commit/236d876a638217bd6f8f8aaf215cfe33692eca9a))

## [0.4.0](https://github.com/nscaledev/devflow/compare/v0.3.1...v0.4.0) - 2026-11-20

### Features

- auto-init from the plugin, devflow remove, skill per command (#8) ([5aa3c03](https://github.com/nscaledev/devflow/commit/5aa3c0346b5aea1a374364a25ee0b4fa1ec77a8f))

## [0.3.1](https://github.com/nscaledev/devflow/compare/v0.3.0...v0.3.1) - 2026-11-20

### Fixes

- check PR titles before they become squash commits (#7) ([c871e27](https://github.com/nscaledev/devflow/commit/c871e2757c4428196c00c28cf516292805a93db8))

## [0.3.0](https://github.com/nscaledev/devflow/compare/v0.2.0...v0.3.0) - 2026-11-20

### Breaking changes

- publish stable releases and use the v0 floating tag (#5) ([bae531b](https://github.com/nscaledev/devflow/commit/bae531b0589477129d8c8818d67725e208b84965))

### Features

- publish stable releases and use the v0 floating tag (#5) ([bae531b](https://github.com/nscaledev/devflow/commit/bae531b0589477129d8c8818d67725e208b84965))

## [0.2.0](https://github.com/nscaledev/devflow/compare/v0.1.0...v0.2.0) - 2026-11-20

### Features

- generated changelog and enforced commit convention for devflow (#4) ([6bf1efb](https://github.com/nscaledev/devflow/commit/6bf1efb7400ebb1e2dc12eebf5f33e7e1dc6ad3a))

## [0.1.0](https://github.com/nscaledev/devflow/releases/tag/v0.1.0) - 2026-11-20

### Features

- add devflow CLI, MCP server, workflows and client adapters ([9537532](https://github.com/nscaledev/devflow/commit/9537532f3ca021f844eb3142fc3b893efdf2c6d2))
- semantic versioning for every deployable, plugin e2e test and fixes (#1) ([42a700e](https://github.com/nscaledev/devflow/commit/42a700e73b2062aba37603280abae31592ad17ee))
