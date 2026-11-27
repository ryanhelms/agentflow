import type { ReactNode } from 'react';
/** Backdrop for a modal surface: dims and blurs the console; a press on the backdrop itself calls `onClose`. `top` pins the content to the upper third. */
export declare function Scrim({ onClose, top, children }: {
    onClose: () => void;
    top?: boolean;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
