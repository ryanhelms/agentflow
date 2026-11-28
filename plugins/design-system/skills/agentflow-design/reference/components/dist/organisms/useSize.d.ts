/** Track an element's content box so SVG charts can render at real pixel sizes. */
export declare function useSize<T extends HTMLElement>(): [React.RefObject<T>, {
    w: number;
    h: number;
}];
