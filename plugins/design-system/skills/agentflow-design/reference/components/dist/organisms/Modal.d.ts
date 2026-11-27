import { type ReactNode } from 'react';
export declare function Modal({ open, onClose, title, children, footer, width }: {
    open: boolean;
    onClose: () => void;
    title: ReactNode;
    children: ReactNode;
    footer?: ReactNode;
    width?: number;
}): import("react/jsx-runtime").JSX.Element | null;
/**
 * Confirmation with an optional required reason. Replaces the native confirm()/prompt() the
 * current console uses for delete, reject and restore.
 */
export declare function ConfirmDialog({ open, onClose, onConfirm, title, children, confirmLabel, danger, reason }: {
    open: boolean;
    onClose: () => void;
    onConfirm: (reason: string) => void;
    title: ReactNode;
    children?: ReactNode;
    confirmLabel?: string;
    danger?: boolean;
    reason?: {
        label: string;
        required?: boolean;
        placeholder?: string;
    };
}): import("react/jsx-runtime").JSX.Element;
