import type { ReactNode } from 'react';
/** Toggleable filter chip with an optional count. */
export declare function Chip({ active, onClick, count, children, tone }: {
    active?: boolean;
    onClick?: () => void;
    count?: number | string;
    children: ReactNode;
    tone?: 'ok' | 'warn' | 'bad' | 'info';
}): import("react/jsx-runtime").JSX.Element;
