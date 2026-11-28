import type { ElementProps } from './Box.js';
declare const TONE: {
    readonly muted: "muted";
    readonly subtle: "subtle";
    readonly ok: "c-ok";
    readonly warn: "c-warn";
    readonly bad: "c-bad";
    readonly info: "tone-info";
    readonly accent: "c-accent";
};
declare const WEIGHT: {
    readonly regular: "fw-400";
    readonly medium: "fw-500";
    readonly semibold: "fw-600";
    readonly bold: "fw-700";
};
export interface TextProps extends ElementProps {
    /** The element to render: `span` unless the text is a paragraph, heading, label, list item or term. */
    as?: 'span' | 'p' | 'div' | 'strong' | 'b' | 'em' | 'i' | 'small' | 'label' | 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6' | 'li' | 'dt' | 'dd' | 'code' | 'pre' | 'time' | 'abbr' | 'sub' | 'sup' | 'figcaption' | 'legend' | 'th' | 'td';
    /** Text style from the type ramp (`t-*` in tokens/base.css). Without it the text takes the type of its context. */
    variant?: 'h1' | 'h2' | 'h3' | 'h4' | 'body' | 'small' | 'eyebrow' | 'caption' | 'label' | 'mono' | 'mono-lg' | 'mono-xl';
    /** Colour: `muted` and `subtle` step the text back; the rest are status colours and always sit beside a label. */
    tone?: keyof typeof TONE;
    weight?: keyof typeof WEIGHT;
    /** One line, cut with an ellipsis. */
    truncate?: boolean;
    /** One line that also keeps its width in a flex row. */
    nowrap?: boolean;
    /** Clamp to two lines, breaking anywhere (labels that must keep their tail). */
    clamp?: 2;
    /** Take the free space of a flex parent. */
    grow?: boolean;
    /** For screen readers only: visually hidden (`sr-only`). */
    srOnly?: boolean;
}
/**
 * Text: every run of copy outside an atom of its own. Maps props to the type and colour classes, so
 * `<Text variant="small" tone="muted">` is `<span class="t-small muted">`. `className` adds the
 * component or page class the text is styled by.
 */
export declare const Text: import("react").ForwardRefExoticComponent<TextProps & import("react").RefAttributes<HTMLElement>>;
export {};
