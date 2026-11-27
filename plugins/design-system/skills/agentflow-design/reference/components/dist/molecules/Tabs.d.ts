import type { ReactNode } from 'react';
import { type IconName } from '../atoms/index.js';
export interface TabItem {
    id: string;
    label: ReactNode;
    count?: number | string;
    icon?: IconName;
}
/** Underlined tabs; the active marker is a Blue keyline that draws in. */
export declare function Tabs({ items, value, onChange }: {
    items: TabItem[];
    value: string;
    onChange: (id: string) => void;
}): import("react/jsx-runtime").JSX.Element;
