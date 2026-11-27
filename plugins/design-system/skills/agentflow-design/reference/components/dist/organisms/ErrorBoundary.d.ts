import { Component, type ReactNode } from 'react';
/** Catches a render error in its children (a failed WebGL start, a broken widget) and renders `fallback(message)` instead. Remount it with a `key` to retry. */
export declare class ErrorBoundary extends Component<{
    fallback: (error: string) => ReactNode;
    children: ReactNode;
}, {
    error: string | null;
}> {
    state: {
        error: string | null;
    };
    static getDerivedStateFromError(e: unknown): {
        error: string;
    };
    render(): ReactNode;
}
