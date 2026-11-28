import { type ReactNode } from 'react';
/** One step of the trail. `to` makes it a link back; `subtle` is a group that is not a page; `mono` is a key (the leaf of a sub-page). */
export interface BreadcrumbItem {
    label: ReactNode;
    to?: string;
    subtle?: boolean;
    mono?: boolean;
}
/** Where you are, left to right, steps joined by chevrons. A step that is none of link, subtle or mono is the current page, in bold. */
export declare function Breadcrumb({ items }: {
    items: BreadcrumbItem[];
}): import("react/jsx-runtime").JSX.Element;
