import { type AllHTMLAttributes } from 'react';
/** Spacing step of a flex gap: `gap-N` is `--s-N`. 2 is what `Row` and `Stack` have without the prop. */
export type Gap = 0 | 1 | 2 | 3 | 4 | 5 | 6;
/** Every attribute an HTML element takes (events, `role`, `title`, `aria-*`, `data-*`, `colSpan`, `htmlFor`), minus the names the primitives reuse. */
export type ElementProps = Omit<AllHTMLAttributes<HTMLElement>, 'as' | 'wrap'>;
export interface BoxProps extends ElementProps {
    /** The element to render: `div` unless a semantic one fits (section, header, footer, nav, aside, ul, li, dl, i, span). */
    as?: keyof HTMLElementTagNameMap;
    /** `gap-N`: the gap between children of a flex or grid box. */
    gap?: Gap;
    /** Cross-axis alignment (`ai-*`). A `Row` centres and a `Stack` stretches without it. */
    align?: 'start' | 'center' | 'end' | 'baseline' | 'stretch';
    /** Main-axis distribution (`jc-*`). */
    justify?: 'start' | 'center' | 'end' | 'between';
    /** Let a `Row` wrap onto more lines. */
    wrap?: boolean;
    /** Take the free space of a flex parent (`flex: 1; min-width: 0`). */
    grow?: boolean;
}
/**
 * Plain block: the element a page or organism class sits on (`<Box className="pg-x-card">`). Renders
 * exactly `<as class="…">` with the attributes it is given; `style` is for dynamic values only.
 */
export declare const Box: import("react").ForwardRefExoticComponent<BoxProps & import("react").RefAttributes<HTMLElement>>;
/** Flex row, centred on the cross axis, 8px gap (`.row`). */
export declare const Row: import("react").ForwardRefExoticComponent<BoxProps & import("react").RefAttributes<HTMLElement>>;
/** Flex column, 8px gap (`.col`). */
export declare const Stack: import("react").ForwardRefExoticComponent<BoxProps & import("react").RefAttributes<HTMLElement>>;
/** The spacer of a row or toolbar: an empty span that takes the free space and pushes what follows to the end. */
export declare function Spacer(): import("react/jsx-runtime").JSX.Element;
/** Keylined grid of equal columns (`.grid cols-N`); children are its cells. Collapses below 1100px. */
export declare function Grid({ cols, className, ...rest }: ElementProps & {
    cols: 2 | 3 | 4 | 6;
}): import("react/jsx-runtime").JSX.Element;
declare const SPLIT: {
    readonly '2:1': "pg-split";
    readonly halves: "pg-halves";
    readonly thirds: "pg-thirds";
};
/** Keylined row of panels: two thirds and one third (`2:1`), `halves` or `thirds`. One column below 1100px. */
export declare function Split({ ratio, className, ...rest }: ElementProps & {
    ratio?: keyof typeof SPLIT;
}): import("react/jsx-runtime").JSX.Element;
export {};
