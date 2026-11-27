import type { AnchorHTMLAttributes } from 'react';
export interface LinkProps extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> {
    /** A route in the console: renders a router link. */
    to?: string;
    /** Any other address: renders a plain anchor. `external` opens it in a new tab. */
    href?: string;
    external?: boolean;
    /** Navigation link: knows whether its route is the current one (`aria-current`, and `activeClassName` when set). */
    nav?: boolean;
    /** With `nav`: match the route exactly, not its sub-routes. */
    end?: boolean;
    /** With `nav`: class added while the route is current. Without it the router's own `active` class is added. */
    activeClassName?: string;
}
/** Link: the one anchor. Unstyled beyond the base anchor colour; `className` gives it a component or page class. */
export declare function Link({ to, href, external, nav, end, activeClassName, className, children, ...rest }: LinkProps): import("react/jsx-runtime").JSX.Element;
