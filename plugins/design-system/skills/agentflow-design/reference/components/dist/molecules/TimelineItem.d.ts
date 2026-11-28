import type { ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/** One event on a vertical keyline: square node, time, title, meta. Wrap a run of them in `Timeline`. */
export declare function TimelineItem({ tone, time, title, meta, children, onClick }: {
    tone?: Tone;
    time: ReactNode;
    title: ReactNode;
    meta?: ReactNode;
    children?: ReactNode;
    onClick?: () => void;
}): import("react/jsx-runtime").JSX.Element;
/** The vertical keyline a run of `TimelineItem`s sits on. */
export declare function Timeline({ children, className }: {
    children: ReactNode;
    className?: string;
}): import("react/jsx-runtime").JSX.Element;
