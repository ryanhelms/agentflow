import type { AriaAttributes, MouseEventHandler, ReactNode } from 'react';
/**
 * One row of a dense, keylined list (a picker, a side list, cross-links): children in a centred flex
 * row. `to` makes it a router link, `onClick` a button, neither a plain row. `selected` marks the
 * current one. `className` adds page classes after `pg-list-row`. `button` renders a button even with no
 * handler (an inert current row); `aria-*` attributes pass through.
 */
export declare function ListRow({ to, onClick, button, selected, title, disabled, className, children, ...aria }: {
    to?: string;
    onClick?: MouseEventHandler<HTMLButtonElement>;
    button?: boolean;
    selected?: boolean;
    title?: string;
    disabled?: boolean;
    className?: string;
    children: ReactNode;
} & AriaAttributes): import("react/jsx-runtime").JSX.Element;
