import type { ReactNode } from 'react';
export interface Step {
    label: ReactNode;
    detail?: ReactNode;
    state: 'done' | 'current' | 'todo' | 'failed';
}
/** Horizontal process steps joined by keylines (publish flow, pipeline stages). Label and detail stay on one line each; the full text is the tooltip. */
export declare function Stepper({ steps }: {
    steps: Step[];
}): import("react/jsx-runtime").JSX.Element;
