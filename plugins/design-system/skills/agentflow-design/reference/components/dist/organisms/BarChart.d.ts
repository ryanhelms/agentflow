export interface BarKey {
    key: string;
    label: string;
    color: string;
}
/**
 * Stacked vertical bars with square ends on a round-number axis (0 / 10 / 20 / 30), one faint gridline
 * per tick. Bars grow from the baseline on mount; hover reads out every segment.
 */
export declare function BarChart({ data, keys, height, yFormat, onPick }: {
    data: {
        label: string;
        values: Record<string, number>;
    }[];
    keys: BarKey[];
    height?: number;
    yFormat?: (v: number) => string;
    onPick?: (index: number) => void;
}): import("react/jsx-runtime").JSX.Element;
