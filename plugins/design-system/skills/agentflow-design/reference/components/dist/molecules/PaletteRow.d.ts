import type { KeyboardEvent, ReactNode } from 'react';
import { type IconName } from '../atoms/index.js';
/** One result in the command palette: icon, label, a mono hint, then the kind as a `tag` and/or a `kbd` shortcut. `active` is the keyboard row. */
export declare function PaletteRow({ icon, label, hint, tag, kbd, active, onClick, onHover }: {
    icon: IconName;
    label: ReactNode;
    hint?: ReactNode;
    tag?: ReactNode;
    kbd?: string;
    active?: boolean;
    onClick?: () => void;
    onHover?: () => void;
}): import("react/jsx-runtime").JSX.Element;
/** The palette's query field: command icon, a bare input that takes focus on mount (`autoFocus`, on by default), and the key that closes it. */
export declare function PaletteInput({ value, onChange, onKeyDown, placeholder, kbd, autoFocus }: {
    value: string;
    onChange: (v: string) => void;
    onKeyDown?: (e: KeyboardEvent<HTMLInputElement>) => void;
    placeholder?: string;
    kbd?: string;
    autoFocus?: boolean;
}): import("react/jsx-runtime").JSX.Element;
