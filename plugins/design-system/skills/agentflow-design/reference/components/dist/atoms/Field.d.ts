import type { InputHTMLAttributes, ReactNode, SelectHTMLAttributes, TextareaHTMLAttributes } from 'react';
/**
 * Text input. `prefixLabel` puts a visible label ("From", "To") inside the keyline, ahead of the control.
 * `fit` sizes the control to its content instead of the row (toolbars, inline filters); `width` fixes it.
 */
export declare function Input({ mono, fit, width, prefixLabel, className, style, ...rest }: Omit<InputHTMLAttributes<HTMLInputElement>, 'width'> & {
    mono?: boolean;
    fit?: boolean;
    width?: number | string;
    prefixLabel?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
export declare function Textarea({ className, ...rest }: TextareaHTMLAttributes<HTMLTextAreaElement>): import("react/jsx-runtime").JSX.Element;
/** Native select. `fit` sizes it to the selected option (capped) instead of the full row; `width` fixes it (also inside a Toolbar). */
export declare function Select({ className, children, fit, width, style, ...rest }: SelectHTMLAttributes<HTMLSelectElement> & {
    children: ReactNode;
    fit?: boolean;
    width?: number | string;
}): import("react/jsx-runtime").JSX.Element;
export declare function Checkbox({ label, ...rest }: InputHTMLAttributes<HTMLInputElement> & {
    label?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
/** Square toggle switch. */
export declare function Switch({ checked, onChange, label, disabled }: {
    checked: boolean;
    onChange: (v: boolean) => void;
    label?: ReactNode;
    disabled?: boolean;
}): import("react/jsx-runtime").JSX.Element;
