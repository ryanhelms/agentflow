import type { ReactNode } from 'react';
/** Page heading with an optional description, actions, and metadata. */
export declare function PageHeader({ title, description, eyebrow, actions, meta }: {
    title: ReactNode;
    description?: ReactNode;
    eyebrow?: ReactNode;
    actions?: ReactNode;
    meta?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
