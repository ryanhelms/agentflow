import type { ReactNode } from 'react';
/** KPI donut with square (butt) segment ends. Segments sweep in on mount. */
export declare function Donut({ segments, size, thickness, children }: {
    segments: {
        label: string;
        value: number;
        color: string;
    }[];
    size?: number;
    thickness?: number;
    children?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
