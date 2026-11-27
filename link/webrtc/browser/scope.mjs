// Rutis enters this scope when dispatching each call. Where native async
// context is unavailable, retain a specific invocation at the RPC boundary.
let active

export function bindInvocation(fn) {
  const scope = active
  if (!scope) return fn
  return (...args) => scope.storage.run(scope.value, () => fn(...args))
}

export class AsyncLocalStorage {
  #variable = globalThis.AsyncContext?.Variable && new globalThis.AsyncContext.Variable()

  getStore() {
    if (this.#variable) return this.#variable.get()
    for (let scope = active; scope; scope = scope.parent) {
      if (scope.storage === this) return scope.value
    }
  }

  run(value, action) {
    const parent = active
    active = { storage: this, value, parent }
    try {
      return this.#variable ? this.#variable.run(value, action) : action()
    } finally {
      active = parent
    }
  }
}
