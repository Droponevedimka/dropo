package `in`.droponevedimka.dropo

import android.Manifest
import android.app.Activity
import android.app.PendingIntent
import android.app.admin.DevicePolicyManager
import android.content.ActivityNotFoundException
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentSender
import android.content.pm.LauncherApps
import android.content.pm.PackageManager
import android.content.pm.ShortcutInfo
import android.content.pm.ShortcutManager
import android.graphics.drawable.Icon
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import dropoandroid.Dropoandroid
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class MainActivity : FlutterActivity() {
    private val coreExecutor = Executors.newSingleThreadExecutor()
    private val mainHandler = Handler(Looper.getMainLooper())
    private var coreChannel: MethodChannel? = null
    private var eventChannel: EventChannel? = null
    private var eventListener: DropoVpnRuntime.Listener? = null
    private var pendingConnectResult: MethodChannel.Result? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        handleDropoSpaceShortcut(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        handleDropoSpaceShortcut(intent)
    }

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        coreChannel = MethodChannel(
            flutterEngine.dartExecutor.binaryMessenger,
            CHANNEL_CORE,
        ).also { channel ->
            channel.setMethodCallHandler { call, result -> handleCoreCall(call, result) }
        }
        eventChannel = EventChannel(
            flutterEngine.dartExecutor.binaryMessenger,
            CHANNEL_EVENTS,
        ).also { channel ->
            channel.setStreamHandler(
                object : EventChannel.StreamHandler {
                    override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
                        val listener = object : DropoVpnRuntime.Listener {
                            override fun onEvent(event: Map<String, Any?>) {
                                mainHandler.post { events?.success(event) }
                            }
                        }
                        eventListener = listener
                        DropoVpnRuntime.addListener(listener)
                    }

                    override fun onCancel(arguments: Any?) {
                        eventListener?.let { DropoVpnRuntime.removeListener(it) }
                        eventListener = null
                    }
                },
            )
        }
    }

    override fun onDestroy() {
        coreChannel?.setMethodCallHandler(null)
        coreChannel = null
        eventChannel?.setStreamHandler(null)
        eventChannel = null
        eventListener?.let { DropoVpnRuntime.removeListener(it) }
        eventListener = null
        coreExecutor.shutdownNow()
        super.onDestroy()
    }

    @Deprecated("Deprecated in Android framework API")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        when (requestCode) {
            REQUEST_VPN_PREPARE -> {
                if (resultCode == Activity.RESULT_OK) {
                    startVpnAndResolve()
                } else {
                    DropoVpnRuntime.setFailed("VPN permission was denied")
                    recordCoreCall("AndroidEngineError", "[\"VPN permission was denied\"]")
                    resolvePendingConnect(errorJson("VPN permission was denied"))
                }
            }

            REQUEST_MANAGED_PROFILE_PROVISIONING -> {
                val text = if (resultCode == Activity.RESULT_OK) {
                    "profile provisioning finished successfully"
                } else {
                    "profile provisioning closed without confirmation (result=$resultCode)"
                }
                logDropoSpace(text)
            }
        }
    }

    override fun onRequestPermissionsResult(
        requestCode: Int,
        permissions: Array<out String>,
        grantResults: IntArray,
    ) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQUEST_POST_NOTIFICATIONS) {
            continueVpnPrepare()
        }
    }

    private fun handleCoreCall(call: MethodCall, result: MethodChannel.Result) {
        if (call.method in ANDROID_NATIVE_METHODS) {
            handleAndroidNativeCall(call, result)
            return
        }
        if (call.method == "setConnected") {
            handleSetConnected(call, result)
            return
        }
        if (call.method !in BACKGROUND_CORE_METHODS) {
            result.notImplemented()
            return
        }
        coreExecutor.execute {
            val response = try {
                handleCoreCallInBackground(call)
            } catch (error: Throwable) {
                errorJson(error.message ?: error.javaClass.simpleName)
            }
            runOnUiThread { result.success(response) }
        }
    }

    private fun handleAndroidNativeCall(call: MethodCall, result: MethodChannel.Result) {
        try {
            val response = when (call.method) {
                "androidCompatibility" -> buildAndroidCompatibility()
                "androidSetCompatibilityPromptDismissed" -> {
                    val dismissed = boolArg(call, "dismissed")
                    prefs().edit().putBoolean(PREF_COMPAT_PROMPT_DISMISSED, dismissed).apply()
                    successJson("Compatibility prompt preference saved")
                }
                "androidCreateDropoSpace" -> startManagedProfileProvisioning()
                "androidMoveToDropoSpace" -> movePackageToDropoSpace(stringArg(call, "packageName"))
                "androidRequestDropoSpaceShortcut" -> requestDropoSpaceShortcut(stringArg(call, "packageName"))
                "androidOpenCloneHelpSearch" -> openCloneHelpSearch()
                "androidOpenExternal" -> openExternalUrl(stringArg(call, "url"))
                else -> errorJson("Unsupported Android method ${call.method}")
            }
            result.success(response)
        } catch (error: Throwable) {
            result.success(errorJson(error.message ?: error.javaClass.simpleName))
        }
    }

    private fun handleCoreCallInBackground(call: MethodCall): String {
        return when (call.method) {
            "ensureStarted" -> ensureCoreStarted()
            "status" -> DropoVpnRuntime.mergeCoreStatus(Dropoandroid.status())
            "logs" -> Dropoandroid.logs()
            "events" -> Dropoandroid.events(longArg(call, "since"))
            "serviceStatus" -> JSONObject(DropoVpnRuntime.snapshot()).toString()
            "diagnostics" -> buildDiagnostics()
            "call" -> Dropoandroid.call(
                stringArg(call, "method"),
                stringArg(call, "argsJson", "[]"),
            )
            "shutdown" -> Dropoandroid.shutdown()
            else -> error("Unsupported core method ${call.method}")
        }
    }

    private fun handleSetConnected(call: MethodCall, result: MethodChannel.Result) {
        try {
            handleSetConnectedUnsafe(call, result)
        } catch (error: Throwable) {
            result.success(errorJson(error.message ?: error.javaClass.simpleName))
        }
    }

    private fun handleSetConnectedUnsafe(call: MethodCall, result: MethodChannel.Result) {
        val connected = boolArg(call, "connected")
        if (!connected) {
            DropoVpnRuntime.setDisconnecting("VPN останавливается")
            recordCoreCall("AndroidServiceState", "[\"disconnecting\",\"VPN останавливается\",\"\"]")
            recordCoreCall("AndroidEngineLog", "[\"VPN stop requested from Flutter\"]")
            DropoVpnService.stop(this)
            result.success(successJson("Android VPN stop requested"))
            return
        }
        if (pendingConnectResult != null) {
            result.success(errorJson("VPN start is already pending"))
            return
        }
        pendingConnectResult = result
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            androidNotificationsEnabled() &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(
                arrayOf(Manifest.permission.POST_NOTIFICATIONS),
                REQUEST_POST_NOTIFICATIONS,
            )
            return
        }
        continueVpnPrepare()
    }

    private fun continueVpnPrepare() {
        val prepareIntent = VpnService.prepare(this)
        if (prepareIntent != null) {
            @Suppress("DEPRECATION")
            startActivityForResult(prepareIntent, REQUEST_VPN_PREPARE)
            return
        }
        startVpnAndResolve()
    }

    private fun startVpnAndResolve() {
        try {
            DropoVpnRuntime.setStarting("Android VPN start requested")
            recordCoreCall("AndroidServiceState", "[\"starting\",\"Android VPN start requested\",\"\"]")
            DropoVpnService.start(this)
            resolvePendingConnect(successJson("Android VPN start requested"))
        } catch (error: Throwable) {
            val message = error.message ?: error.javaClass.simpleName
            DropoVpnRuntime.setFailed(message)
            recordCoreCall(
                "AndroidServiceState",
                "[\"failed\",${JSONObject.quote(message)},${JSONObject.quote(message)}]",
            )
            DropoVpnService.stop(this)
            resolvePendingConnect(errorJson(message))
        }
    }

    private fun resolvePendingConnect(value: String) {
        val result = pendingConnectResult
        pendingConnectResult = null
        result?.success(value)
    }

    private fun ensureCoreStarted(): String {
        return Dropoandroid.ensureStarted(filesDir.absolutePath, packageVersionName())
    }

    private fun androidNotificationsEnabled(): Boolean {
        return runCatching {
            JSONObject(Dropoandroid.call("GetAppConfig", "[]")).optBoolean("notifications", true)
        }.getOrDefault(true)
    }

    private fun recordCoreCall(method: String, argsJson: String) {
        coreExecutor.execute {
            runCatching { Dropoandroid.call(method, argsJson) }
        }
    }

    private fun logDropoSpace(message: String) {
        val line = "dropo space: $message"
        DropoVpnRuntime.appendLog(line)
        recordCoreCall("AndroidEngineLog", "[${JSONObject.quote(line)}]")
    }

    private fun buildDiagnostics(): String {
        val coreStatus = runCatching { Dropoandroid.status() }
            .getOrElse { errorJson(it.message ?: it.javaClass.simpleName) }
        val coreDiagnostics = runCatching {
            JSONObject(Dropoandroid.call("AndroidDiagnostics", "[]")).optString("text")
        }.getOrElse { "core diagnostics failed: ${it.message ?: it.javaClass.simpleName}" }
        val serviceSnapshot = JSONObject(DropoVpnRuntime.snapshot()).toString(2)
        val nativeLogs = DropoVpnRuntime.recentLogs().joinToString("\n")
        val logcat = readLogcatTail()
        val text = listOf(
            "dropo Android native diagnostics",
            "package: $packageName",
            "version: ${packageVersionName()}",
            "sdk: ${Build.VERSION.SDK_INT}",
            "device: ${Build.MANUFACTURER} ${Build.MODEL}",
            "abis: ${Build.SUPPORTED_ABIS.joinToString(",")}",
            "",
            "service snapshot:",
            serviceSnapshot,
            "",
            "core status:",
            coreStatus,
            "",
            coreDiagnostics,
            "",
            "native recent logs:",
            nativeLogs.ifBlank { "empty" },
            "",
            "logcat tail:",
            logcat.ifBlank { "empty" },
        ).joinToString("\n")
        return JSONObject(mapOf("success" to true, "text" to text)).toString()
    }

    private fun buildAndroidCompatibility(): String {
        val apps = buildRiskAppStatuses()
        val installedCount = apps.count { it["installed"] == true }
        val inSpaceCount = apps.count { it["inDropoSpace"] == true }
        val hasDropoSpace = hasDropoSpaceProfile()
        val canCreate = canProvisionManagedProfile()
        val payload = mapOf(
            "success" to true,
            "supported" to true,
            "manufacturer" to Build.MANUFACTURER,
            "model" to Build.MODEL,
            "deviceLabel" to listOf(Build.MANUFACTURER, Build.MODEL)
                .filter { it.isNotBlank() }
                .joinToString(" ")
                .trim(),
            "androidVersion" to Build.VERSION.RELEASE,
            "sdk" to Build.VERSION.SDK_INT,
            "dropoSpaceSupported" to (hasDropoSpace || canCreate),
            "dropoSpaceReady" to hasDropoSpace,
            "dropoSpaceCanCreate" to canCreate,
            "privateSpaceSupported" to (Build.VERSION.SDK_INT >= 35),
            "promptDismissed" to prefs().getBoolean(PREF_COMPAT_PROMPT_DISMISSED, false),
            "installedRiskCount" to installedCount,
            "inDropoSpaceCount" to inSpaceCount,
            "searchUrl" to cloneHelpSearchUrl(),
            "riskApps" to apps,
        )
        return JSONObject(payload).toString()
    }

    private fun buildRiskAppStatuses(): List<Map<String, Any?>> {
        val profileUsers = dropoSpaceUsers()
        val rawStatuses = RISK_APPS.map { app ->
            val installed = isPackageInstalledInCurrentUser(app.packageName)
            val inSpace = profileUsers.any { user ->
                launchableActivities(app.packageName, user).isNotEmpty()
            }
            mapOf(
                "packageName" to app.packageName,
                "name" to app.name,
                "installed" to installed,
                "inDropoSpace" to inSpace,
                "status" to when {
                    inSpace -> "in_space"
                    installed -> "installed"
                    else -> "not_installed"
                },
            )
        }
        return rawStatuses
            .groupBy { it["name"]?.toString().orEmpty() }
            .values
            .map { variants ->
                val selected =
                    variants.firstOrNull { it["inDropoSpace"] == true }
                        ?: variants.firstOrNull { it["installed"] == true }
                        ?: variants.first()
                val installed = variants.any { it["installed"] == true }
                val inSpace = variants.any { it["inDropoSpace"] == true }
                selected + mapOf(
                    "installed" to installed,
                    "inDropoSpace" to inSpace,
                    "status" to when {
                        inSpace -> "in_space"
                        installed -> "installed"
                        else -> "not_installed"
                    },
                )
            }
    }

    private fun canProvisionManagedProfile(): Boolean {
        return runCatching {
            managedProfileProvisioningBlocker(managedProfileProvisioningIntent()) == null
        }.getOrDefault(false)
    }

    private fun hasDropoSpaceProfile(): Boolean {
        return dropoSpaceUsers().any { user ->
            launchableActivities(packageName, user).isNotEmpty()
        }
    }

    private fun dropoSpaceUsers(): List<android.os.UserHandle> {
        val current = android.os.Process.myUserHandle()
        return runCatching {
            val launcher = getSystemService(LauncherApps::class.java)
            launcher?.profiles.orEmpty().filter { user ->
                user != current && launchableActivities(packageName, user).isNotEmpty()
            }
        }.getOrDefault(emptyList())
    }

    private fun launchableActivities(
        targetPackage: String,
        user: android.os.UserHandle,
    ): List<android.content.pm.LauncherActivityInfo> {
        return runCatching {
            getSystemService(LauncherApps::class.java)
                ?.getActivityList(targetPackage, user)
                .orEmpty()
        }.getOrDefault(emptyList())
    }

    private fun isPackageInstalledInCurrentUser(targetPackage: String): Boolean {
        return runCatching {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                packageManager.getPackageInfo(
                    targetPackage,
                    PackageManager.PackageInfoFlags.of(0),
                )
            } else {
                @Suppress("DEPRECATION")
                packageManager.getPackageInfo(targetPackage, 0)
            }
            true
        }.getOrDefault(false)
    }

    private fun managedProfileProvisioningIntent(): Intent {
        val admin = ComponentName(this, DropoDeviceAdminReceiver::class.java)
        return Intent(DevicePolicyManager.ACTION_PROVISION_MANAGED_PROFILE)
            .putExtra(DevicePolicyManager.EXTRA_PROVISIONING_DEVICE_ADMIN_COMPONENT_NAME, admin)
            .putExtra(DevicePolicyManager.EXTRA_PROVISIONING_SKIP_ENCRYPTION, false)
    }

    private fun managedProfileProvisioningBlocker(intent: Intent): String? {
        val dpm = getSystemService(DevicePolicyManager::class.java)
            ?: return "DevicePolicyManager недоступен"
        if (!dpm.isProvisioningAllowed(DevicePolicyManager.ACTION_PROVISION_MANAGED_PROFILE)) {
            return "Android не разрешает создание рабочего профиля для этого пользователя"
        }
        if (intent.resolveActivity(packageManager) == null) {
            return "На устройстве нет системного мастера создания рабочего профиля"
        }
        return null
    }

    private fun startManagedProfileProvisioning(): String {
        logDropoSpace(
            "create requested sdk=${Build.VERSION.SDK_INT} device=${Build.MANUFACTURER} ${Build.MODEL}",
        )
        if (hasDropoSpaceProfile()) {
            logDropoSpace("create skipped: profile already exists")
            return JSONObject(
                mapOf(
                    "success" to true,
                    "action" to "already_ready",
                    "message" to "Dropo Space is already created",
                ),
            ).toString()
        }
        val intent = managedProfileProvisioningIntent()
        val blocker = managedProfileProvisioningBlocker(intent)
        if (blocker != null) {
            logDropoSpace("create blocked: $blocker")
            return JSONObject(
                mapOf(
                    "success" to false,
                    "action" to "unsupported",
                    "error" to "Создание Dropo Space недоступно на этом устройстве: $blocker",
                    "diagnostic" to blocker,
                    "searchUrl" to cloneHelpSearchUrl(),
                ),
            ).toString()
        }
        return try {
            @Suppress("DEPRECATION")
            startActivityForResult(intent, REQUEST_MANAGED_PROFILE_PROVISIONING)
            logDropoSpace("system provisioning activity started")
            JSONObject(
                mapOf(
                    "success" to true,
                    "action" to "provisioning_started",
                    "message" to "Открылся системный мастер создания Dropo Space",
                    "needsRefresh" to true,
                    "searchUrl" to cloneHelpSearchUrl(),
                ),
            ).toString()
        } catch (error: ActivityNotFoundException) {
            dropoSpaceStartError("create failed: no provisioning activity", error)
        } catch (error: SecurityException) {
            dropoSpaceStartError("create failed: security exception", error)
        } catch (error: IllegalArgumentException) {
            dropoSpaceStartError("create failed: invalid provisioning request", error)
        }
    }

    private fun dropoSpaceStartError(prefix: String, error: Throwable): String {
        val detail = error.message ?: error.javaClass.simpleName
        logDropoSpace("$prefix: $detail")
        return JSONObject(
            mapOf(
                "success" to false,
                "action" to "provisioning_failed",
                "error" to "Не удалось открыть системный мастер Dropo Space: $detail",
                "diagnostic" to detail,
                "searchUrl" to cloneHelpSearchUrl(),
            ),
        ).toString()
    }

    private fun movePackageToDropoSpace(targetPackage: String): String {
        val packageName = targetPackage.trim()
        val app = RISK_APPS.firstOrNull { it.packageName == packageName }
        val label = app?.name ?: packageName
        logDropoSpace("configure requested package=$packageName label=$label")
        if (packageName.isBlank()) {
            logDropoSpace("configure rejected: empty package")
            return errorJson("Package name is empty")
        }
        if (app == null) {
            logDropoSpace("configure rejected: package is outside the reviewed allowlist")
            return errorJson("Package is not supported by Dropo Space")
        }
        if (!isPackageInstalledInCurrentUser(packageName)) {
            logDropoSpace("configure rejected: package is not installed in current user")
            return JSONObject(
                mapOf(
                    "success" to false,
                    "action" to "not_installed",
                    "packageName" to packageName,
                    "appName" to label,
                    "error" to "Приложение $label не установлено в основном профиле",
                    "searchUrl" to cloneHelpSearchUrl(),
                ),
            ).toString()
        }
        val profileUsers = dropoSpaceUsers()
        if (profileUsers.isEmpty()) {
            val provisioning = startManagedProfileProvisioning()
            val data = JSONObject(provisioning)
            if (data.optString("action") == "provisioning_started") {
                data.put("action", "provisioning_started_for_app")
                data.put(
                    "message",
                    "Сначала создайте Dropo Space. После завершения вернитесь и нажмите «Настроить» для $label ещё раз.",
                )
            }
            data.put("packageName", packageName)
            data.put("appName", label)
            return data.toString()
        }
        val profileUser = profileUsers.first()
        if (launchableActivities(packageName, profileUser).isNotEmpty()) {
            logDropoSpace("package already launchable in profile: $packageName")
            val shortcut = requestDropoSpaceShortcutJson(packageName)
            shortcut.put("success", true)
            shortcut.put("action", "already_in_space")
            shortcut.put("message", "Приложение уже есть в Dropo Space")
            return shortcut.toString()
        }

        val market = openMarketInProfile(packageName, profileUser)
        if (market.success) {
            logDropoSpace("market opened in profile for $packageName")
        } else {
            logDropoSpace("market open failed for $packageName: ${market.error}")
        }
        return JSONObject(
            mapOf(
                "success" to market.success,
                "action" to if (market.success) "open_market" else "manual_install_required",
                "packageName" to packageName,
                "appName" to label,
                "message" to if (market.success) {
                    "Открылся магазин приложений в Dropo Space. Установите приложение там и вернитесь в dropo."
                } else {
                    "Не удалось открыть магазин в Dropo Space. Установите копию приложения внутри рабочего профиля вручную."
                },
                "error" to market.error,
                "searchUrl" to cloneHelpSearchUrl(),
            ),
        ).toString()
    }

    private fun openMarketInProfile(
        targetPackage: String,
        user: android.os.UserHandle,
    ): DropoSpaceStartResult {
        return runCatching {
            val launcher = getSystemService(LauncherApps::class.java)
                ?: return DropoSpaceStartResult(false, "LauncherApps service недоступен")
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.VANILLA_ICE_CREAM) {
                val intentSender = launcher.getAppMarketActivityIntent(targetPackage, user)
                if (intentSender != null) {
                    startIntentSender(intentSender, null, 0, 0, 0)
                    return DropoSpaceStartResult(true)
                }
            }
            // API < 35: getAppMarketActivityIntent недоступен — открываем главный
            // экран магазина внутри профиля, приложение придётся найти вручную.
            for (store in MARKET_PACKAGES) {
                val activity = launchableActivities(store, user).firstOrNull() ?: continue
                launcher.startMainActivity(activity.componentName, user, null, null)
                return DropoSpaceStartResult(true)
            }
            DropoSpaceStartResult(false, "Магазин приложений недоступен в Dropo Space")
        }.getOrElse { error ->
            val message = when (error) {
                is IntentSender.SendIntentException -> "Не удалось открыть intent магазина"
                is SecurityException -> "Android запретил открыть магазин в рабочем профиле"
                else -> error.message ?: error.javaClass.simpleName
            }
            DropoSpaceStartResult(false, message)
        }
    }

    private fun requestDropoSpaceShortcut(packageName: String): String {
        return requestDropoSpaceShortcutJson(packageName).toString()
    }

    private fun requestDropoSpaceShortcutJson(packageName: String): JSONObject {
        if (packageName.isBlank()) {
            logDropoSpace("shortcut rejected: empty package")
            return JSONObject(mapOf("success" to false, "error" to "Package name is empty"))
        }
        val app = RISK_APPS.firstOrNull { it.packageName == packageName }
            ?: return JSONObject(
                mapOf(
                    "success" to false,
                    "action" to "unsupported_package",
                    "error" to "Package is not supported by Dropo Space",
                ),
            )
        val label = app?.name ?: packageName
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            logDropoSpace("shortcut unsupported: sdk=${Build.VERSION.SDK_INT}")
            return JSONObject(
                mapOf(
                    "success" to false,
                    "action" to "shortcut_unsupported",
                    "error" to "Лаунчер не поддерживает закрепление ярлыков",
                ),
            )
        }
        val manager = getSystemService(ShortcutManager::class.java)
        if (manager?.isRequestPinShortcutSupported != true) {
            logDropoSpace("shortcut unsupported by launcher")
            return JSONObject(
                mapOf(
                    "success" to false,
                    "action" to "shortcut_unsupported",
                    "error" to "Лаунчер не поддерживает закрепление ярлыков",
                ),
            )
        }
        val shortcutId = "dropo-space-$packageName"
        val launchIntent = Intent(this, MainActivity::class.java)
            .setAction(ACTION_DROPO_SPACE_SHORTCUT)
            .putExtra(EXTRA_DROPO_SPACE_PACKAGE, packageName)
            .addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val shortcut = ShortcutInfo.Builder(this, shortcutId)
            .setShortLabel("$label Dropo")
            .setLongLabel("$label - Dropo Space")
            .setIcon(Icon.createWithResource(this, R.mipmap.ic_launcher))
            .setIntent(launchIntent)
            .build()
        val callback = PendingIntent.getActivity(
            this,
            shortcutId.hashCode(),
            manager.createShortcutResultIntent(shortcut),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        ).intentSender
        val requested = manager.requestPinShortcut(shortcut, callback)
        logDropoSpace("shortcut request package=$packageName requested=$requested")
        return JSONObject(
            mapOf(
                "success" to requested,
                "action" to if (requested) "shortcut_requested" else "shortcut_failed",
                "message" to if (requested) "Android запросил закрепление ярлыка Dropo Space" else "Не удалось запросить ярлык",
                "error" to if (requested) "" else "Лаунчер отклонил запрос на ярлык",
            ),
        )
    }

    private fun handleDropoSpaceShortcut(intent: Intent?) {
        if (intent?.action != ACTION_DROPO_SPACE_SHORTCUT) {
            return
        }
        val targetPackage = intent.getStringExtra(EXTRA_DROPO_SPACE_PACKAGE).orEmpty()
        if (targetPackage.isBlank() || RISK_APPS.none { it.packageName == targetPackage }) {
            logDropoSpace("shortcut rejected: unsupported package")
            return
        }
        mainHandler.postDelayed({
            if (!launchPackageFromDropoSpace(targetPackage)) {
                logDropoSpace(
                    "shortcut launch failed for $targetPackage; keeping dropo open instead of launching the main-profile app",
                )
            }
        }, 350)
    }

    private fun launchPackageFromDropoSpace(targetPackage: String): Boolean {
        val launcher = getSystemService(LauncherApps::class.java) ?: return false
        for (user in dropoSpaceUsers()) {
            val activity = launchableActivities(targetPackage, user).firstOrNull() ?: continue
            return runCatching {
                launcher.startMainActivity(activity.componentName, user, null, null)
                logDropoSpace("shortcut launched profile app $targetPackage")
                true
            }.getOrElse { error ->
                logDropoSpace(
                    "shortcut launch failed for $targetPackage: ${error.message ?: error.javaClass.simpleName}",
                )
                false
            }
        }
        return false
    }

    private fun openCloneHelpSearch(): String {
        val url = cloneHelpSearchUrl()
        val intent = Intent(Intent.ACTION_VIEW, Uri.parse(url))
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        startActivity(intent)
        return JSONObject(
            mapOf(
                "success" to true,
                "url" to url,
                "message" to "Открыт поиск инструкции по клонированию приложений",
            ),
        ).toString()
    }

    private fun openExternalUrl(url: String): String {
        val trimmed = url.trim()
        if (trimmed.isBlank()) {
            return errorJson("URL is empty")
        }
        val uri = runCatching { Uri.parse(trimmed) }.getOrNull()
            ?: return errorJson("Invalid URL")
        val scheme = uri.scheme?.lowercase().orEmpty()
        if (scheme !in setOf("https", "http", "tg", "market")) {
            return errorJson("Unsupported URL scheme: $scheme")
        }
        val intent = Intent(Intent.ACTION_VIEW, uri).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return runCatching {
            startActivity(intent)
            JSONObject(mapOf("success" to true, "url" to trimmed)).toString()
        }.getOrElse { error ->
            errorJson(error.message ?: error.javaClass.simpleName)
        }
    }

    private fun cloneHelpSearchUrl(): String {
        val manufacturer = Build.MANUFACTURER.ifBlank { "Android" }
        val model = Build.MODEL.ifBlank { "phone" }
        val query = "$manufacturer $model Android ${Build.VERSION.RELEASE} как клонировать приложение"
        return "https://www.google.com/search?q=${java.net.URLEncoder.encode(query, "UTF-8")}"
    }

    private fun prefs() = getSharedPreferences("dropo_android", Context.MODE_PRIVATE)

    private fun readLogcatTail(): String {
        if (!isDebugBuild()) {
            return "logcat collection is disabled in release builds"
        }
        return runCatching {
            val process = ProcessBuilder("logcat", "-d", "-t", "220")
                .redirectErrorStream(true)
                .start()
            if (!process.waitFor(2, TimeUnit.SECONDS)) {
                process.destroyForcibly()
                return@runCatching "logcat timed out"
            }
            process.inputStream.bufferedReader().use { reader ->
                reader.readText()
                    .lineSequence()
                    .filter { line ->
                        line.contains("DropoVpnService") ||
                            line.contains("dropo", ignoreCase = true) ||
                            line.contains("sing-box", ignoreCase = true) ||
                            line.contains(packageName)
                    }
                    .toList()
                    .takeLast(160)
                    .joinToString("\n")
            }
        }.getOrElse { "logcat unavailable: ${it.message ?: it.javaClass.simpleName}" }
    }

    private fun packageVersionName(): String {
        return try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                packageManager.getPackageInfo(
                    packageName,
                    PackageManager.PackageInfoFlags.of(0),
                ).versionName ?: "dev"
            } else {
                @Suppress("DEPRECATION")
                packageManager.getPackageInfo(packageName, 0).versionName ?: "dev"
            }
        } catch (_: Throwable) {
            "dev"
        }
    }

    private fun stringArg(call: MethodCall, name: String, fallback: String = ""): String {
        return call.argument<String>(name) ?: fallback
    }

    private fun boolArg(call: MethodCall, name: String): Boolean {
        return call.argument<Boolean>(name) ?: false
    }

    private fun longArg(call: MethodCall, name: String): Long {
        val raw = (call.arguments as? Map<*, *>)?.get(name)
        return when (raw) {
            is Long -> raw
            is Int -> raw.toLong()
            is Number -> raw.toLong()
            else -> 0L
        }
    }

    private fun errorJson(message: String): String {
        return JSONObject(mapOf("success" to false, "error" to message)).toString()
    }

    private fun successJson(message: String): String {
        return JSONObject(mapOf("success" to true, "message" to message)).toString()
    }

    private data class DropoSpaceStartResult(
        val success: Boolean,
        val error: String = "",
    )

    companion object {
        private const val CHANNEL_CORE = "dropo/core"
        private const val CHANNEL_EVENTS = "dropo/core/events"
        private val BACKGROUND_CORE_METHODS = setOf(
            "ensureStarted",
            "status",
            "logs",
            "events",
            "serviceStatus",
            "diagnostics",
            "call",
            "shutdown",
        )
        private val ANDROID_NATIVE_METHODS = setOf(
            "androidCompatibility",
            "androidSetCompatibilityPromptDismissed",
            "androidCreateDropoSpace",
            "androidMoveToDropoSpace",
            "androidRequestDropoSpaceShortcut",
            "androidOpenCloneHelpSearch",
            "androidOpenExternal",
        )
        private const val REQUEST_VPN_PREPARE = 4101
        private const val REQUEST_POST_NOTIFICATIONS = 4102
        private const val REQUEST_MANAGED_PROFILE_PROVISIONING = 4103
        private const val PREF_COMPAT_PROMPT_DISMISSED = "compat_prompt_dismissed"
        private const val ACTION_DROPO_SPACE_SHORTCUT = "in.droponevedimka.dropo.action.DROPO_SPACE_SHORTCUT"
        private const val EXTRA_DROPO_SPACE_PACKAGE = "dropo_space_package"

        private val MARKET_PACKAGES = listOf("com.android.vending", "ru.vk.store")

        private data class RiskApp(val packageName: String, val name: String)

        private val RISK_APPS = listOf(
            RiskApp("ru.oneme.app", "MAX"),
            RiskApp("ru.rostel", "Госуслуги"),
            RiskApp("ru.sberbankmobile", "СберБанк"),
            RiskApp("ru.sberbankmobile_new", "СберБанк"),
            RiskApp("com.idamob.tinkoff.android", "Т-Банк"),
            RiskApp("ru.vtb24.mobilebanking.android", "ВТБ Онлайн"),
            RiskApp("ru.alfabank.mobile.android", "Альфа-Банк"),
            RiskApp("ru.ozon.app.android", "Ozon"),
            RiskApp("com.wildberries.ru", "Wildberries"),
            RiskApp("com.avito.android", "Avito"),
            RiskApp("com.vkontakte.android", "ВКонтакте"),
            RiskApp("ru.ok.android", "Одноклассники"),
            RiskApp("ru.vk.store", "RuStore"),
            RiskApp("ru.rutube.app", "Rutube"),
            RiskApp("ru.yandex.yandexmaps", "Яндекс Карты"),
            RiskApp("com.yandex.browser", "Яндекс Браузер"),
            RiskApp("ru.yandex.music", "Яндекс Музыка"),
            RiskApp("ru.kinopoisk", "Кинопоиск"),
            RiskApp("ru.dublgis.dgismobile", "2ГИС"),
            RiskApp("ru.mts.mymts", "Мой МТС"),
            RiskApp("ru.sbermegamarket", "МегаМаркет"),
            RiskApp("ru.samokat.android", "Самокат"),
        )
    }
}
