import { type ReactNode } from 'react';
/** Page title with an optional description. Existing layouts can also supply an eyebrow. */
export declare function PageTitle({ title, description, eyebrow }: {
    title: ReactNode;
    description?: ReactNode;
    eyebrow?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
/** Head of a drawer: optional eyebrow over a one-line title, `actions` (previous / next), then Close. */
export declare function DrawerHeader({ title, eyebrow, actions, onClose }: {
    title: ReactNode;
    eyebrow?: ReactNode;
    actions?: ReactNode;
    onClose: () => void;
}): import("react/jsx-runtime").JSX.Element;
/** Head of a modal: the title and Close. */
export declare function ModalHeader({ title, onClose }: {
    title: ReactNode;
    onClose: () => void;
}): import("react/jsx-runtime").JSX.Element;
