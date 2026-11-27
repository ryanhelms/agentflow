import type { ReactNode } from 'react';
import { type IconName } from '../atoms/index.js';
export declare function EmptyState({ icon, title, children, action }: {
    icon?: IconName;
    title: ReactNode;
    children?: ReactNode;
    action?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
