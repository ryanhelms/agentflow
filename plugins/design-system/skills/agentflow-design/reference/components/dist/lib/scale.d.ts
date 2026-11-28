/** Axis scale helpers shared by the charts: round tick values instead of fractions of the data max. */
export interface NiceScale {
    lo: number;
    hi: number;
    step: number;
    ticks: number[];
}
/** Round `raw` up to the nearest 1, 2 or 5 × 10^n. */
export declare function niceStep(raw: number): number;
/**
 * Nice-number scale covering [min, max] with about `target` intervals: bounds snap outward to a
 * multiple of the step, so ticks read 0 / 10 / 20 / 30 rather than 0 / 15 / 29.
 * `integer` keeps the step at 1 or more (counts). Ticks are rounded to kill float noise.
 */
export declare function niceScale(min: number, max: number, target?: number, integer?: boolean): NiceScale;
