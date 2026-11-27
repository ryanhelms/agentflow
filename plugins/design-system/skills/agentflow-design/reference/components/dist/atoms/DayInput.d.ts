/**
 * Typed ISO day field (YYYY-MM-DD, UTC) with its label ("From", "To") inside the keyline, so date
 * bounds read like every other date in the console. `onCommit` fires when the text is a complete,
 * real day or has been cleared (`undefined`); a half-typed or invalid value turns red and reverts
 * to `value` on blur. `min` / `max` (ISO days, inclusive) bound it: a day outside them turns red and
 * is not committed while typing, and is clamped to the bound on blur or Enter, so From never passes To.
 */
export declare function DayInput({ label, value, onCommit, min, max, className }: {
    label: string;
    value: string;
    onCommit: (v: string | undefined) => void;
    min?: string;
    max?: string;
    className?: string;
}): import("react/jsx-runtime").JSX.Element;
