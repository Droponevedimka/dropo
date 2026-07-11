package `in`.droponevedimka.dropo

import android.app.admin.DeviceAdminReceiver
import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.util.Log

class DropoDeviceAdminReceiver : DeviceAdminReceiver() {
    override fun onProfileProvisioningComplete(context: Context, intent: Intent) {
        val policyManager = context.getSystemService(DevicePolicyManager::class.java) ?: return
        if (!policyManager.isProfileOwnerApp(context.packageName)) {
            Log.e(TAG, "Provisioned profile is not owned by dropo; refusing to configure it")
            return
        }

        val admin = ComponentName(context, DropoDeviceAdminReceiver::class.java)
        runCatching {
            policyManager.setProfileName(admin, PROFILE_NAME)
            policyManager.setProfileEnabled(admin)
        }.onFailure { error ->
            Log.e(TAG, "Failed to finish managed-profile provisioning", error)
        }
    }

    private companion object {
        const val TAG = "DropoDeviceAdmin"
        const val PROFILE_NAME = "Dropo Space"
    }
}
