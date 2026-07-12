package `in`.droponevedimka.dropo

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.PackageManager.NameNotFoundException
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.IpPrefix
import android.net.Network
import android.net.NetworkCapabilities
import android.net.ProxyInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.os.Process
import android.os.SystemClock
import android.system.OsConstants
import android.util.Base64
import android.util.Log
import androidx.annotation.RequiresApi
import dropoandroid.CommandServer
import dropoandroid.CommandServerHandler
import dropoandroid.ConnectionOwner
import dropoandroid.Dropoandroid
import dropoandroid.InterfaceUpdateListener
import dropoandroid.LocalDNSTransport
import dropoandroid.NetworkInterfaceIterator
import dropoandroid.OverrideOptions
import dropoandroid.PlatformInterface
import dropoandroid.RoutePrefix
import dropoandroid.StringIterator
import dropoandroid.SystemProxyStatus
import dropoandroid.TunOptions
import dropoandroid.WIFIState
import dropoandroid.NetworkInterface as BoxNetworkInterface
import dropoandroid.Notification as BoxNotification
import dropoandroid.SetupOptions as BoxSetupOptions
import java.net.Inet6Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.InterfaceAddress
import java.security.KeyStore
import java.security.cert.X509Certificate
import java.util.Locale
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import org.json.JSONObject
import java.net.NetworkInterface as JavaNetworkInterface

class DropoVpnService :
    VpnService(),
    PlatformInterface,
    CommandServerHandler {
    private val executor = Executors.newSingleThreadExecutor()
    private val connectivity by lazy { getSystemService(ConnectivityManager::class.java) }

    @Volatile
    private var tunInterface: ParcelFileDescriptor? = null

    @Volatile
    private var commandServer: CommandServer? = null

    @Volatile
    private var starting = false

    @Volatile
    private var stopping = false

    @Volatile
    private var lastCoreDebugLogAt = 0L

    @Volatile
    private var lastRuntimeSingBoxLogAt = 0L

    @Volatile
    private var verboseSingBoxLogs = false

    private var interfaceUpdateListener: InterfaceUpdateListener? = null
    private var networkCallbackRegistered = false
    private val networkCallback =
        object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) {
                notifyDefaultInterfaceAsync()
            }

            override fun onLost(network: Network) {
                notifyDefaultInterfaceAsync()
            }

            override fun onCapabilitiesChanged(
                network: Network,
                networkCapabilities: NetworkCapabilities,
            ) {
                notifyDefaultInterfaceAsync()
            }
        }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        return when (intent?.action) {
            ACTION_STOP -> {
                DropoVpnRuntime.setDisconnecting("VPN останавливается")
                stopVpn(stopSelf = true)
                START_NOT_STICKY
            }
            else -> {
                startVpn()
                START_STICKY
            }
        }
    }

    override fun onRevoke() {
        stopVpn(stopSelf = true)
        super.onRevoke()
    }

    override fun onDestroy() {
        stopVpn(stopSelf = false)
        executor.shutdown()
        super.onDestroy()
    }

    fun protectFileDescriptor(socket: Int): Boolean = protect(socket)

    private fun startVpn() {
        createNotificationChannel()
        if (starting || commandServer != null) {
            executor.execute { coreLog("start skipped: VPN service is already running") }
            if (commandServer != null) {
                DropoVpnRuntime.setConnected("VPN уже работает")
            }
            startForegroundCompat(buildNotification("VPN работает"))
            return
        }
        starting = true
        stopping = false
        DropoVpnRuntime.setStarting("VPN запускается")
        startForegroundCompat(buildNotification("VPN запускается"))
        executor.execute {
            try {
                Dropoandroid.ensureStarted(filesDir.absolutePath, packageVersionName())
                syncCoreServiceState("starting", "VPN запускается")
                Dropoandroid.call("AndroidEngineStarting", "[]")
                coreLog("startForeground requested")
                startEngine()
            } catch (error: Throwable) {
                val message = describeError(error)
                Log.e(TAG, "startEngine failed: $message", error)
                coreError(message)
                stopVpn(stopSelf = true, failureMessage = message)
            } finally {
                starting = false
            }
        }
    }

    private fun startEngine() {
        if (commandServer != null) {
            coreLog("start skipped: command server is already running")
            Dropoandroid.setConnected(true)
            DropoVpnRuntime.setConnected("VPN уже работает")
            syncCoreServiceState("connected", "VPN уже работает")
            return
        }

        coreLog("libbox setup")
        ensureLibboxSetup(this)
        coreLog("building sing-box config")
        val configResult = JSONObject(Dropoandroid.buildSingBoxConfig())
        if (!configResult.optBoolean("success")) {
            error(configResult.optString("error", "Failed to build Android sing-box config"))
        }
        val config = configResult.getString("config")
        if (configResult.optBoolean("cached")) {
            coreLog("using cached sing-box config: ${configResult.optString("warning", "subscription refresh failed")}")
        }
        verboseSingBoxLogs = androidSingBoxVerboseLogging()
        if (verboseSingBoxLogs) {
            coreLog("sing-box log capture enabled")
        }
        coreLog("checking sing-box config")
        Dropoandroid.checkConfig(config)

        coreLog("starting command server")
        val server = CommandServer(this, this)
        server.start()
        coreLog("starting sing-box service")
        server.startOrReloadService(config, OverrideOptions().apply { autoRedirect = false })
        commandServer = server

        Dropoandroid.setConnected(true)
        val version = configResult.optString("version", Dropoandroid.version())
        DropoVpnRuntime.setConnected("VPN работает")
        syncCoreServiceState("connected", "VPN работает")
        coreLog("sing-box $version is active")
        startForegroundCompat(buildNotification("VPN работает"))
    }

    private fun stopVpn(stopSelf: Boolean, failureMessage: String? = null) {
        if (stopping) return
        stopping = true
        if (failureMessage == null) {
            DropoVpnRuntime.setDisconnecting("VPN останавливается")
        }
        executor.execute {
            val server = commandServer
            val hadRuntime = server != null || tunInterface != null || starting
            starting = false
            commandServer = null
            runCatching { server?.closeService() }
            runCatching { server?.close() }
            closeTun()
            closeDefaultInterfaceMonitor(interfaceUpdateListener)
            if (hadRuntime) {
                coreLog("VpnService stopped")
            }
            verboseSingBoxLogs = false
            Dropoandroid.setConnected(false)
            if (failureMessage == null) {
                DropoVpnRuntime.setStopped("VPN остановлен")
                syncCoreServiceState("stopped", "VPN остановлен")
            } else {
                DropoVpnRuntime.setFailed(failureMessage)
                syncCoreServiceState("failed", failureMessage, failureMessage)
            }
            stopForegroundCompat()
            stopping = false
            if (stopSelf) {
                stopSelf()
            }
        }
    }

    private fun closeTun() {
        runCatching { tunInterface?.close() }
        tunInterface = null
    }

    override fun openTun(options: TunOptions): Int {
        if (prepare(this) != null) error("android: missing VPN permission")
        coreLog("open TUN mtu=${options.mtu}")

        val builder = Builder()
            .setSession("dropo")
            .setMtu(options.mtu.coerceAtLeast(1280))

        builder.setMetered(false)

        var hasInet4 = false
        val inet4Address = options.inet4Address
        while (inet4Address.hasNext()) {
            val address = inet4Address.next()
            builder.addAddress(address.address(), address.prefix())
            hasInet4 = true
        }

        var hasInet6 = false
        val inet6Address = options.inet6Address
        while (inet6Address.hasNext()) {
            val address = inet6Address.next()
            builder.addAddress(address.address(), address.prefix())
            hasInet6 = true
        }

        if (options.autoRoute) {
            runCatching {
                val dnsAddress = options.dnsServerAddress?.value
                if (!dnsAddress.isNullOrBlank()) {
                    builder.addDnsServer(dnsAddress)
                }
            }.onFailure {
                Log.w(TAG, "DNS hijack address unavailable", it)
            }
            addRoutes(builder, options, hasInet4, hasInet6)
            addApplications(builder, options)
        }

        if (options.isHTTPProxyEnabled) {
            builder.setHttpProxy(
                ProxyInfo.buildDirectProxy(
                    options.httpProxyServer,
                    options.httpProxyServerPort,
                    options.httpProxyBypassDomain.toList(),
                ),
            )
        }

        val pfd = builder.establish() ?: error("android: VPN establish returned null")
        tunInterface = pfd
        coreLog("TUN established fd=${pfd.fd}")
        return pfd.fd
    }

    private fun addRoutes(
        builder: Builder,
        options: TunOptions,
        hasInet4: Boolean,
        hasInet6: Boolean,
    ) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            addRoutesWithExcludes(builder, options, hasInet4, hasInet6)
        } else {
            addRoutesLegacy(builder, options, hasInet4, hasInet6)
        }
    }

    @RequiresApi(Build.VERSION_CODES.TIRAMISU)
    private fun addRoutesWithExcludes(
        builder: Builder,
        options: TunOptions,
        hasInet4: Boolean,
        hasInet6: Boolean,
    ) {
        var added4 = false
        val inet4Routes = options.inet4RouteAddress
        while (inet4Routes.hasNext()) {
            builder.addRoute(inet4Routes.next().toIpPrefix())
            added4 = true
        }
        if (!added4 && hasInet4) {
            builder.addRoute("0.0.0.0", 0)
        }

        var added6 = false
        val inet6Routes = options.inet6RouteAddress
        while (inet6Routes.hasNext()) {
            builder.addRoute(inet6Routes.next().toIpPrefix())
            added6 = true
        }
        if (!added6 && hasInet6) {
            builder.addRoute("::", 0)
        }

        val inet4Exclude = options.inet4RouteExcludeAddress
        while (inet4Exclude.hasNext()) {
            builder.excludeRoute(inet4Exclude.next().toIpPrefix())
        }
        val inet6Exclude = options.inet6RouteExcludeAddress
        while (inet6Exclude.hasNext()) {
            builder.excludeRoute(inet6Exclude.next().toIpPrefix())
        }
    }

    // API < 33: Builder.excludeRoute is unavailable, so route exclusions cannot be
    // expressed directly. RouteRange carries the merged prefixes sing-box computes
    // with the exclusions already subtracted.
    private fun addRoutesLegacy(
        builder: Builder,
        options: TunOptions,
        hasInet4: Boolean,
        hasInet6: Boolean,
    ) {
        var added4 = false
        val inet4Ranges = options.inet4RouteRange
        while (inet4Ranges.hasNext()) {
            val prefix = inet4Ranges.next()
            builder.addRoute(prefix.address(), prefix.prefix())
            added4 = true
        }
        if (!added4 && hasInet4) {
            builder.addRoute("0.0.0.0", 0)
        }

        var added6 = false
        val inet6Ranges = options.inet6RouteRange
        while (inet6Ranges.hasNext()) {
            val prefix = inet6Ranges.next()
            builder.addRoute(prefix.address(), prefix.prefix())
            added6 = true
        }
        if (!added6 && hasInet6) {
            builder.addRoute("::", 0)
        }
    }

    private fun addApplications(builder: Builder, options: TunOptions) {
        val includePackages = options.includePackage.toList()
        val excludePackages = options.excludePackage.toList().toMutableList()
        if (includePackages.isEmpty() && packageName !in excludePackages) {
            excludePackages += packageName
        }

        for (packageName in includePackages) {
            runCatching { builder.addAllowedApplication(packageName) }
                .onFailure { if (it is NameNotFoundException) Log.w(TAG, "unknown allowed app $packageName") }
        }

        for (packageName in excludePackages) {
            runCatching { builder.addDisallowedApplication(packageName) }
                .onFailure { if (it is NameNotFoundException) Log.w(TAG, "unknown disallowed app $packageName") }
        }
    }

    override fun usePlatformAutoDetectInterfaceControl(): Boolean = true

    override fun autoDetectInterfaceControl(fd: Int) {
        if (!protect(fd)) {
            error("android: failed to protect socket fd=$fd from the VPN tunnel")
        }
    }

    override fun useProcFS(): Boolean = false

    override fun findConnectionOwner(
        ipProtocol: Int,
        sourceAddress: String,
        sourcePort: Int,
        destinationAddress: String,
        destinationPort: Int,
    ): ConnectionOwner {
        val uid = connectivity.getConnectionOwnerUid(
            ipProtocol,
            InetSocketAddress(sourceAddress, sourcePort),
            InetSocketAddress(destinationAddress, destinationPort),
        )
        if (uid == Process.INVALID_UID) error("android: connection owner not found")
        val packages = packageManager.getPackagesForUid(uid).orEmpty()
        return ConnectionOwner().apply {
            userId = uid
            userName = packages.firstOrNull().orEmpty()
            setAndroidPackageNames(StringArray(packages.asList().iterator()))
        }
    }

    override fun startDefaultInterfaceMonitor(listener: InterfaceUpdateListener) {
        interfaceUpdateListener = listener
        if (!networkCallbackRegistered) {
            connectivity.registerDefaultNetworkCallback(networkCallback)
            networkCallbackRegistered = true
        }
        notifyDefaultInterfaceAsync()
    }

    override fun closeDefaultInterfaceMonitor(listener: InterfaceUpdateListener?) {
        interfaceUpdateListener = null
        if (networkCallbackRegistered) {
            runCatching { connectivity.unregisterNetworkCallback(networkCallback) }
            networkCallbackRegistered = false
        }
    }

    override fun getInterfaces(): NetworkInterfaceIterator {
        val javaInterfaces = JavaNetworkInterface.getNetworkInterfaces().toList()
        val result = mutableListOf<BoxNetworkInterface>()
        for (network in connectivity.allNetworks) {
            val linkProperties = connectivity.getLinkProperties(network) ?: continue
            val capabilities = connectivity.getNetworkCapabilities(network) ?: continue
            val name = linkProperties.interfaceName ?: continue
            val javaInterface = javaInterfaces.firstOrNull { it.name == name } ?: continue
            result += BoxNetworkInterface().apply {
                this.name = name
                index = javaInterface.index
                mtu = runCatching { javaInterface.mtu }.getOrDefault(1500)
                addresses = StringArray(javaInterface.interfaceAddresses.map { it.toPrefix() }.iterator())
                dnsServer = StringArray(linkProperties.dnsServers.mapNotNull { it.hostAddress }.iterator())
                type = when {
                    capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> Dropoandroid.InterfaceTypeWIFI
                    capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> Dropoandroid.InterfaceTypeCellular
                    capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> Dropoandroid.InterfaceTypeEthernet
                    else -> Dropoandroid.InterfaceTypeOther
                }
                metered = !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
                flags = javaInterface.toFlags(capabilities)
            }
        }
        return NetworkInterfaceArray(result.iterator())
    }

    override fun underNetworkExtension(): Boolean = false

    override fun includeAllNetworks(): Boolean = false

    override fun readWIFIState(): WIFIState? = null

    override fun systemCertificates(): StringIterator = StringArray(loadSystemCertificates().iterator())

    override fun clearDNSCache() {
    }

    override fun sendNotification(notification: BoxNotification) {
        val text = notification.body.ifBlank {
            notification.subtitle.ifBlank {
                notification.title.ifBlank { "VPN работает" }
            }
        }
        startForegroundCompat(buildNotification(userNotificationText(text)))
    }

    override fun localDNSTransport(): LocalDNSTransport? = null

    override fun serviceStop() {
        commandServer = null
        starting = false
        closeTun()
        closeDefaultInterfaceMonitor(interfaceUpdateListener)
        verboseSingBoxLogs = false
        coreLog("sing-box requested service stop")
        Dropoandroid.setConnected(false)
        DropoVpnRuntime.setStopped("VPN остановлен")
        syncCoreServiceState("stopped", "VPN остановлен")
        stopForegroundCompat()
        stopSelf()
    }

    override fun serviceReload() {
        val configResult = JSONObject(Dropoandroid.buildSingBoxConfig())
        if (!configResult.optBoolean("success")) {
            error(configResult.optString("error", "Failed to reload Android sing-box config"))
        }
        if (configResult.optBoolean("cached")) {
            coreLog("reloading cached sing-box config: ${configResult.optString("warning", "subscription refresh failed")}")
        }
        verboseSingBoxLogs = androidSingBoxVerboseLogging()
        coreLog("reloading sing-box service")
        commandServer?.startOrReloadService(
            configResult.getString("config"),
            OverrideOptions().apply { autoRedirect = false },
        )
    }

    override fun getSystemProxyStatus(): SystemProxyStatus =
        SystemProxyStatus().apply {
            available = false
            enabled = false
        }

    override fun setSystemProxyEnabled(enabled: Boolean) {
    }

    override fun writeDebugMessage(message: String?) {
        val text = message.orEmpty().trim()
        if (text.isEmpty()) return
        if (isDebugBuild()) {
            Log.d("sing-box", text)
        }
        val important = isImportantSingBoxMessage(text)
        if (verboseSingBoxLogs) {
            runCatching {
                Dropoandroid.call("AndroidSingBoxLog", "[${JSONObject.quote(text.take(2000))}]")
            }
            val runtimeNow = SystemClock.elapsedRealtime()
            if (runtimeNow - lastRuntimeSingBoxLogAt >= 500) {
                lastRuntimeSingBoxLogAt = runtimeNow
                DropoVpnRuntime.appendLog("sing-box: ${text.take(240)}")
            }
        }
        if (!important) return

        val now = SystemClock.elapsedRealtime()
        if (now - lastCoreDebugLogAt < 1000) return
        lastCoreDebugLogAt = now
        coreLog("sing-box: ${text.take(240)}")
    }

    private fun isImportantSingBoxMessage(text: String): Boolean {
        val normalized = text.lowercase(Locale.ROOT)
        if ("noerror" in normalized) return false
        return Regex("\\b(error|warn|warning|fatal|panic|exception|failed)\\b")
            .containsMatchIn(normalized)
    }

    private fun androidSingBoxVerboseLogging(): Boolean {
        return runCatching {
            val config = JSONObject(Dropoandroid.call("GetAppConfig", "[]"))
            config.optBoolean("enableLogging", true)
        }.getOrDefault(true)
    }

    private fun coreLog(message: String) {
        DropoVpnRuntime.appendLog("android engine: $message")
        Dropoandroid.call("AndroidEngineLog", "[${JSONObject.quote(message)}]")
    }

    private fun coreError(message: String) {
        DropoVpnRuntime.setFailed(message)
        DropoVpnRuntime.appendLog("android engine error: $message")
        Dropoandroid.call("AndroidEngineError", "[${JSONObject.quote(message)}]")
    }

    private fun syncCoreServiceState(
        state: String,
        message: String,
        error: String = "",
    ) {
        Dropoandroid.call(
            "AndroidServiceState",
            "[${JSONObject.quote(state)},${JSONObject.quote(message)},${JSONObject.quote(error)}]",
        )
    }

    private fun describeError(error: Throwable): String {
        val message = error.message ?: error.javaClass.simpleName
        return "${error.javaClass.simpleName}: $message"
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

    private fun notifyDefaultInterfaceAsync() {
        runCatching {
            executor.execute { notifyDefaultInterface() }
        }.onFailure {
            Log.w(TAG, "default interface update skipped", it)
        }
    }

    private fun notifyDefaultInterface() {
        val listener = interfaceUpdateListener ?: return
        val network = connectivity.activeNetwork ?: return
        val linkProperties = connectivity.getLinkProperties(network) ?: return
        val capabilities = connectivity.getNetworkCapabilities(network) ?: return
        val name = linkProperties.interfaceName ?: return
        val javaInterface = JavaNetworkInterface.getByName(name) ?: return
        listener.updateDefaultInterface(
            name,
            javaInterface.index,
            !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED),
            !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_CONGESTED),
        )
    }

    private fun startForegroundCompat(notification: Notification) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun stopForegroundCompat() {
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    private fun buildNotification(text: String): Notification {
        val safeText = userNotificationText(text)
        val openIntent = packageManager.getLaunchIntentForPackage(packageName)
            ?: Intent(this, MainActivity::class.java)
        val openPendingIntent = PendingIntent.getActivity(
            this,
            0,
            openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val stopPendingIntent = PendingIntent.getService(
            this,
            1,
            Intent(this, DropoVpnService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )

        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle("dropo VPN")
            .setContentText(safeText)
            .setSmallIcon(R.mipmap.ic_launcher)
            .setOngoing(true)
            .setContentIntent(openPendingIntent)
            .addAction(android.R.drawable.ic_menu_close_clear_cancel, "Отключить", stopPendingIntent)
            .build()
    }

    private fun userNotificationText(text: String): String {
        val normalized = text.trim()
        if (normalized.isEmpty()) return "VPN работает"
        if (normalized.contains("sing-box", ignoreCase = true)) return "VPN работает"
        if (normalized.contains("active", ignoreCase = true)) return "VPN работает"
        if (normalized.contains("connected", ignoreCase = true)) return "VPN работает"
        if (normalized.contains("starting", ignoreCase = true)) return "VPN запускается"
        if (normalized.contains("stopping", ignoreCase = true)) return "VPN останавливается"
        if (normalized.contains("stopped", ignoreCase = true)) return "VPN остановлен"
        return normalized
    }

    private fun createNotificationChannel() {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "dropo VPN",
                NotificationManager.IMPORTANCE_LOW,
            ),
        )
    }

    private fun InterfaceAddress.toPrefix(): String {
        val host = if (address is Inet6Address) {
            Inet6Address.getByAddress(address.address).hostAddress?.substringBefore("%").orEmpty()
        } else {
            address.hostAddress.orEmpty()
        }
        return "$host/$networkPrefixLength"
    }

    private fun JavaNetworkInterface.toFlags(capabilities: NetworkCapabilities): Int {
        var value = 0
        if (capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) {
            value = value or OsConstants.IFF_UP or OsConstants.IFF_RUNNING
        }
        if (runCatching { isLoopback }.getOrDefault(false)) value = value or OsConstants.IFF_LOOPBACK
        if (runCatching { isPointToPoint }.getOrDefault(false)) value = value or OsConstants.IFF_POINTOPOINT
        if (runCatching { supportsMulticast() }.getOrDefault(false)) value = value or OsConstants.IFF_MULTICAST
        return value
    }

    @RequiresApi(Build.VERSION_CODES.TIRAMISU)
    private fun RoutePrefix.toIpPrefix(): IpPrefix {
        return IpPrefix(InetAddress.getByName(address()), prefix())
    }

    private fun StringIterator.toList(): List<String> {
        val result = mutableListOf<String>()
        while (hasNext()) {
            result += next()
        }
        return result
    }

    private class StringArray(iterator: Iterator<String>) : StringIterator {
        private val values = iterator.asSequence().toList()
        private var index = 0

        override fun len(): Int = values.size - index
        override fun hasNext(): Boolean = index < values.size
        override fun next(): String = values[index++]
    }

    private class NetworkInterfaceArray(
        private val iterator: Iterator<BoxNetworkInterface>,
    ) : NetworkInterfaceIterator {
        override fun hasNext(): Boolean = iterator.hasNext()
        override fun next(): BoxNetworkInterface = iterator.next()
    }

    companion object {
        private const val TAG = "DropoVpnService"
        private const val CHANNEL_ID = "dropo_vpn"
        private const val NOTIFICATION_ID = 5001
        private const val ACTION_START = "in.droponevedimka.dropo.START_VPN"
        private const val ACTION_STOP = "in.droponevedimka.dropo.STOP_VPN"

        private val libboxSetup = AtomicBoolean(false)
        private val certificateCache = mutableListOf<String>()

        fun start(context: Context) {
            val intent = Intent(context, DropoVpnService::class.java).setAction(ACTION_START)
            context.startForegroundService(intent)
        }

        fun stop(context: Context) {
            val intent = Intent(context, DropoVpnService::class.java).setAction(ACTION_STOP)
            context.startService(intent)
        }

        private fun ensureLibboxSetup(context: Context) {
            if (libboxSetup.get()) return
            synchronized(libboxSetup) {
                if (libboxSetup.get()) return
                val workingDir = java.io.File(context.noBackupFilesDir, "libbox").apply {
                    if (!exists() && !mkdirs()) {
                        error("Failed to create private libbox working directory")
                    }
                }
                Dropoandroid.setup(
                    BoxSetupOptions().apply {
                        basePath = context.filesDir.absolutePath
                        workingPath = workingDir.absolutePath
                        tempPath = context.cacheDir.absolutePath
                        logMaxLines = 2400
                        debug = context.isDebugBuild()
                    },
                )
                libboxSetup.set(true)
            }
        }

        private fun loadSystemCertificates(): List<String> {
            synchronized(certificateCache) {
                if (certificateCache.isNotEmpty()) return certificateCache.toList()
                runCatching {
                    val keyStore = KeyStore.getInstance("AndroidCAStore")
                    keyStore.load(null)
                    val aliases = keyStore.aliases()
                    while (aliases.hasMoreElements()) {
                        val certificate = keyStore.getCertificate(aliases.nextElement()) as? X509Certificate
                        if (certificate != null) {
                            certificateCache += certificate.toPem()
                        }
                    }
                }.onFailure {
                    Log.w(TAG, "system certificate load failed", it)
                }
                return certificateCache.toList()
            }
        }

        private fun X509Certificate.toPem(): String {
            val encoded = Base64.encodeToString(encoded, Base64.NO_WRAP)
                .chunked(64)
                .joinToString("\n")
            return "-----BEGIN CERTIFICATE-----\n$encoded\n-----END CERTIFICATE-----\n"
        }
    }
}
