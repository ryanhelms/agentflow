import { type CSSProperties, type ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
export interface Column<T> {
    key: string;
    header: ReactNode;
    width?: number | string;
    align?: 'left' | 'right' | 'center';
    /** Cells render in DM Mono (numbers, keys, timestamps). */
    mono?: boolean;
    /** Enable client-side sorting; return the comparable value. */
    sortValue?: (row: T) => string | number | null | undefined;
    render?: (row: T, index: number) => ReactNode;
    /** A click inside the cell's content (a link, a switch, row actions) does not also select the row. A function limits it to some rows. */
    stopClick?: boolean | ((row: T) => boolean);
}
export interface DataTableProps<T> {
    rows: T[];
    columns: Column<T>[];
    rowKey: (row: T) => string;
    onRowClick?: (row: T) => void;
    selectedKey?: string | null;
    defaultSort?: {
        key: string;
        dir: 'asc' | 'desc';
    };
    /** Left edge tone bar per row (status at a glance). */
    rowTone?: (row: T) => Tone | undefined;
    /** Keys that should flash (new or changed rows in a live feed). */
    flashKeys?: Set<string>;
    loading?: boolean;
    empty?: ReactNode;
    maxHeight?: number | string;
    style?: CSSProperties;
    /** Animate rows in on first paint. */
    animate?: boolean;
    /** compact: 8px cell gutters instead of 12px (the table's outer edges keep 12px), for tables with many columns. */
    density?: 'default' | 'compact';
}
/**
 * Dense data grid: sentence-case semibold headers over a 1.5px rule, hairline row dividers,
 * sticky header, client-side sort, keyboard row navigation (↑ ↓ Enter) and a scan-line hover.
 * Edges that hide more columns or rows carry an accent cue; right-aligned headers put the sort icon first
 * so values line up under the header text.
 */
export declare function DataTable<T>({ rows, columns, rowKey, onRowClick, selectedKey, defaultSort, rowTone, flashKeys, loading, empty, maxHeight, style, animate, density }: DataTableProps<T>): import("react/jsx-runtime").JSX.Element;
