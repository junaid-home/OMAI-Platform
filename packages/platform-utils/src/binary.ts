export namespace Binary {
  export function search<T>(items: T[], id: string, identify: (item: T) => string): { found: boolean; index: number } {
    let left = 0
    let right = items.length - 1

    while (left <= right) {
      const middle = Math.floor((left + right) / 2)
      const middleID = identify(items[middle])
      if (middleID === id) return { found: true, index: middle }
      if (middleID < id) left = middle + 1
      else right = middle - 1
    }

    return { found: false, index: left }
  }

  export function insert<T>(items: T[], item: T, identify: (item: T) => string): T[] {
    const id = identify(item)
    let left = 0
    let right = items.length

    while (left < right) {
      const middle = Math.floor((left + right) / 2)
      if (identify(items[middle]) < id) left = middle + 1
      else right = middle
    }

    items.splice(left, 0, item)
    return items
  }
}
