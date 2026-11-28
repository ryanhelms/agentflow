import type { ReactNode } from 'react';
export declare function DashboardTemplate({ header, kpis, children }: {
    header: ReactNode;
    kpis?: ReactNode;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
export declare function ListTemplate({ header, kpis, toolbar, children, footer, drawer, above }: {
    header: ReactNode;
    kpis?: ReactNode;
    toolbar?: ReactNode;
    children: ReactNode;
    footer?: ReactNode;
    drawer?: ReactNode;
    above?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
/**
 * `asideWidth` (px, default 340) sets the aside column; `asideFlush` drops the aside padding for a
 * stack of panels that runs edge to edge. Below 1100px the aside is hidden either way.
 */
export declare function DetailTemplate({ header, kpis, tabs, children, aside, drawer, asideWidth, asideFlush }: {
    header: ReactNode;
    kpis?: ReactNode;
    tabs?: ReactNode;
    children: ReactNode;
    aside?: ReactNode;
    drawer?: ReactNode;
    asideWidth?: number;
    asideFlush?: boolean;
}): import("react/jsx-runtime").JSX.Element;
export declare function WorkbenchTemplate({ header, kpis, toolbar, left, children, right, leftWidth, rightWidth }: {
    header: ReactNode;
    kpis?: ReactNode;
    toolbar?: ReactNode;
    left?: ReactNode;
    children: ReactNode;
    right?: ReactNode;
    leftWidth?: number;
    rightWidth?: number;
}): import("react/jsx-runtime").JSX.Element;
export declare function CanvasTemplate({ header, toolbar, children, hud, side }: {
    header: ReactNode;
    toolbar?: ReactNode;
    children: ReactNode;
    hud?: ReactNode;
    side?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
