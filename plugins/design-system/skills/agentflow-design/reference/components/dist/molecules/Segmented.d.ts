import type { ReactNode } from 'react';
import { type IconName } from '../atoms/index.js';
/** Segmented control for 2-5 mutually exclusive views or ranges. `fill` stretches it across its container in equal segments. */
export declare function Segmented<T extends string>({ options, value, onChange, fill }: {
    options: {
        value: T;
        label?: ReactNode;
        icon?: IconName;
        title?: string;
    }[];
    value: T;
    onChange: (v: T) => void;
    fill?: boolean;
}): import("react/jsx-runtime").JSX.Element;
