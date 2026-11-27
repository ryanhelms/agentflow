import type { JsonSchema } from '../lib/schema.js';
/**
 * Builds connection-settings fields from a JSON Schema (string, integer/number, boolean, enum).
 * `x-secret` fields take a secret ref (ref:<name>), never a literal.
 */
export declare function SchemaForm({ schema, value, onChange, disabled }: {
    schema: JsonSchema;
    value: Record<string, unknown>;
    onChange: (v: Record<string, unknown>) => void;
    disabled?: boolean;
}): import("react/jsx-runtime").JSX.Element;
