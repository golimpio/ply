export interface RawValue {
  /** Relative to src, with / separators. */
  file: string;
  line: number;
  message: string;
}

/** The raw colour, font, font size, radius and motion values (and uses of primitives) of sources given as { path: text }. */
export function checkSources(sources: Record<string, string>): RawValue[];

/** The .svelte and .css files under `dir` (default: the app's src), read from disk, as checkSources takes them. */
export function readSources(dir?: string): Record<string, string>;

/** checkSources(readSources(dir)). */
export function checkTokens(dir?: string): RawValue[];
