import type { ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/**
 * One cell of the status bar: a quiet label, the value in DM Mono and a quiet unit ("Ingest 12.4k rows/min").
 * `tone` puts a status dot first (`pulse` while live) and `text` is the plain word beside it;
 * children follow the value (a control). Any part can be left out.
 */
export declare function StatusItem({ label, value, unit, text, tone, pulse, title, className, children }: {
    label?: ReactNode;
    value?: ReactNode;
    unit?: ReactNode;
    text?: ReactNode;
    tone?: Tone;
    pulse?: boolean;
    title?: string;
    className?: string;
    children?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
