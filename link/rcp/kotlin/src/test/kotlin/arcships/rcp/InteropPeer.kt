package arcships.rcp

import io.ktor.server.cio.CIO
import io.ktor.server.engine.*
import io.ktor.server.application.install
import io.ktor.server.routing.routing
import io.ktor.server.websocket.*
import kotlinx.coroutines.*
import kotlinx.serialization.json.*
import java.io.File
import java.util.Base64

/** Test process driven by the Go path matrix. Secrets are read from its private fixture file. */
fun main() = runBlocking {
    val config = Json.parseToJsonElement(File(System.getenv("RCP_FIXTURE")).readText()).jsonObject
    fun field(k: String) = config.getValue(k).jsonPrimitive.content
    val role = if (field("role") == "client") Role.CLIENT else Role.DAEMON
    val grant = Grant(field("address"), role, Base64.getDecoder().decode(field("privateKey")),
        Base64.getDecoder().decode(field("peerKey")), Base64.getDecoder().decode(field("binding")),
        System.currentTimeMillis() + 60_000)
    if (field("mode") == "fault") {
        val fault = field("fault")
        val reason = Reason.entries.single { it.code == field("reason") }
        val terminal = fault in setOf("version", "revoked", "invalid", "slow", "refused")
        var calls = 0
        var opens = 0
        var sequence = 0
        val endings = mutableListOf<Reason>()
        val retries = mutableListOf<Reason>()
        val result = try {
            runRcp(Limits(4096,150), {
                calls++
                check(calls <= 2)
                if (fault == "expired" && calls == 1) throw RcpException(Reason.EXPIRED)
                if (fault == "invalid") Grant(grant.address, role, grant.privateKey, grant.peerKey,
                    byteArrayOf(0), grant.expiresAtMillis) else grant
            }, { why, delay -> check(delay == 250L) { "$fault retry: $why after ${delay}ms, previous=$retries" }; retries.add(why) }) { sender ->
                opens++
                if (fault !in setOf("slow", "refused") && !(fault in setOf("silent", "oversize") && opens == 1)) sender.close()
                object : Receiver {
                    override suspend fun receive(message: ByteArray) {
                        if (fault == "refused") error("test consumer declined the message")
                        if (fault == "oversize") { sender.send(ByteArray(4097)); error("oversized send returned") }
                        check(fault == "slow")
                        delay(60)
                        sequence++
                        check(message.size == 4096 && message.all { it == sequence.toByte() })
                    }
                    override suspend fun ended(reason: Reason) { endings += reason }
                }
            }
            null
        } catch (e: RcpException) { e.reason }
        check(result == (if (terminal) reason else null)) { "terminal outcome: $result" }
        check(retries == (if (fault in setOf("version","slow","refused")) emptyList() else listOf(reason))) { "retry outcomes: $retries" }
        check(calls == if (fault in setOf("version","slow","refused")) 1 else 2)
        val expectedEnds = when (fault) {
            "oversize" -> listOf(Reason.SIZE, Reason.DONE)
            "refused" -> listOf(Reason.REFUSED)
            "silent" -> listOf(Reason.TRANSPORT, Reason.DONE)
            "slow" -> listOf(Reason.REPLACED)
            else -> if (terminal) emptyList() else listOf(Reason.DONE)
        }
        check(endings == expectedEnds && opens == expectedEnds.size)
        if (fault == "slow") check(sequence == 3)
        return@runBlocking
    }
    val mine = field("role")
    val other = if (role == Role.CLIENT) "daemon" else "client"
    val complete = CompletableDeferred<Unit>()
    val limits = Limits(4096, 150)
    val open: suspend (Sender) -> Receiver = { sender ->
        sender.send("visible-only-at-the-rutis-endpoints-$mine".toByteArray())
        object : Receiver {
            var greeted = false
            override suspend fun receive(message: ByteArray) {
                when (message.decodeToString()) {
                    "visible-only-at-the-rutis-endpoints-$other" -> { greeted = true; sender.send("ack-$other".toByteArray()) }
                    "ack-$mine" -> {
                        check(greeted)
                        if (role == Role.CLIENT) sender.close()
                    }
                    else -> error("unexpected message")
                }
                if (message.decodeToString() == "ack-$mine") acknowledged = true
            }
            var acknowledged = false
            override suspend fun ended(reason: Reason) {
                if (greeted && acknowledged && (reason == Reason.DONE || reason == Reason.PEER_CLOSED || reason == Reason.TRANSPORT))
                    complete.complete(Unit)
                else complete.completeExceptionally(IllegalStateException("incomplete exchange: ${reason.code}"))
            }
        }
    }
    withTimeout(35_000) {
        if (field("mode") == "listen") {
            val server = embeddedServer(CIO, host = "127.0.0.1", port = 0) {
                install(WebSockets) { maxFrameSize = 4129 }
                routing { webSocket("/pair") {
                    try { serveRcp(KtorSocket(this), grant, limits, open) }
                    catch (e: RcpException) { if (!complete.isCompleted) complete.completeExceptionally(e) }
                } }
            }.start(wait = false)
            try {
                val port = server.engine.resolvedConnectors().single().port
                File(field("readyFile")).writeText("ws://127.0.0.1:$port/pair")
                complete.await()
            } finally { server.stop(0, 1000) }
        } else {
            val task = launch { runRcp(limits, { grant }, open = open) }
            try { complete.await() } finally { task.cancelAndJoin() }
        }
    }
}
