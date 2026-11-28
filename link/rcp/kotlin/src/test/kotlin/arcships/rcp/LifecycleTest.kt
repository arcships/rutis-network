package arcships.rcp

import java.io.IOException
import java.util.concurrent.atomic.AtomicReference
import kotlin.coroutines.Continuation
import kotlin.coroutines.resumeWithException
import kotlin.coroutines.suspendCoroutine
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

    // Models a native callback that only completes its read when the owner closes the socket.
    private class CallbackSocket : BinarySocket {
        val reading = CompletableDeferred<Unit>()
        private val pending = AtomicReference<Continuation<ByteArray>?>()
        override fun setReadLimit(bytes: Int) {}
        override suspend fun write(bytes: ByteArray) {}
        override suspend fun read(): ByteArray = suspendCoroutine {
            pending.set(it)
            reading.complete(Unit)
        }
        override fun abort() { pending.getAndSet(null)?.resumeWithException(IOException("closed")) }
    }

    @Test fun cancelAndExpiryAbortPendingHandshakeIO() = runBlocking {
        for (cancelOwner in listOf(true, false)) {
            val socket = CallbackSocket()
            val duration = if (cancelOwner) 60_000L else 300L
            val grant = Grant("", Role.DAEMON, ByteArray(32) { 1 }, byteArrayOf(9) + ByteArray(31),
                byteArrayOf(1), System.currentTimeMillis() + duration)
            val row = async {
                try { serveRcp(socket, grant) { error("unexpected admission") }; null }
                catch (e: RcpException) { e.reason }
            }
            try {
                withTimeout(2000) { socket.reading.await() }
                if (cancelOwner) row.cancel()
                withTimeout(2000) { row.join() }
                if (cancelOwner) assertTrue(row.isCancelled) else assertEquals(Reason.EXPIRED, row.await())
            } finally { socket.abort() }
        }
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
