// Fixture output crosses a process boundary: missing data must fail the test.
export function required<T>(value: T | null | undefined): T {
  if (value === null || value === undefined) {
    throw new Error("fixture omitted a required value");
  }
  return value;
}
