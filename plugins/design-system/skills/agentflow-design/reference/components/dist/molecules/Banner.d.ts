import type { ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/** Inline message with a tone bar on the left. */
export declare function Banner({ tone, title, children, actions }: {
    tone?: Tone;
    title?: ReactNode;
    children?: ReactNode;
    actions?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
