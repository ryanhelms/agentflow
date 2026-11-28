/** Rows-per-page select ("50 / page") for a table footer or toolbar. `onChange` gets the option value as text. */
export declare function PageSizeSelect({ value, sizes, onChange }: {
    value: number | string;
    sizes: readonly number[];
    onChange: (v: string) => void;
}): import("react/jsx-runtime").JSX.Element;
