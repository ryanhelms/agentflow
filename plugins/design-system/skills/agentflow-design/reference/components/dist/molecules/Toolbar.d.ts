import type { ReactNode } from 'react';
/**
 * Horizontal strip of filters and actions above a table or canvas. Use `<Spacer />` as a spacer.
 * `end` is a trailing group that stays together and pinned right, also when the row wraps. Inside, group
 * controls with `ToolbarGroup` and put long chip rows in `ChipScroll` so the strip wraps between groups.
 */
export declare function Toolbar({ children, end, className }: {
    children: ReactNode;
    end?: ReactNode;
    className?: string;
}): import("react/jsx-runtime").JSX.Element;
/** Controls that stay on one line together when the toolbar wraps. `as="span"` where the group sits in inline content. */
export declare function ToolbarGroup({ children, as: As, className, title }: {
    children: ReactNode;
    as?: 'div' | 'span';
    className?: string;
    title?: string;
}): import("react/jsx-runtime").JSX.Element;
/** Keyline between two groups of a toolbar. */
export declare function ToolbarSep(): import("react/jsx-runtime").JSX.Element;
/** A chip row that scrolls sideways instead of wrapping. */
export declare function ChipScroll({ children, className }: {
    children: ReactNode;
    className?: string;
}): import("react/jsx-runtime").JSX.Element;
