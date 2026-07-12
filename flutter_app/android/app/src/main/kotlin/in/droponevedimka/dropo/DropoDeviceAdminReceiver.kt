package `in`.droponevedimka.dropo

import android.app.admin.DeviceAdminReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

class DropoDeviceAdminReceiver : DeviceAdminReceiver() {
    override fun onProfileProvisioningComplete(context: Context, intent: Intent) {
        if (!DropoSpaceManager.isProfileOwner(context)) {
            Log.e(TAG, "Provisioned profile is not owned by dropo; refusing to configure it")
            return
        }
        val report = DropoSpaceManager.finishProvisioning(context)
        Log.i(
            TAG,
            "Managed profile ready: installed=${report.installed.size} " +
                "existing=${report.alreadyInstalled.size} unavailable=${report.unavailable.size} " +
                "errors=${report.errors.size}",
        )
    }

    private companion object {
        const val TAG = "DropoDeviceAdmin"
    }
}
