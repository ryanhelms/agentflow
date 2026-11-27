/** The JSON Schema properties supported by SchemaForm. */
export interface JsonSchema {
    type?: string | string[];
    title?: string;
    description?: string;
    default?: unknown;
    enum?: unknown[];
    properties?: Record<string, JsonSchema>;
    required?: string[];
    anyOf?: JsonSchema[];
    $ref?: string;
    $defs?: Record<string, JsonSchema>;
    additionalProperties?: boolean | JsonSchema;
    items?: JsonSchema;
    'x-secret'?: boolean;
}
