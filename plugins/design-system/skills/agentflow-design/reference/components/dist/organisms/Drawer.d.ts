import type { ReactNode } from 'react';
/** Right-hand blade for record detail and edit forms. Esc closes. Does not block the page behind it. */
export declare function Drawer({ open, onClose, title, eyebrow, actions, footer, width, children }: {
    open: boolean;
    onClose: () => void;
    title: ReactNode;
    eyebrow?: ReactNode;
    actions?: ReactNode;
    footer?: ReactNode;
    width?: number;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element | null;
