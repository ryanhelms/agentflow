import type { Tone } from '../lib/tone.js';
/**
 * One row of square cells: status history (uptime, delivery outcome per period). Newest on the right.
 * `fill` stretches the cells across the container (`cell` becomes the minimum width). `stretch` lets the
 * cells grow to the strip's own height (the usual height becomes the minimum).
 * A `neutral` tone or `empty` draws an outlined "no data" slot.
 */
export declare function HeatStrip({ cells, cell, gap, onPick, fill, height, stretch }: {
    cells: {
        tone: Tone;
        title?: string;
        empty?: boolean;
    }[];
    cell?: number;
    gap?: number;
    onPick?: (index: number) => void;
    fill?: boolean;
    height?: number;
    stretch?: boolean;
}): import("react/jsx-runtime").JSX.Element;
/**
 * Rows × columns intensity grid (e.g. deliveries per metric per hour). `value` 0..1 drives the Blue ramp.
 * `fill` stretches the columns across the panel (`cell` becomes the minimum width). Column labels are
 * horizontal, one every `labelEvery` columns. A cell with `empty` (or v = 0 and no tone) is "no data".
 */
export declare function HeatGrid({ rows, cols, cell, onPick, fill, labelEvery, cellHeight }: {
    rows: {
        label: string;
        values: {
            v: number;
            title?: string;
            tone?: Tone;
            empty?: boolean;
        }[];
    }[];
    cols: string[];
    cell?: number;
    onPick?: (row: number, col: number) => void;
    fill?: boolean;
    labelEvery?: number;
    cellHeight?: number;
}): import("react/jsx-runtime").JSX.Element;
