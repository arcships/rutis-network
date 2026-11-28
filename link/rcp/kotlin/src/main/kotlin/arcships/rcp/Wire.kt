package arcships.rcp

import com.southernstorm.noise.protocol.CipherState
import com.southernstorm.noise.protocol.HandshakeState
import com.southernstorm.noise.protocol.Noise
import java.nio.ByteBuffer
import java.security.GeneralSecurityException

enum class Role { CLIENT, DAEMON }
enum class Reason(val code: String) {
    CREDENTIAL("credential-rejected"), EXPIRED("credential-expired"), VERSION("version-mismatch"),
    FRAME("invalid-frame"), SIZE("frame-too-large"), OFFLINE("peer-offline"), UNAVAILABLE("relay-unavailable"),
    PROVIDER("credential-provider-unavailable"), TRANSPORT("transport-error"), RESTARTED("peer-restarted"),
    PEER_CLOSED("peer-closed"), DONE("done"), REPLACED("replaced"), REFUSED("refused")
}
class RcpException(val reason: Reason) : Exception(reason.code)
internal fun fail(reason: Reason): Nothing = throw RcpException(reason)

class Grant(
    val address: String, val role: Role, val privateKey: ByteArray, val peerKey: ByteArray,
    val binding: ByteArray, val expiresAtMillis: Long
) {
    internal fun validate() {
        if (expiresAtMillis <= System.currentTimeMillis()) fail(Reason.EXPIRED)
        if (privateKey.size != 32 || peerKey.size != 32 || binding.size !in 1..1024) fail(Reason.CREDENTIAL)
    }
}

data class Limits(val maxMessage: Int = 16 shl 20, val heartbeatMillis: Int = 10_000) {
    init {
        require(maxMessage in 1..(1 shl 30) && heartbeatMillis in 100..300_000)
    }
    internal fun offer(): ByteArray = ByteBuffer.allocate(8).putInt(maxMessage).putInt(heartbeatMillis).array()
    internal fun negotiate(payload: ByteArray): Limits {
        val buf = ByteBuffer.wrap(payload)
        val size = buf.int
        val beat = buf.int
        if (size !in 1..(1 shl 30) || beat !in 100..300_000) fail(Reason.FRAME)
        return Limits(minOf(maxMessage, size), maxOf(heartbeatMillis, beat))
    }
}

internal val version = byteArrayOf(82, 67, 80, 1)
internal val rejectVersion = byteArrayOf(82, 67, 80, 69, 1)
internal val rejectCredential = byteArrayOf(82, 67, 80, 69, 2)
internal fun isPair(b: ByteArray) = b.size == 60 && b.copyOfRange(0, 4).contentEquals(version)
internal fun envelope(b: ByteArray) = b.size >= 33 && ByteBuffer.wrap(b).long == 12L.shl(32)

internal fun checkHandshake(b: ByteArray) {
    if (b.contentEquals(rejectCredential)) fail(Reason.CREDENTIAL)
    if (b.contentEquals(rejectVersion) || (b.size >= 4 && b[0] == 82.toByte() &&
            b[1] == 67.toByte() && b[2] == 80.toByte() && b[3] != 1.toByte())) fail(Reason.VERSION)
}

internal class Pairing(grant: Grant, ephemeral: ByteArray? = null) : AutoCloseable {
    private val state = HandshakeState("Noise_KK_25519_AESGCM_SHA256",
        if (grant.role == Role.CLIENT) HandshakeState.INITIATOR else HandshakeState.RESPONDER)
    init {
        grant.validate()
        val prologue = version + grant.binding
        state.setPrologue(prologue, 0, prologue.size)
        state.localKeyPair.setPrivateKey(grant.privateKey, 0)
        validatePeer(grant.peerKey)
        state.remotePublicKey.setPublicKey(grant.peerKey, 0)
        if (ephemeral != null) state.fixedEphemeralKey.setPrivateKey(ephemeral, 0)
        state.start()
    }
    // noise-java exposes raw X25519. Reject a non-contributory peer key before mixing it.
    private fun validatePeer(key: ByteArray) {
        val remote = Noise.createDH("25519")
        val shared = ByteArray(32)
        try {
            remote.setPublicKey(key, 0)
            state.localKeyPair.calculate(shared, 0, remote)
            if (shared.all { it == 0.toByte() }) fail(Reason.CREDENTIAL)
        } finally {
            shared.fill(0)
            remote.destroy()
        }
    }
    fun write(payload: ByteArray): ByteArray {
        val out = version.copyOf(60)
        state.writeMessage(out, 4, payload, 0, payload.size)
        return out
    }
    fun read(frame: ByteArray): ByteArray {
        checkHandshake(frame)
        if (!isPair(frame)) fail(Reason.FRAME)
        validatePeer(frame.copyOfRange(4, 36))
        val out = ByteArray(8)
        try {
            state.readMessage(frame, 4, 56, out, 0)
        } catch (_: GeneralSecurityException) { fail(Reason.CREDENTIAL) }
        return out
    }
    fun split(limit: Int): Records {
        val pair = state.split()
        return Records(Direction(pair.sender, limit), Direction(pair.receiver, limit))
    }
    override fun close() = state.destroy()
}

internal class Records(val tx: Direction, val rx: Direction) : AutoCloseable {
    override fun close() { tx.destroy(); rx.destroy() }
}
internal data class Record(val kind: Int, val bytes: ByteArray)
internal class Direction(private val cipher: CipherState, private val limit: Int) {
    private var next = 0L
    fun seal(kind: Int, body: ByteArray = byteArrayOf()): ByteArray {
        if (body.size > limit) fail(Reason.SIZE)
        if (next == -1L) fail(Reason.FRAME)
        val header = ByteBuffer.allocate(16).putInt(12).putInt(0).putLong(next).array()
        val plain = byteArrayOf(kind.toByte()) + body
        val frame = header.copyOf(33 + body.size)
        cipher.encryptWithAd(header, plain, 0, frame, 16, plain.size)
        next++
        return frame
    }
    fun open(b: ByteArray): Record {
        if (isPair(b)) fail(Reason.RESTARTED)
        if (b.size > limit + 33) fail(Reason.SIZE)
        if (next == -1L || !envelope(b) || ByteBuffer.wrap(b, 8, 8).long != next) fail(Reason.FRAME)
        val plain = ByteArray(b.size - 32)
        try {
            cipher.decryptWithAd(b.copyOfRange(0, 16), b, 16, plain, 0, b.size - 16)
        } catch (_: GeneralSecurityException) { fail(Reason.FRAME) }
        next++
        val kind = plain[0].toInt()
        if (kind !in 0..6 || (kind != 1 && plain.size != 1)) fail(Reason.FRAME)
        return Record(kind, plain.copyOfRange(1, plain.size))
    }
    fun destroy() = cipher.destroy()
}
