/** The message of whatever a catch holds: an Error's, else its text. */
export function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
