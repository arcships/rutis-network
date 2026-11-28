package arcships.rcp

import com.southernstorm.noise.protocol.Noise
import kotlinx.serialization.json.*
import kotlin.test.*

internal fun checkGoVector(text: String) {
    val json = Json.parseToJsonElement(text).jsonObject
    fun v(key: String) = json.getValue(key).jsonPrimitive.content.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
    fun pub(key: ByteArray): ByteArray {
        val dh = Noise.createDH("25519")
        return try {
            dh.setPrivateKey(key, 0)
            ByteArray(32).also { dh.getPublicKey(it, 0) }
        } finally { dh.destroy() }
    }
    fun grant(role: Role, mine: String, peer: String) = Grant("ws://127.0.0.1",
        role, v(mine + "StaticPrivate"), pub(v(peer + "StaticPrivate")), v("binding"), Long.MAX_VALUE)
    val client = Pairing(grant(Role.CLIENT, "client", "daemon"), v("clientEphemeralPrivate"))
    val daemon = Pairing(grant(Role.DAEMON, "daemon", "client"), v("daemonEphemeralPrivate"))
    assertContentEquals(v("clientPair"), client.write(v("clientOffer")))
    assertContentEquals(v("clientOffer"), daemon.read(v("clientPair")))
    assertContentEquals(v("daemonPair"), daemon.write(v("daemonOffer")))
    assertContentEquals(v("daemonOffer"), client.read(v("daemonPair")))
    val c = client.split(1024)
    val d = daemon.split(1024)
    fun record(key: String, tx: Direction, rx: Direction, kind: Int, data: ByteArray = byteArrayOf()) {
        val frame = tx.seal(kind, data)
        assertContentEquals(v(key), frame)
        val decoded = rx.open(frame)
        assertEquals(kind, decoded.kind)
        assertContentEquals(data, decoded.bytes)
    }
    record("clientReady", c.tx, d.rx, 0)
    record("daemonReady", d.tx, c.rx, 0)
    assertEquals(Reason.FRAME, assertFailsWith<RcpException> { d.rx.open(v("clientDataInvalidTag")) }.reason)
    record("clientData", c.tx, d.rx, 1, v("plain"))
    assertEquals(Reason.FRAME, assertFailsWith<RcpException> { d.rx.open(v("clientData")) }.reason)
    record("daemonEmptyData", d.tx, c.rx, 1)
    record("clientClose", c.tx, d.rx, 4)
    assertContentEquals(ByteArray(1024), d.rx.open(c.tx.seal(1, ByteArray(1024))).bytes)
    assertEquals(Reason.SIZE, assertFailsWith<RcpException> { c.tx.seal(1, ByteArray(1025)) }.reason)
    assertEquals(Reason.VERSION, assertFailsWith<RcpException> { checkHandshake(v("rejectVersion")) }.reason)
    assertEquals(Reason.CREDENTIAL, assertFailsWith<RcpException> { checkHandshake(v("rejectCredential")) }.reason)
    c.close(); d.close(); client.close(); daemon.close()
}
