import type { CSSProperties, ReactNode } from 'react';
import type { Tone } from '../lib/tone.js';
/**
 * One node of a flow diagram: status dot, a label that wraps to two lines, a mono value and a mono
 * sub-line. `dim` fades it while another path is lit, `off` draws it dashed (disabled), `selected`
 * marks the picked node. `id` is written as `data-flow-id`, which the diagram measures to route links.
 */
export declare function FlowNodeCard({ id, label, sub, value, tone, pulse, dim, off, selected, style, onClick, onHover }: {
    id: string;
    label: ReactNode;
    sub?: ReactNode;
    value?: ReactNode;
    tone?: Tone;
    pulse?: boolean;
    dim?: boolean;
    off?: boolean;
    selected?: boolean;
    style?: CSSProperties;
    onClick?: () => void;
    onHover?: (over: boolean) => void;
}): import("react/jsx-runtime").JSX.Element;
