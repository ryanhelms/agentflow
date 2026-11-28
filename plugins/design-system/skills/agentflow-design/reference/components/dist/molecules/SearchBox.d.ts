import type { Ref } from 'react';
/** Filter box. `kbd` shows the hotkey hint; `inputRef` reaches the input so the page hook can focus it on that hotkey. */
export declare function SearchBox({ value, onChange, placeholder, kbd, width, autoFocus, inputRef }: {
    value: string;
    onChange: (v: string) => void;
    placeholder?: string;
    kbd?: string;
    width?: number | string;
    autoFocus?: boolean;
    inputRef?: Ref<HTMLInputElement>;
}): import("react/jsx-runtime").JSX.Element;
