export interface IceServer {
  urls: string | string[]
  username?: string
  credential?: string
}

export interface Grant {
  attemptId: string
  expiresAt: string
  role: 'offerer' | 'answerer'
  iceServers?: IceServer[]
  refreshAt?: string
  renewOf?: string
}

export interface Description {
  type: 'offer' | 'answer'
  sdp: string
}

export interface Acquire {
  pairId: string
  after: string | null
  waitMs: number
  renewOf?: string
}

export interface Attempt {
  pairId: string
  attemptId: string
}

export interface Signaling {
  acquire(input: Acquire, signal: AbortSignal): Promise<Grant | null | undefined>
  receive(
    input: Attempt & { type: Description['type']; waitMs: number },
    signal: AbortSignal,
  ): Promise<Description | null | undefined>
  publish(input: Attempt & Description, signal: AbortSignal): Promise<unknown>
}
