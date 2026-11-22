# Provider authoring

Provider implementations translate between service APIs and the domain types
in [internal/provider](../internal/provider/). Select providers independently
in [the repository configuration](providers.md). Use the existing application
operations so terminal clients, MCP clients and hosted runners enforce the
same validation and policy.

1. Implement the contract for the selected role. Return qualified references
   with a stable instance, scope and ID. Declare optional capabilities such as
   assignment and comments separately. Use `provider.ErrUnsupported` for an
   operation the service cannot perform.
2. Add a role setting type and default constructor to
   [the schema registry](../internal/config/providers.go). Use the YAML field
   names and validation tags. Keep credential references separate from
   public configuration, and add a provider readiness probe in
   [internal/setup](../internal/setup/).
3. Resolve configured templates and targets before requesting a write.
   Use application policy for approval and audit. Bind approval to the
   qualified destination and requested effects.
4. Preserve stable operation IDs across retries. Return existing effects
   when the destination can verify them. Keep ambiguous write results
   visible for reconciliation. Handle concurrent revisions according to
   the capability's documented preconditions.
5. Exercise the adapter with a deterministic fixture. Cover normal reads
   and writes, unavailable credentials, unsupported fields, an interrupted
   write, and replay. Check the existing CLI, MCP and caller workflow
   contracts when connecting an adapter to an entry point.

The [provider configuration guide](providers.md) contains YAML examples that
are loaded by the repository configuration tests. Use the
[consumer rehearsal](rollout.md#provider-migration-rehearsal) to validate a
change in a separate checkout before enabling a consumer writer.
