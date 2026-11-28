package arcships.rcp

import io.ktor.http.cio.parseResponse
import io.ktor.utils.io.ByteReadChannel
import java.io.BufferedInputStream
import java.io.DataInputStream
import java.io.IOException
import java.io.ByteArrayOutputStream
import java.net.InetSocketAddress
import java.net.Socket
import java.net.URI
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.net.ssl.SSLSocket
import javax.net.ssl.SSLSocketFactory
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

// A session that reads ahead can parse the first application frame with the
// previous (handshake) limit. Pull frames only when the protocol asks for one.
private class ClientSocket(stream: Socket, private val tcp: Socket) : BinarySocket {
    private val input = DataInputStream(BufferedInputStream(stream.getInputStream(), 128))
    private val output = stream.getOutputStream().buffered()
    private val writer = Mutex()
    private val random = SecureRandom()
    private var limit = 128

    suspend fun upgrade(uri: URI) {
        val nonce = Base64.getEncoder().encodeToString(ByteArray(16).also(random::nextBytes))
        val target = uri.rawPath.ifEmpty { "/" } + (uri.rawQuery?.let { "?$it" } ?: "")
        val headers = listOf("GET $target HTTP/1.1", "Host: ${uri.rawAuthority}", "Upgrade: websocket",
            "Connection: Upgrade", "Sec-WebSocket-Version: 13", "Sec-WebSocket-Key: $nonce", "", "")
        output.write(headers.joinToString("\r\n").toByteArray(Charsets.US_ASCII))
        output.flush()
        val header = ByteArrayOutputStream()
        var lastFour = 0
        while (lastFour != 0x0d0a0d0a) {
            if (header.size() >= 16_384) fail(Reason.FRAME)
            val byte = input.readUnsignedByte()
            header.write(byte)
            lastFour = (lastFour shl 8) or byte
        }
        val response = parseResponse(ByteReadChannel(header.toByteArray())) ?: fail(Reason.FRAME)
        try {
            when (response.status) {
                101 -> Unit
                401, 403 -> fail(Reason.CREDENTIAL)
                410 -> fail(Reason.EXPIRED)
                404 -> fail(Reason.OFFLINE)
                429, 502, 503, 504 -> fail(Reason.UNAVAILABLE)
                else -> fail(Reason.REFUSED)
            }
            fun field(name: String) = response.headers[name]?.toString()
            val expected = MessageDigest.getInstance("SHA-1").digest(
                (nonce + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").toByteArray(Charsets.US_ASCII))
            if (!field("Upgrade").equals("websocket", ignoreCase = true) ||
                field("Connection").orEmpty().split(',').none { it.trim().equals("upgrade", ignoreCase = true) } ||
                field("Sec-WebSocket-Accept") != Base64.getEncoder().encodeToString(expected) ||
                field("Sec-WebSocket-Extensions") != null || field("Sec-WebSocket-Protocol") != null) fail(Reason.FRAME)
        } finally { response.release() }
    }

    override fun setReadLimit(bytes: Int) { limit = bytes }

    override suspend fun read(): ByteArray = withContext(Dispatchers.IO) { readMessage() }

    private suspend fun readMessage(): ByteArray {
        var fragments: ByteArrayOutputStream? = null
        while (true) {
            val flags = input.readUnsignedByte()
            val lengthTag = input.readUnsignedByte()
            if (flags and 0x70 != 0 || lengthTag >= 128) fail(Reason.FRAME)
            val opcode = flags and 15
            val fin = flags and 0x80 != 0
            if (opcode >= 8) {
                if (!fin || lengthTag > 125) fail(Reason.FRAME)
                val data = ByteArray(lengthTag).also(input::readFully)
                when (opcode) {
                    8 -> {
                        if (data.size == 1) fail(Reason.FRAME)
                        val tooLarge = data.size >= 2 && data[0] == 3.toByte() && data[1] == 0xf1.toByte()
                        try { sendFrame(8, data) } catch (_: IOException) { /* Peer may already be closed. */ }
                        fail(if (tooLarge) Reason.SIZE else Reason.TRANSPORT)
                    }
                    9 -> sendFrame(10, data)
                    10 -> Unit
                    else -> fail(Reason.FRAME)
                }
                continue
            }
            if (opcode != (if (fragments == null) 2 else 0)) fail(Reason.FRAME)
            val length = when (lengthTag) {
                126 -> input.readUnsignedShort().toLong().also { if (it < 126) fail(Reason.FRAME) }
                127 -> input.readLong().also { if (it < 65_536) fail(Reason.FRAME) }
                else -> lengthTag.toLong()
            }
            if (length > limit - (fragments?.size() ?: 0)) fail(Reason.SIZE)
            val data = ByteArray(length.toInt()).also(input::readFully)
            if (fragments == null && fin) return data
            val parts = fragments ?: ByteArrayOutputStream().also { fragments = it }
            parts.write(data)
            if (fin) return parts.toByteArray()
        }
    }

    private suspend fun sendFrame(opcode: Int, bytes: ByteArray) = writer.withLock {
        val mask = ByteArray(4).also(random::nextBytes)
        val header = ByteBuffer.allocate(14).put((0x80 or opcode).toByte())
        when (bytes.size) {
            in 0..125 -> header.put((0x80 or bytes.size).toByte())
            in 126..65535 -> header.put(0xfe.toByte()).putShort(bytes.size.toShort())
            else -> header.put(0xff.toByte()).putLong(bytes.size.toLong())
        }
        header.put(mask)
        output.write(header.array(), 0, header.position())
        val masked = ByteArray(bytes.size) { (bytes[it].toInt() xor mask[it and 3].toInt()).toByte() }
        output.write(masked)
        output.flush()
    }

    override suspend fun write(bytes: ByteArray) = withContext(Dispatchers.IO) { sendFrame(2, bytes) }
    override fun abort() = tcp.close()
}

internal suspend fun dial(grant: Grant, limits: Limits, open: suspend (Sender) -> Receiver) {
    val uri = URI(endpoint(grant.address).toASCIIString())
    val host = uri.host.removeSurrounding("[", "]")
    val secure = uri.scheme == "wss"
    val port = if (uri.port >= 0) uri.port else if (secure) 443 else 80
    if (port !in 1..65535) fail(Reason.CREDENTIAL)
    Socket().use { tcp ->
        var tls: SSLSocket? = null
        try {
            val client = withTimeoutOrNull(10_000) {
                abortOnCancellation(tcp::close) {
                    withContext(Dispatchers.IO) {
                        tcp.connect(InetSocketAddress(host, port), 10_000)
                        val stream = if (secure) {
                            val factory = SSLSocketFactory.getDefault() as SSLSocketFactory
                            (factory.createSocket(tcp, host, port, true) as SSLSocket).apply {
                                tls = this
                                sslParameters = sslParameters.apply { endpointIdentificationAlgorithm = "HTTPS" }
                                startHandshake()
                            }
                        } else tcp
                        ClientSocket(stream, tcp).also { it.upgrade(uri) }
                    }
                }
            } ?: fail(Reason.TRANSPORT)
            serveRcp(client, grant, limits, open)
        } finally {
            tcp.close()
            try { tls?.close() } catch (_: IOException) { /* TCP has already been released. */ }
        }
    }
}
