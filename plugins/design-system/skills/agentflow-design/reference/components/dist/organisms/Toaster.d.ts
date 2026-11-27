import type { Tone } from '../lib/tone.js';
export interface ToastMessage {
    id: number;
    tone: Tone;
    title: string;
    detail?: string;
}
/** Toast stack, bottom-right. The shell feeds it from session.toast(); a click dismisses. */
export declare function Toaster({ toasts, onDismiss }: {
    toasts: ToastMessage[];
    onDismiss: (id: number) => void;
}): import("react/jsx-runtime").JSX.Element;
