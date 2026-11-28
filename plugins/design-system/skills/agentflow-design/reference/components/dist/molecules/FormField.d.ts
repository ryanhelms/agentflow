import type { ReactNode } from 'react';
/** Label + control + hint/error. The control is passed as children. */
export declare function FormField({ label, hint, error, required, children, span }: {
    label: ReactNode;
    hint?: ReactNode;
    error?: ReactNode;
    required?: boolean;
    children: ReactNode;
    span?: 2 | 3;
}): import("react/jsx-runtime").JSX.Element;
/** Grid of form fields: as many 160px+ columns as fit, or a fixed `cols`. A `FormField` with `span` covers more than one. */
export declare function FormGrid({ cols, className, children }: {
    cols?: 2 | 3;
    className?: string;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
