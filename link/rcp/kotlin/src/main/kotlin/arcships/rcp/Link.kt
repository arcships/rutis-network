package arcships.rcp

import io.ktor.client.HttpClient
import io.ktor.client.engine.cio.CIO
import io.ktor.client.plugins.HttpResponseValidator
import io.ktor.client.plugins.websocket.WebSockets
import io.ktor.client.plugins.websocket.webSocketSession
import io.ktor.websocket.Frame
import io.ktor.websocket.FrameTooBigException
import io.ktor.websocket.WebSocketSession
import java.net.URI
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** The owner supplies one ordered binary WebSocket, with a bounded read and write path. */
interface BinarySocket {
    fun setReadLimit(bytes: Int)
    suspend fun read(): ByteArray
    suspend fun write(bytes: ByteArray)
    fun abort()
}

interface Sender {
    val limits: Limits
    suspend fun send(message: ByteArray)
    suspend fun close()
}

interface Receiver {
    suspend fun receive(message: ByteArray)
    suspend fun ended(reason: Reason) {}
}

private suspend fun <T : Any> bounded(ms: Long, reason: Reason, block: suspend () -> T): T =
    withTimeoutOrNull(ms) { block() } ?: fail(reason)

private suspend fun pair(socket: BinarySocket, grant: Grant, limits: Limits): Pair<Limits, Records> = coroutineScope {
    Pairing(grant).use { state ->
        val first = if (grant.role == Role.CLIENT) state.write(limits.offer()) else null
        val repeat = first?.let {
            socket.write(it)
            launch { while (isActive) { delay(1000); socket.write(it) } }
        }
        suspend fun nextOffer(): ByteArray {
            while (true) {
                val bytes = socket.read()
                checkHandshake(bytes)
                if (isPair(bytes)) return bytes
                if (bytes.size > 128) fail(Reason.SIZE)
                if (!envelope(bytes)) fail(Reason.FRAME)
            }
        }
        val offer = try { nextOffer() } finally { repeat?.cancelAndJoin() }
        val agreed = limits.negotiate(state.read(offer))
        socket.setReadLimit(maxOf(60, agreed.maxMessage + 33))
        if (first == null) socket.write(state.write(limits.offer()))
        val keys = state.split(agreed.maxMessage)
        try {
            if (first != null) socket.write(keys.tx.seal(0))
            while (true) {
                val b = socket.read()
                if (b.size > 128) fail(Reason.SIZE)
                checkHandshake(b)
                if (first == null && b.contentEquals(offer)) continue
                if (keys.rx.open(b).kind != 0) fail(Reason.FRAME)
                break
            }
            if (first == null) socket.write(keys.tx.seal(0))
            grant.validate()
            agreed to keys
        } catch (e: Throwable) { keys.close(); throw e }
    }
}

/** Owns this socket through admission and teardown. Only an admitted peer reaches open. */
suspend fun serveRcp(socket: BinarySocket, grant: Grant, limits: Limits = Limits(),
                     open: suspend (Sender) -> Receiver) {
    try {
        grant.validate()
        socket.setReadLimit(128)
        val until = minOf(10_000L, grant.expiresAtMillis - System.currentTimeMillis())
        val (agreed, keys) = try { bounded(until, Reason.OFFLINE) { pair(socket, grant, limits) } }
        catch (e: RcpException) {
            val reason = if (grant.expiresAtMillis <= System.currentTimeMillis()) Reason.EXPIRED else e.reason
            val rejection = when (reason) {
                Reason.CREDENTIAL, Reason.EXPIRED -> rejectCredential
                Reason.VERSION -> rejectVersion
                else -> null
            }
            if (rejection != null) {
                try { bounded(1000, Reason.TRANSPORT) { socket.write(rejection) } }
                catch (_: CancellationException) { currentCoroutineContext().ensureActive() }
                catch (_: Exception) { /* the peer may already have closed */ }
            }
            fail(reason)
        }
        val writeLock = Mutex()
        val stopped = AtomicBoolean()
        val writeFailure = AtomicReference<Reason?>()
        val localClose = AtomicBoolean()
        suspend fun send(kind: Int, body: ByteArray = byteArrayOf()) = writeLock.withLock {
            if (stopped.get() || (localClose.get() && kind != 4)) fail(Reason.DONE)
            writeFailure.get()?.let { fail(it) }
            try {
                bounded(3L * agreed.heartbeatMillis, Reason.TRANSPORT) { socket.write(keys.tx.seal(kind, body)) }
            } catch (e: Exception) {
                writeFailure.compareAndSet(null, (e as? RcpException)?.reason ?: Reason.TRANSPORT)
                socket.abort()
                throw e
            }
        }
        val sender = object : Sender {
            override val limits = agreed
            override suspend fun send(message: ByteArray) { send(1, message) }
            override suspend fun close() {
                if (!localClose.compareAndSet(false, true)) return
                try { bounded(1000, Reason.TRANSPORT) { send(4) } } finally { socket.abort() }
            }
        }
        var receiver: Receiver? = null
        var reason = Reason.DONE
        try {
            receiver = try { open(sender) }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { fail(writeFailure.get() ?: Reason.REFUSED) }
            coroutineScope {
                val heartbeat = launch {
                    while (isActive) { delay(agreed.heartbeatMillis.toLong()); send(2) }
                }
                try {
                    while (!localClose.get()) {
                        val data = bounded(3L * agreed.heartbeatMillis, Reason.TRANSPORT) { socket.read() }
                        val record = keys.rx.open(data)
                        when (record.kind) {
                            1 -> try {
                                receiver.receive(record.bytes)
                            } catch (e: CancellationException) {
                                throw e
                            } catch (_: Exception) {
                                fail(writeFailure.get() ?: Reason.REFUSED)
                            }
                            2 -> send(3)
                            3 -> Unit
                            4 -> fail(Reason.PEER_CLOSED)
                            5 -> fail(Reason.REPLACED)
                            6 -> fail(Reason.REFUSED)
                            else -> fail(Reason.FRAME)
                        }
                    }
                } finally { heartbeat.cancel() }
            }
        } catch (e: CancellationException) {
            currentCoroutineContext().ensureActive()
            if (!localClose.get()) { reason = writeFailure.get() ?: Reason.TRANSPORT; fail(reason) }
        } catch (e: Exception) {
            if (!localClose.get()) {
                val received = (e as? RcpException)?.reason ?: Reason.TRANSPORT
                // A heartbeat interrupted by teardown must not mask a received close record.
                reason = if (received == Reason.TRANSPORT) writeFailure.get() ?: received else received
                fail(reason)
            }
        } finally {
            stopped.set(true)
            socket.abort()
            withContext(NonCancellable) {
                writeLock.withLock { keys.close() }
                receiver?.ended(reason)
            }
        }
    } finally { socket.abort() }
}

internal class KtorSocket(private val session: WebSocketSession) : BinarySocket {
    override fun setReadLimit(bytes: Int) { session.maxFrameSize = bytes.toLong() }
    override suspend fun read(): ByteArray {
        val frame = try { session.incoming.receive() } catch (_: FrameTooBigException) { fail(Reason.SIZE) }
        return (frame as? Frame.Binary)?.data ?: fail(Reason.FRAME)
    }
    override suspend fun write(bytes: ByteArray) {
        session.outgoing.send(Frame.Binary(true, bytes))
        session.flush()
    }
    override fun abort() { session.cancel() }
}

internal fun endpoint(address: String): URI {
    val uri = try { URI(address) } catch (_: Exception) { fail(Reason.CREDENTIAL) }
    val host = uri.host ?: fail(Reason.CREDENTIAL)
    val loopback = host == "[::1]" || (host.matches(Regex("127(\\.[0-9]{1,3}){3}")) &&
        host.split('.').all { it.toInt() <= 255 })
    if (uri.userInfo != null || uri.fragment != null || (uri.scheme != "wss" && !(uri.scheme == "ws" && loopback)))
        fail(Reason.CREDENTIAL)
    return uri
}

private suspend fun dial(grant: Grant, limits: Limits, open: suspend (Sender) -> Receiver) {
    endpoint(grant.address)
    val client = HttpClient(CIO) {
        followRedirects = false
        install(WebSockets) { maxFrameSize = 128 }
        HttpResponseValidator {
            validateResponse { response ->
                when (response.status.value) {
                    101 -> Unit
                    401, 403 -> fail(Reason.CREDENTIAL)
                    410 -> fail(Reason.EXPIRED)
                    404 -> fail(Reason.OFFLINE)
                    429, 502, 503, 504 -> fail(Reason.UNAVAILABLE)
                    else -> fail(Reason.REFUSED)
                }
            }
        }
    }
    try {
        val session = bounded(10_000, Reason.TRANSPORT) { client.webSocketSession(grant.address) }
        serveRcp(KtorSocket(session), grant, limits, open)
    } finally { client.coroutineContext.cancel(); client.close() }
}

/** Run one device until local close, terminal failure or caller cancellation. */
suspend fun runRcp(limits: Limits = Limits(), provider: suspend () -> Grant,
                   onRetry: (Reason, Long) -> Unit = { _, _ -> }, open: suspend (Sender) -> Receiver) {
    var backoff = 250L
    var refreshed = false
    while (true) {
        currentCoroutineContext().ensureActive()
        val reason = try {
            val grant = try { provider() }
            catch (e: CancellationException) { throw e }
            catch (e: RcpException) { throw e }
            catch (_: java.net.SocketTimeoutException) { fail(Reason.PROVIDER) }
            catch (_: Exception) { fail(Reason.CREDENTIAL) }
            grant.validate()
            dial(grant, limits) { sender ->
                backoff = 250L
                refreshed = false
                open(sender)
            }
            return
        } catch (e: CancellationException) { currentCoroutineContext().ensureActive(); Reason.TRANSPORT }
          catch (e: RcpException) { e.reason }
          catch (_: Exception) { Reason.TRANSPORT }
        when (reason) {
            Reason.CREDENTIAL, Reason.EXPIRED -> { if (refreshed) fail(reason); refreshed = true }
            Reason.TRANSPORT, Reason.OFFLINE, Reason.UNAVAILABLE, Reason.PROVIDER, Reason.PEER_CLOSED,
            Reason.RESTARTED, Reason.SIZE -> Unit
            else -> fail(reason)
        }
        onRetry(reason, backoff)
        delay(backoff)
        backoff = minOf(2 * backoff, 30_000)
    }
}
