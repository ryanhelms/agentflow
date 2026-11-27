import type { ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/**
 * KPI tile: eyebrow label, the value (count-up) in DM Sans with tabular numerals, unit, delta and a sparkline.
 * The big number is not DM Mono: its slashed zero reads badly at 24px and the font has no plain alternate.
 * `text` replaces the number with a word or phrase, set in DM Sans; `unit` is a small suffix for either.
 * `size` steps a long value down ('md', 'sm') instead of letting it truncate. `to` makes the whole tile a drill-down link.
 */
export declare function StatTile({ label, value, format, unit, delta, deltaTone, spark, tone, to, hint, text, size }: {
    label: ReactNode;
    value?: number;
    text?: ReactNode;
    format?: (n: number) => string;
    unit?: ReactNode;
    delta?: string;
    deltaTone?: Tone;
    spark?: (number | null)[];
    tone?: Tone;
    to?: string;
    hint?: ReactNode;
    size?: 'lg' | 'md' | 'sm';
}): import("react/jsx-runtime").JSX.Element;
