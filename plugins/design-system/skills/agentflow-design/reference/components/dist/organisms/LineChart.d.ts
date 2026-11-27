export interface LineSeries {
    id: string;
    label: string;
    color?: string;
    values: (number | null)[];
    /** Optional min/max envelope drawn as a band behind the line. */
    band?: {
        min: (number | null)[];
        max: (number | null)[];
    };
    dashed?: boolean;
}
/**
 * Time-series chart: keyline grid on round-number ticks, DM Mono axes (unit in the gutter above the
 * y axis, first and last x points always labelled), area under the first series, optional min/max band,
 * threshold rules and a crosshair with a value read-out for every series. Lines draw in on mount.
 */
export declare function LineChart({ series, labels, height, yFormat, unit, thresholds, yMin, yMax, onPick }: {
    series: LineSeries[];
    labels: string[];
    height?: number;
    yFormat?: (v: number) => string;
    unit?: string;
    thresholds?: {
        value: number;
        label: string;
        tone?: 'warn' | 'bad' | 'ok';
    }[];
    yMin?: number;
    yMax?: number;
    onPick?: (index: number) => void;
}): import("react/jsx-runtime").JSX.Element;
