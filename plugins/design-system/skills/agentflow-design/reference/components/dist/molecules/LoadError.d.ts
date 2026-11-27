import type { ReactNode } from 'react';
/**
 * A failed load: "Could not load {what}" over the error message. `onRetry` adds a Retry button
 * (`retryIcon` puts the refresh glyph on it).
 */
export declare function LoadError({ what, error, onRetry, retryIcon }: {
    what: string;
    error: ReactNode;
    onRetry?: () => void;
    retryIcon?: boolean;
}): import("react/jsx-runtime").JSX.Element;
