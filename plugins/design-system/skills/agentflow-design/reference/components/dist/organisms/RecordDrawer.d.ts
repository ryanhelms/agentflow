import { type ReactNode } from 'react';
/**
 * Record inspector blade: attributes in a fixed order, raw JSON, copy, previous/next and related links.
 */
export declare function RecordDrawer({ open, onClose, title, eyebrow, record, attributes, links, onPrev, onNext, children }: {
    open: boolean;
    onClose: () => void;
    title: ReactNode;
    eyebrow?: ReactNode;
    record: unknown;
    attributes: {
        k: ReactNode;
        v: ReactNode;
        mono?: boolean;
    }[];
    links?: ReactNode;
    onPrev?: () => void;
    onNext?: () => void;
    children?: ReactNode;
}): import("react/jsx-runtime").JSX.Element;
