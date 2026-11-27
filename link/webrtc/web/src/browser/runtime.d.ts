// scripts/browser-runtime.mjs binds these modules to the pinned rutis runtime.
declare module 'rutis-codec-v5' {
  export function decode(text: string): unknown
}
declare module 'rutis-session-v5' {
  export const RECORD: unique symbol
  export const ENDPOINT_ID: RegExp
  export class Session {
    constructor(options: {
      endpoint: { local: string }
      maxTransfer: number
      dispatch(target: string, method: string, args: unknown[]): unknown
      pump(): never
      abort(error: unknown): void
      send(text: string): void
    })
    readonly ready: Promise<unknown>
    readonly fault?: Error
    start(): void
    receive(record: unknown): void
    close(reason: unknown): void
    invokeAsync(target: string, method: string, args: unknown[]): Promise<unknown>
    callFunctionAsync(fn: Function, args: unknown[]): Promise<unknown>
  }
}
declare module 'buffer/' {
  export { Buffer } from 'buffer'
}
