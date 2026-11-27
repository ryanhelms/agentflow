import type { CSSProperties, ReactNode } from 'react';
/**
 * Panel: the keylined surface every block of content sits in. Header = eyebrow + title + actions.
 * `flush` removes body padding (tables, charts that run edge to edge). `corners` adds blueprint markers.
 * `meta` is a short note beside the title; a bare number renders as a count badge.
 * `fill` makes the body a flex column, so its one child can take the panel's full height.
 * `stacked` keeps only the bottom keyline: a panel in an edge-to-edge stack (an aside, a workbench column).
 */
export declare function Panel({ title, eyebrow, actions, footer, flush, fill, stacked, corners, className, style, children, meta }: {
    title?: ReactNode;
    eyebrow?: ReactNode;
    actions?: ReactNode;
    footer?: ReactNode;
    flush?: boolean;
    fill?: boolean;
    stacked?: boolean;
    corners?: boolean;
    className?: string;
    style?: CSSProperties;
    children?: ReactNode;
    meta?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
