import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { type IconName } from './Icon.js';
export interface ButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'children'> {
    /** primary: brand Blue, hover goes Spearmint with a Midnight label (one per view). */
    /** link: an inline text link that is a button (it acts instead of navigating); takes the type of its context, `size` does not apply. */
    variant?: 'primary' | 'secondary' | 'ghost' | 'danger' | 'link';
    size?: 'sm' | 'md';
    icon?: IconName;
    iconRight?: IconName;
    loading?: boolean;
    /** Renders a router link styled as a button. */
    to?: string;
    children?: ReactNode;
}
export declare function Button({ variant, size, icon, iconRight, loading, to, children, className, ...rest }: ButtonProps): import("react/jsx-runtime").JSX.Element;
