import type { ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/** Status pill: square, keylined, always labelled (RAG is never colour alone). `pulse` marks a live signal. `to` makes it a router link. */
export declare function Pill({ tone, dot, pulse, children, title, solid, to }: {
    tone?: Tone;
    dot?: boolean;
    pulse?: boolean;
    children: ReactNode;
    title?: string;
    solid?: boolean;
    to?: string;
}): import("react/jsx-runtime").JSX.Element;
