import type { ReactNode } from 'react';
/**
 * Dense key/value grid. Values are DM Mono by default (specs) and truncate on one line.
 * Per row: `wrap` lets a long value break, `full` spans every column, `stack` puts the value under its label.
 */
export declare function KeyValue({ items, cols }: {
    items: {
        k: ReactNode;
        v: ReactNode;
        mono?: boolean;
        wrap?: boolean;
        full?: boolean;
        stack?: boolean;
    }[];
    cols?: 1 | 2 | 3;
}): import("react/jsx-runtime").JSX.Element;
