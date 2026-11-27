import { type ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
export interface FlowNode {
    id: string;
    label: ReactNode;
    sub?: ReactNode;
    tone?: Tone;
    value?: ReactNode;
    dim?: boolean;
}
/** `weight` is the column's share of the width (default 1): give columns with long labels more room. */
export interface FlowColumn {
    title: ReactNode;
    nodes: FlowNode[];
    weight?: number;
}
export interface FlowLink {
    from: string;
    to: string;
    tone?: Tone;
    idle?: boolean;
}
/**
 * Left-to-right flow graph: columns of square nodes joined by horizontal-tangent curves that leave and
 * enter a node on separate ports, so fan-in and fan-out stay readable in a narrow gutter. A link between
 * two nodes of the same column runs as a square bracket down the column's right side. Active links
 * carry animated dashes and a travelling packet, so the direction of data is always visible.
 * Labels wrap to two lines rather than truncating; `weight` on a column widens it.
 * Hover or select a node to light its whole upstream and downstream path and dim the rest.
 * `minWidth` keeps the columns readable in a narrow parent: put the diagram in a wrapper that scrolls sideways.
 */
export declare function FlowDiagram({ columns, links, selected, onSelect, minHeight, minWidth }: {
    columns: FlowColumn[];
    links: FlowLink[];
    selected?: string | null;
    onSelect?: (id: string | null) => void;
    minHeight?: number;
    minWidth?: number;
}): import("react/jsx-runtime").JSX.Element;
