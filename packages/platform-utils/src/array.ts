export function findLast<T>(
  items: readonly T[],
  predicate: (item: T, index: number, items: readonly T[]) => boolean,
): T | undefined {
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index]
    if (predicate(item, index, items)) return item
  }
  return undefined
}
