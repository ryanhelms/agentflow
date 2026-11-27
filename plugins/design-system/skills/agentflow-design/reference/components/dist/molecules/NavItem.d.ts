import type { ReactNode } from 'react';
import { type IconName } from '../atoms/index.js';
/**
 * One navigation link of the rail: icon, label, then a live count (`badge`, shown while `n` is above 0)
 * or the `hotkey` hint on hover. Marks itself while its route is current (`end`: only on an exact match).
 * `collapsed` is the icon-only rail: the tooltip becomes the label instead of the `blurb`.
 */
export declare function NavItem({ to, end, icon, label, blurb, badge, hotkey, collapsed }: {
    to: string;
    end?: boolean;
    icon: IconName;
    label: string;
    blurb?: string;
    badge?: {
        n: number;
        tone: 'ok' | 'warn' | 'bad' | 'info';
        title?: string;
    };
    hotkey?: string;
    collapsed?: boolean;
}): import("react/jsx-runtime").JSX.Element;
/** A titled group of `NavItem`s in the rail. */
export declare function NavGroup({ title, children }: {
    title: ReactNode;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
