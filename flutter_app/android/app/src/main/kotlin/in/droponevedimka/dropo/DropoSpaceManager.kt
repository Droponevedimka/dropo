package `in`.droponevedimka.dropo

import android.app.admin.DevicePolicyManager
import android.content.ComponentName
import android.content.Context
import android.util.Log

internal data class DropoSpaceApp(val packageName: String, val name: String)

internal data class DropoSpaceInstallReport(
    val installed: List<String>,
    val alreadyInstalled: List<String>,
    val unavailable: List<String>,
    val errors: List<String>,
) {
    val readyCount: Int get() = installed.size + alreadyInstalled.size

    fun userMessage(): String = when {
        installed.isNotEmpty() -> "Добавлено приложений в Dropo Space: ${installed.size}"
        alreadyInstalled.isNotEmpty() -> "Приложения Dropo Space уже настроены"
        else -> "Подходящие приложения не найдены; используйте установку из магазина"
    }
}

/** Operations that must run inside the managed profile owned by dropo. */
internal object DropoSpaceManager {
    private const val TAG = "DropoSpaceManager"

    val apps = listOf(
        DropoSpaceApp("ru.oneme.app", "MAX"),
        DropoSpaceApp("ru.rostel", "Госуслуги"),
        DropoSpaceApp("ru.sberbankmobile", "СберБанк"),
        DropoSpaceApp("ru.sberbankmobile_new", "СберБанк"),
        DropoSpaceApp("com.idamob.tinkoff.android", "Т-Банк"),
        DropoSpaceApp("ru.vtb24.mobilebanking.android", "ВТБ Онлайн"),
        DropoSpaceApp("ru.alfabank.mobile.android", "Альфа-Банк"),
        DropoSpaceApp("ru.ozon.app.android", "Ozon"),
        DropoSpaceApp("com.wildberries.ru", "Wildberries"),
        DropoSpaceApp("com.avito.android", "Avito"),
        DropoSpaceApp("com.vkontakte.android", "ВКонтакте"),
        DropoSpaceApp("ru.ok.android", "Одноклассники"),
        DropoSpaceApp("ru.vk.store", "RuStore"),
        DropoSpaceApp("ru.rutube.app", "Rutube"),
        DropoSpaceApp("ru.yandex.yandexmaps", "Яндекс Карты"),
        DropoSpaceApp("com.yandex.browser", "Яндекс Браузер"),
        DropoSpaceApp("ru.yandex.music", "Яндекс Музыка"),
        DropoSpaceApp("ru.kinopoisk", "Кинопоиск"),
        DropoSpaceApp("ru.dublgis.dgismobile", "2ГИС"),
        DropoSpaceApp("ru.mts.mymts", "Мой МТС"),
        DropoSpaceApp("ru.sbermegamarket", "МегаМаркет"),
        DropoSpaceApp("ru.samokat.android", "Самокат"),
    )

    fun isProfileOwner(context: Context): Boolean {
        val policyManager = context.getSystemService(DevicePolicyManager::class.java)
            ?: return false
        return policyManager.isProfileOwnerApp(context.packageName)
    }

    fun finishProvisioning(context: Context): DropoSpaceInstallReport {
        val policyManager = context.getSystemService(DevicePolicyManager::class.java)
            ?: return emptyReport("DevicePolicyManager unavailable")
        val admin = ComponentName(context, DropoDeviceAdminReceiver::class.java)
        if (!policyManager.isProfileOwnerApp(context.packageName)) {
            return emptyReport("dropo is not the profile owner")
        }
        runCatching {
            policyManager.setProfileName(admin, "Dropo Space")
            policyManager.setProfileEnabled(admin)
        }.onFailure { error ->
            Log.e(TAG, "Failed to enable managed profile", error)
        }
        return installExistingApps(context)
    }

    fun installExistingApps(context: Context): DropoSpaceInstallReport {
        val policyManager = context.getSystemService(DevicePolicyManager::class.java)
            ?: return emptyReport("DevicePolicyManager unavailable")
        val admin = ComponentName(context, DropoDeviceAdminReceiver::class.java)
        if (!policyManager.isProfileOwnerApp(context.packageName)) {
            return emptyReport("dropo is not the profile owner")
        }

        val installed = mutableListOf<String>()
        val alreadyInstalled = mutableListOf<String>()
        val unavailable = mutableListOf<String>()
        val errors = mutableListOf<String>()
        for (app in apps) {
            val wasInstalled = isInstalledInThisProfile(context, app.packageName)
            try {
                if (policyManager.installExistingPackage(admin, app.packageName)) {
                    runCatching {
                        policyManager.setApplicationHidden(admin, app.packageName, false)
                    }
                    if (wasInstalled) {
                        alreadyInstalled += app.packageName
                    } else {
                        installed += app.packageName
                    }
                    Log.i(TAG, "Existing package ready ${app.packageName}")
                } else {
                    unavailable += app.packageName
                }
            } catch (error: SecurityException) {
                errors += "${app.packageName}: policy denied"
                Log.w(TAG, "Policy denied existing package ${app.packageName}", error)
            } catch (error: IllegalArgumentException) {
                unavailable += app.packageName
                Log.i(TAG, "Package is not available on device: ${app.packageName}")
            } catch (error: RuntimeException) {
                errors += "${app.packageName}: ${error.javaClass.simpleName}"
                Log.w(TAG, "Failed to install existing package ${app.packageName}", error)
            }
        }
        return DropoSpaceInstallReport(installed, alreadyInstalled, unavailable, errors)
    }

    private fun isInstalledInThisProfile(context: Context, packageName: String): Boolean {
        return runCatching {
            context.packageManager.getPackageInfo(packageName, 0)
            true
        }.getOrDefault(false)
    }

    private fun emptyReport(error: String) = DropoSpaceInstallReport(
        installed = emptyList(),
        alreadyInstalled = emptyList(),
        unavailable = emptyList(),
        errors = listOf(error),
    )
}
