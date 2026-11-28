/** Column chooser for a grid: one checkbox per column name; the last visible column cannot be hidden. */
export declare function ColumnsMenu({ columns, hidden, onToggle, onShowAll, title }: {
    columns: string[];
    hidden: string[];
    onToggle: (name: string) => void;
    onShowAll: () => void;
    title?: string;
}): import("react/jsx-runtime").JSX.Element;
