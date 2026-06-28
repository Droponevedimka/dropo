package `in`.droponevedimka.dropo

import android.net.VpnService

class DropoVpnService : VpnService() {
    fun protectFileDescriptor(socket: Int): Boolean {
        return protect(socket)
    }
}
