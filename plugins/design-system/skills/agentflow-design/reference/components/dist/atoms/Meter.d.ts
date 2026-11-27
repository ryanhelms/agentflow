import type { Tone } from '../lib/tone.js';
/** Thin horizontal meter, 0..1. Segmented ticks keep it sharp. */
export declare function Meter({ value, tone, w }: {
    value: number;
    tone?: Tone;
    w?: number | string;
}): import("react/jsx-runtime").JSX.Element;
