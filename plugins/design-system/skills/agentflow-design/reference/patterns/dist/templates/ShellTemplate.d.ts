import { type ReactNode } from 'react';
/**
 * App shell layout: rail | top bar | routed page | status bar, plus a slot for overlays (palette,
 * toasts). Regions only: the app fills the slots and owns the global shortcuts.
 * The page region re-mounts when `routeKey` changes, so pages animate in on route change, and shows
 * a skeleton while a lazy page chunk loads.
 */
export declare function ShellTemplate({ rail, top, status, overlays, routeKey, children }: {
    rail: ReactNode;
    top: ReactNode;
    status: ReactNode;
    overlays?: ReactNode;
    routeKey?: string;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
