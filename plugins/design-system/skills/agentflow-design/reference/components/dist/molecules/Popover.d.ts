import { type ReactNode } from 'react';
import { type IconName } from '../atoms/index.js';
/** Small menu anchored under its trigger button (right edges aligned). Closes on outside click and Escape. */
export declare function Popover({ label, icon, children }: {
    label: ReactNode;
    icon?: IconName;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
