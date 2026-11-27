/** Inline sparkline. `tone` picks the stroke; the last point gets a square node. */
export declare function Spark({ values, w, h, tone, area }: {
    values: (number | null)[];
    w?: number;
    h?: number;
    tone?: 'accent' | 'ok' | 'warn' | 'bad' | 'muted';
    area?: boolean;
}): import("react/jsx-runtime").JSX.Element;
