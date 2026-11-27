import { type AnchorHTMLAttributes, type ComponentType, type ReactNode } from 'react';
export interface LinkAdapterProps extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> {
    to: string;
    nav?: boolean;
    end?: boolean;
    activeClassName?: string;
}
export declare const LinkAdapterContext: import("react").Context<ComponentType<LinkAdapterProps> | null>;
/** Supply an application's routing adapter for links with a `to` prop. */
export declare function LinkAdapterProvider({ adapter, children }: {
    adapter: ComponentType<LinkAdapterProps>;
    children: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
