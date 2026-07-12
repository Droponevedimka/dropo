package `in`.droponevedimka.dropo

import android.os.Process
import android.os.SystemClock
import android.text.format.DateFormat
import org.json.JSONObject
import java.util.concurrent.CopyOnWriteArraySet
import java.util.concurrent.atomic.AtomicLong

object DropoVpnRuntime {
    interface Listener {
        fun onEvent(event: Map<String, Any?>)
    }

    private val listeners = CopyOnWriteArraySet<Listener>()
    private val nextEventId = AtomicLong(1)
    private val lock = Any()

    private var state = STATE_STOPPED
    private var message = ""
    private var error = ""
    private var updatedAt = System.currentTimeMillis()
    private var connectedAt = 0L
    private val recentLogs = ArrayDeque<String>()

    fun addListener(listener: Listener) {
        listeners.add(listener)
        listener.onEvent(event("android-service-status", snapshot()))
    }

    fun removeListener(listener: Listener) {
        listeners.remove(listener)
    }

    fun setStarting(text: String = "VPN запускается") {
        setState(STATE_STARTING, text, "")
    }

    fun setConnected(text: String = "VPN работает") {
        setState(STATE_CONNECTED, text, "")
    }

    fun setDisconnecting(text: String = "VPN останавливается") {
        setState(STATE_DISCONNECTING, text, "")
    }

    fun setStopped(text: String = "VPN остановлен") {
        setState(STATE_STOPPED, text, "")
    }

    fun setFailed(text: String) {
        setState(STATE_FAILED, text, text)
    }

    fun appendLog(line: String) {
        val entry = "${DateFormat.format("HH:mm:ss", System.currentTimeMillis())} $line"
        synchronized(lock) {
            recentLogs.addLast(entry)
            while (recentLogs.size > MAX_RECENT_LOGS) {
                recentLogs.removeFirst()
            }
        }
        emit("android-log", mapOf("line" to entry))
    }

    fun snapshot(): Map<String, Any?> = synchronized(lock) {
        val connected = state == STATE_CONNECTED
        val starting = state == STATE_STARTING
        val disconnecting = state == STATE_DISCONNECTING
        mapOf(
            "state" to state,
            "vpnState" to state,
            "connected" to connected,
            "running" to connected,
            "connecting" to starting,
            "disconnecting" to disconnecting,
            "hasError" to (state == STATE_FAILED),
            "error" to error,
            "message" to message,
            "updatedAt" to updatedAt,
            "connectedAt" to connectedAt,
            "uptimeMs" to if (connectedAt > 0) SystemClock.elapsedRealtime() - connectedAt else 0L,
            "pid" to Process.myPid(),
        )
    }

    fun recentLogs(): List<String> = synchronized(lock) { recentLogs.toList() }

    fun mergeCoreStatus(coreStatusJson: String): String {
        val status = JSONObject(coreStatusJson)
        val snapshot = snapshot()
        val stateValue = snapshot["state"]?.toString().orEmpty()
        val nativeHasError = snapshot["hasError"] == true
        val coreHasError = status.optBoolean("hasError")

        status.put("vpnState", stateValue)
        status.put("connected", snapshot["connected"] == true)
        status.put("running", snapshot["running"] == true)
        status.put("connecting", snapshot["connecting"] == true)
        status.put("disconnecting", snapshot["disconnecting"] == true)
        status.put("serviceMessage", snapshot["message"]?.toString().orEmpty())
        status.put("serviceUpdatedAt", snapshot["updatedAt"])
        status.put("servicePid", snapshot["pid"])
        status.put("hasError", nativeHasError || coreHasError)
        if (nativeHasError) {
            status.put("error", snapshot["error"]?.toString().orEmpty())
        }
        return status.toString()
    }

    private fun setState(nextState: String, nextMessage: String, nextError: String) {
        val shouldEmit: Boolean
        synchronized(lock) {
            shouldEmit = state != nextState || message != nextMessage || error != nextError
            state = nextState
            message = nextMessage
            error = nextError
            updatedAt = System.currentTimeMillis()
            if (nextState == STATE_CONNECTED && connectedAt == 0L) {
                connectedAt = SystemClock.elapsedRealtime()
            }
            if (nextState != STATE_CONNECTED) {
                connectedAt = 0L
            }
        }
        if (shouldEmit) {
            emit("android-service-status", snapshot())
        }
    }

    private fun emit(name: String, payload: Map<String, Any?>) {
        val event = event(name, payload)
        listeners.forEach { listener ->
            runCatching { listener.onEvent(event) }
        }
    }

    private fun event(name: String, payload: Map<String, Any?>): Map<String, Any?> {
        return mapOf(
            "id" to nextEventId.getAndIncrement(),
            "name" to name,
            "payload" to payload,
        )
    }

    private const val MAX_RECENT_LOGS = 240
    private const val STATE_STOPPED = "stopped"
    private const val STATE_STARTING = "starting"
    private const val STATE_CONNECTED = "connected"
    private const val STATE_DISCONNECTING = "disconnecting"
    private const val STATE_FAILED = "failed"
}
