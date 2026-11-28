package arcships.rcp

import kotlinx.coroutines.*
import kotlin.test.*

class LifecycleTest {
    @Test fun cancelPendingRetry() = runBlocking {
        var attempts = 0
        val waiting = CompletableDeferred<Unit>()
        val row = launch {
            runRcp(provider = {
                attempts++
                throw RcpException(Reason.PROVIDER)
            }, onRetry = { reason, delay ->
                assertEquals(Reason.PROVIDER, reason)
                assertEquals(250L, delay)
                waiting.complete(Unit)
            }) { error("admitted without a grant") }
        }
        withTimeout(2000) { waiting.await() }
        row.cancelAndJoin()
        delay(300)
        assertEquals(1, attempts)
    }
}
