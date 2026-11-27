/** Animate a number towards its target, honoring reduced-motion preferences. */
export declare function useCountUp(target: number, duration?: number): number;
/** Handle a keyboard shortcut while enabled. Ignore typing in inputs unless a modifier is required. */
export declare function useHotkey(combo: string, fn: (e: KeyboardEvent) => void, enabled?: boolean): void;
export declare function useReducedMotion(): boolean;
