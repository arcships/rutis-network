package arcships.rcp

import kotlinx.coroutines.*
import kotlin.test.*

class LifecycleTest {
    @Test fun peerClosingDuringRejectionPreservesVersionError() = runBlocking {
        val socket = object : BinarySocket {
            override fun setReadLimit(bytes: Int) {}
            override suspend fun read() = rejectVersion
            override suspend fun write(bytes: ByteArray) {
                assertContentEquals(rejectVersion, bytes)
                throw CancellationException("peer already closed its socket")
            }
            override fun abort() {}
        }
        val grant = Grant("", Role.DAEMON, ByteArray(32) { 1 }, byteArrayOf(9) + ByteArray(31),
            byteArrayOf(1), System.currentTimeMillis() + 60_000)
        val error = assertFailsWith<RcpException> {
            serveRcp(socket, grant) { error("version mismatch admitted a session") }
        }
        assertEquals(Reason.VERSION, error.reason)
    }

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
