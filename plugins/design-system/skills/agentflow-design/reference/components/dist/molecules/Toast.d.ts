import type { ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/** One notification: tone icon, title, optional detail and the timer line that runs out. A click dismisses it. */
export declare function Toast({ tone, title, detail, onDismiss }: {
    tone: Tone;
    title: ReactNode;
    detail?: ReactNode;
    onDismiss?: () => void;
}): import("react/jsx-runtime").JSX.Element;
