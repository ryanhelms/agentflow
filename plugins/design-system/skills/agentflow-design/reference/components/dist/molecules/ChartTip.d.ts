import type { CSSProperties, ReactNode } from 'react';
/** Hover readout of a chart: the x label over one row per series (colour key, name, value). `style` places it beside the pointer. */
export declare function ChartTip({ title, rows, style }: {
    title: ReactNode;
    rows: {
        key: string;
        color: string;
        label: ReactNode;
        value: ReactNode;
    }[];
    style?: CSSProperties;
}): import("react/jsx-runtime").JSX.Element;
