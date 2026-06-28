import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' show AppExitResponse;

import 'package:flutter/material.dart';
import 'package:flutter/services.dart';

const String _coreEndpoint = String.fromEnvironment(
  'DROPO_CORE_ENDPOINT',
  defaultValue: 'http://127.0.0.1:17890',
);

ButtonStyle _withClickCursor(ButtonStyle style) {
  return style.copyWith(
    mouseCursor: WidgetStateProperty.resolveWith<MouseCursor?>((states) {
      if (states.contains(WidgetState.disabled)) {
        return SystemMouseCursors.basic;
      }
      return SystemMouseCursors.click;
    }),
  );
}

void main() {
  runApp(const DropoApp());
}

class DropoApp extends StatelessWidget {
  const DropoApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'dropo',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(
        brightness: Brightness.dark,
        fontFamily: 'Segoe UI',
        colorScheme: ColorScheme.fromSeed(
          seedColor: const Color(0xFF36D399),
          brightness: Brightness.dark,
        ),
        scaffoldBackgroundColor: const Color(0xFF101617),
        useMaterial3: true,
      ),
      home: DropoHomePage(bridge: CoreBridge(_coreEndpoint)),
    );
  }
}

class CoreBridge {
  CoreBridge(String endpoint) : baseUri = _normalizeEndpoint(endpoint);

  final Uri baseUri;
  Process? _managedProcess;

  Future<void> ensureStarted() async {
    if (!await _isReachable()) {
      if (!Platform.isWindows) {
        return;
      }

      final exeDir = File(Platform.resolvedExecutable).parent;
      final coreExe = await _findCoreExecutable(exeDir);
      if (!await coreExe.exists()) {
        return;
      }

      final listen =
          '${baseUri.host}:${baseUri.hasPort ? baseUri.port : 17890}';
      _managedProcess = await Process.start(coreExe.path, [
        '--listen',
        listen,
      ], mode: ProcessStartMode.detachedWithStdio);
      unawaited(_managedProcess!.stdout.drain<void>());
      unawaited(_managedProcess!.stderr.drain<void>());

      for (var i = 0; i < 80; i++) {
        if (await _isReachable()) {
          break;
        }
        await Future<void>.delayed(const Duration(milliseconds: 150));
      }
    }

    if (await _isReachable()) {
      unawaited(
        _postMap(
          '/api/tray/ensure',
          timeout: const Duration(seconds: 3),
        ).catchError((_) => <String, dynamic>{}),
      );
      return;
    }
    throw const HttpException('dropo-core не ответил на локальном bridge');
  }

  Future<void> dispose() async {
    try {
      await finalizeQuit();
    } catch (_) {
      _managedProcess?.kill();
    }
    _managedProcess = null;
  }

  Future<File> _findCoreExecutable(Directory exeDir) async {
    final candidates = <File>[
      File('${exeDir.path}${Platform.pathSeparator}dropo-core.exe'),
      File(
        '${exeDir.path}${Platform.pathSeparator}resources'
        '${Platform.pathSeparator}dropo-core.exe',
      ),
      File(
        '${exeDir.path}${Platform.pathSeparator}resources'
        '${Platform.pathSeparator}app${Platform.pathSeparator}dropo-core.exe',
      ),
    ];
    for (final candidate in candidates) {
      if (await candidate.exists()) {
        return candidate;
      }
    }
    return candidates.first;
  }

  Future<CoreStatus> status() async {
    return CoreStatus.fromJson(
      await _getMap('/api/status', timeout: const Duration(seconds: 8)),
    );
  }

  Future<SubscriptionInfo> subscription() async {
    return SubscriptionInfo.fromJson(await callMap('GetCurrentSubscription'));
  }

  Future<Map<String, dynamic>> appConfig() {
    return callMap('GetAppConfig');
  }

  Future<Map<String, dynamic>> saveAppConfig(AppConfig config) {
    return callMap(
      'SaveAppConfig',
      args: [
        config.autoStart,
        config.enableLogging,
        config.checkUpdates,
        config.notifications,
        config.autoUpdateSub,
        config.theme,
        config.language,
        config.logLevel,
        config.subUpdateInterval,
      ],
    );
  }

  Future<Map<String, dynamic>> routingMode() {
    return callMap('GetRoutingMode');
  }

  Future<Map<String, dynamic>> setRoutingMode(String mode) {
    return callMap('SetRoutingMode', args: [mode]);
  }

  Future<Map<String, dynamic>> networkMode() {
    return callMap('GetNetworkMode');
  }

  Future<Map<String, dynamic>> setNetworkMode(String mode) {
    return callMap('SetNetworkMode', args: [mode]);
  }

  Future<Map<String, dynamic>> hideRuTraffic() {
    return callMap('GetHideRuTraffic');
  }

  Future<Map<String, dynamic>> setHideRuTraffic(
    bool enabled,
    String proxyAddress,
  ) {
    return callMap('SetHideRuTraffic', args: [enabled, proxyAddress]);
  }

  Future<Map<String, dynamic>> freeAccessConfig() {
    return callMap('GetFreeAccessConfig');
  }

  Future<Map<String, dynamic>> setDisableFreeAccess(bool disabled) {
    return callMap('SetDisableFreeAccess', args: [disabled]);
  }

  Future<List<RouteService>> routes() async {
    final data = await callMap('GetFreeAccessConfig');
    final raw = data['services'];
    if (raw is! List) {
      return fallbackRoutes;
    }
    return raw
        .whereType<Map<String, dynamic>>()
        .map(RouteService.fromFreeAccessJson)
        .toList(growable: false);
  }

  Future<List<String>> logs() async {
    final data = await _getMap('/api/logs', query: {'lastN': '260'});
    final raw = data['logs'];
    if (raw is! List) {
      return const [];
    }
    return raw.map((item) => item.toString()).toList(growable: false);
  }

  Future<List<BridgeEvent>> events({required int since}) async {
    final data = await _getMap('/api/events', query: {'since': '$since'});
    final raw = data['events'];
    if (raw is! List) {
      return const [];
    }
    return raw
        .whereType<Map<String, dynamic>>()
        .map(BridgeEvent.fromJson)
        .toList(growable: false);
  }

  Future<UpdateInfo> checkUpdates() async {
    return UpdateInfo.fromJson(await callMap('CheckForUpdates'));
  }

  Future<TrafficStatsInfo> trafficStats() async {
    return TrafficStatsInfo.fromJson(await callMap('GetTrafficStats'));
  }

  Future<Map<String, dynamic>> resetTrafficStats() {
    return callMap('ResetTrafficStats');
  }

  Future<List<WireGuardInfo>> wireGuards() async {
    final data = await callMap('GetWireGuardList');
    final raw = data['configs'];
    if (raw is! List) {
      return const [];
    }
    return raw
        .whereType<Map<String, dynamic>>()
        .map(WireGuardInfo.fromJson)
        .toList(growable: false);
  }

  Future<WireGuardInfo?> wireGuardConfig(String tag) async {
    final data = await callMap('GetWireGuardConfig', args: [tag]);
    if (data['success'] == false) {
      return null;
    }
    return WireGuardInfo.fromJson(data);
  }

  Future<Map<String, dynamic>> parseWireGuard(String config) {
    return callMap('ParseWireGuardConfigAPI', args: [config]);
  }

  Future<Map<String, dynamic>> addWireGuard(
    String tag,
    String name,
    String config,
  ) {
    return callMap('AddWireGuard', args: [tag, name, config]);
  }

  Future<Map<String, dynamic>> updateWireGuard(
    String oldTag,
    String tag,
    String name,
    String config,
  ) {
    return callMap('UpdateWireGuard', args: [oldTag, tag, name, config]);
  }

  Future<Map<String, dynamic>> deleteWireGuard(String tag) {
    return callMap('DeleteWireGuard', args: [tag]);
  }

  Future<Map<String, dynamic>> testSubscription(String value) {
    return callMap(
      'TestVPNConnection',
      args: [value.trim()],
      timeout: const Duration(minutes: 2),
    );
  }

  Future<Map<String, dynamic>> runQuickCheck() {
    return callMap(
      'RunClientQuickCheck',
      args: [false],
      timeout: const Duration(minutes: 2),
    );
  }

  Future<Map<String, dynamic>> captureFingerprint() {
    return callMap(
      'CaptureDPIFingerprint',
      timeout: const Duration(minutes: 4),
    );
  }

  Future<void> openFingerprintFolder() async {
    await callMap('OpenFingerprintFolder');
  }

  Future<void> openConfigFolder() async {
    await callMap('OpenConfigFolder');
  }

  Future<Map<String, dynamic>> setConnected(bool value) async {
    return _postMap(
      value ? '/api/connect' : '/api/disconnect',
      timeout: value
          ? const Duration(seconds: 90)
          : const Duration(seconds: 45),
    );
  }

  Future<Map<String, dynamic>> downloadDependencies() async {
    return _postMap(
      '/api/dependencies/download',
      timeout: const Duration(minutes: 8),
    );
  }

  Future<Map<String, dynamic>> saveSubscription(String value) {
    final trimmed = value.trim();
    if (trimmed.isEmpty) {
      return callMap(
        'RemoveVPNSubscription',
        timeout: const Duration(minutes: 2),
      );
    }
    return callMap(
      'SetVPNSubscription',
      args: [trimmed],
      timeout: const Duration(minutes: 3),
    );
  }

  Future<TelegramExitInfo> prepareQuit() async {
    return TelegramExitInfo.fromJson(
      await _postMap('/api/quit', timeout: const Duration(seconds: 15)),
    );
  }

  Future<void> finalizeQuit() async {
    await _postMap('/api/quit/finalize', timeout: const Duration(seconds: 3));
  }

  Future<void> openLogsFolder() async {
    await callMap('OpenLogs');
  }

  Future<void> openExternal(String link) async {
    await callMap('OpenExternalLink', args: [link]);
  }

  Future<Map<String, dynamic>> callMap(
    String method, {
    List<Object?> args = const [],
    Duration timeout = const Duration(seconds: 12),
  }) async {
    return _postMap(
      '/api/call',
      body: {'method': method, 'args': args},
      timeout: timeout,
    );
  }

  Future<bool> _isReachable() async {
    try {
      await _getMap('/api/info');
      return true;
    } catch (_) {
      return false;
    }
  }

  Future<Map<String, dynamic>> _getMap(
    String path, {
    Map<String, String> query = const {},
    Duration timeout = const Duration(seconds: 4),
  }) async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 1);
    try {
      final uri = baseUri.replace(
        path: path,
        queryParameters: query.isEmpty ? null : query,
      );
      final request = await client.getUrl(uri);
      final response = await request.close().timeout(timeout);
      return _decodeResponse(response);
    } finally {
      client.close(force: true);
    }
  }

  Future<Map<String, dynamic>> _postMap(
    String path, {
    Map<String, dynamic> body = const {},
    Duration timeout = const Duration(seconds: 12),
  }) async {
    final client = HttpClient()..connectionTimeout = const Duration(seconds: 2);
    try {
      final request = await client.postUrl(baseUri.replace(path: path));
      request.headers.contentType = ContentType.json;
      request.write(jsonEncode(body));
      final response = await request.close().timeout(timeout);
      return _decodeResponse(response);
    } finally {
      client.close(force: true);
    }
  }

  Future<Map<String, dynamic>> _decodeResponse(
    HttpClientResponse response,
  ) async {
    final text = await response.transform(utf8.decoder).join();
    final decoded = text.isEmpty ? <String, dynamic>{} : jsonDecode(text);
    final data = decoded is Map<String, dynamic>
        ? decoded
        : <String, dynamic>{'data': decoded};
    if (response.statusCode < 200 || response.statusCode >= 300) {
      throw HttpException(
        data['error']?.toString() ?? 'HTTP ${response.statusCode}',
      );
    }
    return data;
  }

  static Uri _normalizeEndpoint(String endpoint) {
    final uri = Uri.parse(endpoint);
    if (uri.scheme == 'ws') {
      return uri.replace(scheme: 'http');
    }
    if (uri.scheme == 'wss') {
      return uri.replace(scheme: 'https');
    }
    return uri;
  }
}

class BridgeEvent {
  const BridgeEvent({
    required this.id,
    required this.name,
    required this.payload,
  });

  final int id;
  final String name;
  final Map<String, dynamic> payload;

  factory BridgeEvent.fromJson(Map<String, dynamic> json) {
    return BridgeEvent(
      id: _asInt(json['id']),
      name: json['name']?.toString() ?? '',
      payload: _asMap(json['payload']),
    );
  }
}

class CoreStatus {
  const CoreStatus({
    required this.connected,
    required this.connecting,
    required this.hasError,
    required this.hasConfig,
    required this.singboxExists,
    required this.networkMode,
    required this.networkLabel,
    required this.networkDescription,
    required this.dependencies,
    required this.version,
  });

  final bool connected;
  final bool connecting;
  final bool hasError;
  final bool hasConfig;
  final bool singboxExists;
  final String networkMode;
  final String networkLabel;
  final String networkDescription;
  final DepsStatus dependencies;
  final VersionInfo version;

  factory CoreStatus.fromJson(Map<String, dynamic> json) {
    return CoreStatus(
      connected: json['connected'] == true || json['running'] == true,
      connecting: json['connecting'] == true,
      hasError: json['hasError'] == true,
      hasConfig: json['configExists'] == true,
      singboxExists: json['singboxExists'] == true,
      networkMode: json['networkMode']?.toString() ?? 'auto',
      networkLabel: json['networkModeLabel']?.toString() ?? 'Auto',
      networkDescription: json['networkModeDescription']?.toString() ?? '',
      dependencies: DepsStatus.fromJson(_asMap(json['dependencies'])),
      version: VersionInfo.fromJson(_asMap(json['version'])),
    );
  }
}

class DepsStatus {
  const DepsStatus({
    required this.managed,
    required this.ready,
    required this.required,
    required this.installed,
    required this.sizeMb,
  });

  final bool managed;
  final bool ready;
  final String required;
  final String installed;
  final int sizeMb;

  factory DepsStatus.fromJson(Map<String, dynamic> json) {
    return DepsStatus(
      managed: json['managed'] == true,
      ready: json['ready'] == true,
      required: json['required']?.toString() ?? '',
      installed: json['installed']?.toString() ?? '',
      sizeMb: _asInt(json['sizeMB']),
    );
  }
}

class VersionInfo {
  const VersionInfo({
    required this.version,
    required this.fullVersion,
    required this.singboxVersion,
  });

  final String version;
  final String fullVersion;
  final String singboxVersion;

  factory VersionInfo.fromJson(Map<String, dynamic> json) {
    return VersionInfo(
      version: json['version']?.toString() ?? 'dev',
      fullVersion:
          json['fullVersion']?.toString() ??
          json['version']?.toString() ??
          'dev',
      singboxVersion: json['singboxVersion']?.toString() ?? '',
    );
  }
}

class SubscriptionInfo {
  const SubscriptionInfo({
    required this.hasSubscription,
    required this.url,
    required this.proxyCount,
  });

  final bool hasSubscription;
  final String url;
  final int proxyCount;

  factory SubscriptionInfo.fromJson(Map<String, dynamic> json) {
    return SubscriptionInfo(
      hasSubscription: json['hasSubscription'] == true,
      url: json['url']?.toString() ?? '',
      proxyCount: _asInt(json['proxyCount']),
    );
  }
}

class AppConfig {
  const AppConfig({
    required this.autoStart,
    required this.enableLogging,
    required this.checkUpdates,
    required this.notifications,
    required this.autoUpdateSub,
    required this.theme,
    required this.language,
    required this.logLevel,
    required this.subUpdateInterval,
    required this.hideRuTraffic,
    required this.ruProxyAddress,
    required this.disableFreeAccess,
    required this.routingMode,
    required this.networkMode,
    required this.githubRepo,
    required this.githubUrl,
    required this.telegramName,
    required this.telegramUrl,
  });

  final bool autoStart;
  final bool enableLogging;
  final bool checkUpdates;
  final bool notifications;
  final bool autoUpdateSub;
  final String theme;
  final String language;
  final String logLevel;
  final int subUpdateInterval;
  final bool hideRuTraffic;
  final String ruProxyAddress;
  final bool disableFreeAccess;
  final String routingMode;
  final String networkMode;
  final String githubRepo;
  final String githubUrl;
  final String telegramName;
  final String telegramUrl;

  static const defaults = AppConfig(
    autoStart: false,
    enableLogging: true,
    checkUpdates: true,
    notifications: true,
    autoUpdateSub: true,
    theme: 'dark',
    language: 'ru',
    logLevel: 'trace',
    subUpdateInterval: 24,
    hideRuTraffic: false,
    ruProxyAddress: '',
    disableFreeAccess: false,
    routingMode: 'blocked_only',
    networkMode: 'auto',
    githubRepo: 'Droponevedimka/dropo',
    githubUrl: 'https://github.com/Droponevedimka/dropo',
    telegramName: 't.me/droponevedimka555',
    telegramUrl: 'https://t.me/droponevedimka555',
  );

  factory AppConfig.fromJson(Map<String, dynamic> json) {
    final networkStatus = _asMap(json['networkModeStatus']);
    return AppConfig(
      autoStart: json['autoStart'] == true,
      enableLogging: json['enableLogging'] != false,
      checkUpdates: json['checkUpdates'] != false,
      notifications: json['notifications'] != false,
      autoUpdateSub: json['autoUpdateSub'] != false,
      theme: json['theme']?.toString() ?? 'dark',
      language: json['language']?.toString() ?? 'ru',
      logLevel: json['logLevel']?.toString() ?? 'trace',
      subUpdateInterval: _asInt(json['subUpdateInterval']) == 0
          ? 24
          : _asInt(json['subUpdateInterval']),
      hideRuTraffic: json['hideRuTraffic'] == true,
      ruProxyAddress: json['ruProxyAddress']?.toString() ?? '',
      disableFreeAccess: json['disableFreeAccess'] == true,
      routingMode: json['routingMode']?.toString() ?? 'blocked_only',
      networkMode:
          json['networkMode']?.toString() ??
          networkStatus['requested']?.toString() ??
          'auto',
      githubRepo: json['githubRepo']?.toString() ?? defaults.githubRepo,
      githubUrl: json['githubURL']?.toString() ?? defaults.githubUrl,
      telegramName: json['telegramName']?.toString() ?? defaults.telegramName,
      telegramUrl: json['telegramURL']?.toString() ?? defaults.telegramUrl,
    );
  }

  AppConfig copyWith({
    bool? autoStart,
    bool? enableLogging,
    bool? checkUpdates,
    bool? notifications,
    bool? autoUpdateSub,
    String? theme,
    String? language,
    String? logLevel,
    int? subUpdateInterval,
    bool? hideRuTraffic,
    String? ruProxyAddress,
    bool? disableFreeAccess,
    String? routingMode,
    String? networkMode,
    String? githubRepo,
    String? githubUrl,
    String? telegramName,
    String? telegramUrl,
  }) {
    return AppConfig(
      autoStart: autoStart ?? this.autoStart,
      enableLogging: enableLogging ?? this.enableLogging,
      checkUpdates: checkUpdates ?? this.checkUpdates,
      notifications: notifications ?? this.notifications,
      autoUpdateSub: autoUpdateSub ?? this.autoUpdateSub,
      theme: theme ?? this.theme,
      language: language ?? this.language,
      logLevel: logLevel ?? this.logLevel,
      subUpdateInterval: subUpdateInterval ?? this.subUpdateInterval,
      hideRuTraffic: hideRuTraffic ?? this.hideRuTraffic,
      ruProxyAddress: ruProxyAddress ?? this.ruProxyAddress,
      disableFreeAccess: disableFreeAccess ?? this.disableFreeAccess,
      routingMode: routingMode ?? this.routingMode,
      networkMode: networkMode ?? this.networkMode,
      githubRepo: githubRepo ?? this.githubRepo,
      githubUrl: githubUrl ?? this.githubUrl,
      telegramName: telegramName ?? this.telegramName,
      telegramUrl: telegramUrl ?? this.telegramUrl,
    );
  }
}

class RouteService {
  const RouteService({
    required this.tag,
    required this.name,
    required this.method,
    required this.requiresVpn,
  });

  final String tag;
  final String name;
  final String method;
  final bool requiresVpn;

  factory RouteService.fromFreeAccessJson(Map<String, dynamic> json) {
    return RouteService(
      tag: json['tag']?.toString() ?? '',
      name: json['name']?.toString() ?? 'Service',
      method:
          json['effectiveMethodLabel']?.toString() ??
          json['methodLabel']?.toString() ??
          json['selectedMethod']?.toString() ??
          'Auto',
      requiresVpn: json['requiresVpn'] == true,
    );
  }
}

class TrafficDataInfo {
  const TrafficDataInfo({
    required this.uploaded,
    required this.downloaded,
    required this.duration,
    required this.uploadedStr,
    required this.downloadedStr,
    required this.durationStr,
  });

  final int uploaded;
  final int downloaded;
  final int duration;
  final String uploadedStr;
  final String downloadedStr;
  final String durationStr;

  factory TrafficDataInfo.fromJson(Map<String, dynamic> json) {
    return TrafficDataInfo(
      uploaded: _asInt(json['uploaded']),
      downloaded: _asInt(json['downloaded']),
      duration: _asInt(json['duration']),
      uploadedStr: json['uploadedStr']?.toString() ?? '0 B',
      downloadedStr: json['downloadedStr']?.toString() ?? '0 B',
      durationStr: json['durationStr']?.toString() ?? '0 сек',
    );
  }
}

class TrafficStatsInfo {
  const TrafficStatsInfo({
    required this.success,
    required this.current,
    required this.last,
    required this.total,
    required this.sessions,
  });

  final bool success;
  final TrafficDataInfo current;
  final TrafficDataInfo last;
  final TrafficDataInfo total;
  final int sessions;

  static final empty = TrafficStatsInfo.fromJson(const {});

  factory TrafficStatsInfo.fromJson(Map<String, dynamic> json) {
    final total = _asMap(json['total']);
    return TrafficStatsInfo(
      success: json['success'] != false,
      current: TrafficDataInfo.fromJson(_asMap(json['current'])),
      last: TrafficDataInfo.fromJson(_asMap(json['last'])),
      total: TrafficDataInfo.fromJson(total),
      sessions: _asInt(total['sessions']),
    );
  }
}

class WireGuardInfo {
  const WireGuardInfo({
    required this.tag,
    required this.name,
    required this.endpoint,
    required this.allowedIps,
    required this.config,
    required this.privateKey,
    required this.localAddress,
    required this.dns,
    required this.mtu,
    required this.publicKey,
    required this.presharedKey,
    required this.persistentKeepalive,
  });

  final String tag;
  final String name;
  final String endpoint;
  final List<String> allowedIps;
  final String config;
  final String privateKey;
  final String localAddress;
  final List<String> dns;
  final int mtu;
  final String publicKey;
  final String presharedKey;
  final int persistentKeepalive;

  factory WireGuardInfo.fromJson(Map<String, dynamic> json) {
    final info = WireGuardInfo(
      tag: json['tag']?.toString() ?? '',
      name: json['name']?.toString() ?? json['tag']?.toString() ?? '',
      endpoint: json['endpoint']?.toString() ?? '',
      allowedIps: _asStringList(json['allowed_ips'] ?? json['allowedIPs']),
      config:
          json['config']?.toString() ?? json['configText']?.toString() ?? '',
      privateKey: json['private_key']?.toString() ?? '',
      localAddress: json['local_address']?.toString() ?? '',
      dns: _asStringList(json['dns']),
      mtu: _asInt(json['mtu']),
      publicKey: json['public_key']?.toString() ?? '',
      presharedKey: json['preshared_key']?.toString() ?? '',
      persistentKeepalive: _asInt(json['persistent_keepalive']),
    );
    if (info.config.isNotEmpty || info.privateKey.isEmpty) {
      return info;
    }
    return WireGuardInfo(
      tag: info.tag,
      name: info.name,
      endpoint: info.endpoint,
      allowedIps: info.allowedIps,
      config: info.toConfigText(),
      privateKey: info.privateKey,
      localAddress: info.localAddress,
      dns: info.dns,
      mtu: info.mtu,
      publicKey: info.publicKey,
      presharedKey: info.presharedKey,
      persistentKeepalive: info.persistentKeepalive,
    );
  }

  String toConfigText() {
    final lines = <String>[
      '[Interface]',
      if (privateKey.isNotEmpty) 'PrivateKey = $privateKey',
      if (localAddress.isNotEmpty) 'Address = $localAddress',
      if (dns.isNotEmpty) 'DNS = ${dns.join(', ')}',
      if (mtu > 0) 'MTU = $mtu',
      '',
      '[Peer]',
      if (publicKey.isNotEmpty) 'PublicKey = $publicKey',
      if (presharedKey.isNotEmpty) 'PresharedKey = $presharedKey',
      if (allowedIps.isNotEmpty) 'AllowedIPs = ${allowedIps.join(', ')}',
      if (endpoint.isNotEmpty) 'Endpoint = $endpoint',
      if (persistentKeepalive > 0) 'PersistentKeepalive = $persistentKeepalive',
    ];
    return lines.join('\n');
  }
}

class UpdateInfo {
  const UpdateInfo({
    required this.success,
    required this.hasUpdate,
    required this.currentVersion,
    required this.latestVersion,
    required this.releaseUrl,
    required this.error,
  });

  final bool success;
  final bool hasUpdate;
  final String currentVersion;
  final String latestVersion;
  final String releaseUrl;
  final String error;

  factory UpdateInfo.fromJson(Map<String, dynamic> json) {
    return UpdateInfo(
      success: json['success'] != false,
      hasUpdate: json['hasUpdate'] == true,
      currentVersion: json['currentVersion']?.toString() ?? '',
      latestVersion: json['latestVersion']?.toString() ?? '',
      releaseUrl: json['releaseURL']?.toString() ?? '',
      error: json['error']?.toString() ?? '',
    );
  }
}

class TelegramExitInfo {
  const TelegramExitInfo({
    required this.showNotice,
    required this.injected,
    required this.recommendRemove,
  });

  final bool showNotice;
  final bool injected;
  final bool recommendRemove;

  factory TelegramExitInfo.fromJson(Map<String, dynamic> json) {
    return TelegramExitInfo(
      showNotice: json['showNotice'] == true,
      injected: json['injected'] == true,
      recommendRemove: json['recommendRemove'] == true,
    );
  }
}

class DropoHomePage extends StatefulWidget {
  const DropoHomePage({super.key, required this.bridge});

  final CoreBridge bridge;

  @override
  State<DropoHomePage> createState() => _DropoHomePageState();
}

class _DropoHomePageState extends State<DropoHomePage>
    with WidgetsBindingObserver {
  CoreStatus status = CoreStatus.fromJson(const {});
  SubscriptionInfo subscription = const SubscriptionInfo(
    hasSubscription: false,
    url: '',
    proxyCount: 0,
  );
  UpdateInfo? updateInfo;
  AppConfig appConfig = AppConfig.defaults;
  List<RouteService> routes = fallbackRoutes;
  List<WireGuardInfo> wireGuards = const [];
  List<String> logs = const [];
  bool booting = true;
  bool online = false;
  bool uiBusy = false;
  bool quitting = false;
  int lastEventId = 0;
  int refreshFailureCount = 0;
  String statusMessage = 'Запускаем dropo-core...';
  String connectionHint = 'Поднимаем локальный bridge и готовим компоненты.';
  String routeHint = '';
  String depsProgress = '';
  final Map<String, String> busyTasks = <String, String>{};
  final subscriptionController = TextEditingController();
  Timer? refreshTimer;
  Timer? eventsTimer;
  bool startupUpdateCheckScheduled = false;

  bool get connectionBusy {
    return busyTasks.containsKey('vpn-connect') ||
        busyTasks.containsKey('vpn-disconnect') ||
        status.connecting;
  }

  bool get controlsDisabled => booting || uiBusy || quitting || !online;

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    unawaited(_bootstrap());
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    refreshTimer?.cancel();
    eventsTimer?.cancel();
    subscriptionController.dispose();
    if (!quitting) {
      unawaited(widget.bridge.dispose());
    }
    super.dispose();
  }

  @override
  Future<AppExitResponse> didRequestAppExit() async {
    if (quitting) {
      return AppExitResponse.exit;
    }
    unawaited(_quitApp());
    return AppExitResponse.cancel;
  }

  Future<void> _bootstrap() async {
    setState(() {
      booting = true;
      statusMessage = 'Запускаем dropo-core...';
      connectionHint =
          'Окно откроется сразу, а ядро продолжит инициализацию в фоне.';
    });
    try {
      await widget.bridge.ensureStarted();
      setState(() {
        online = true;
        statusMessage = 'dropo-core запущен';
        connectionHint = 'Загружаем настройки и состояние VPN...';
      });

      eventsTimer = Timer.periodic(const Duration(milliseconds: 800), (_) {
        unawaited(_pollEvents());
      });
      refreshTimer = Timer.periodic(const Duration(seconds: 2), (_) {
        unawaited(_refresh());
      });

      await _refresh(all: true);
      await _ensureDependenciesReady();
      _scheduleStartupUpdateCheck();
      if (!mounted) {
        return;
      }
      setState(() {
        booting = false;
        connectionHint = status.connected
            ? 'VPN активен. Маршрутизация работает через ${status.networkLabel}.'
            : '';
      });
    } catch (error) {
      if (!mounted) {
        return;
      }
      setState(() {
        booting = false;
        online = false;
        statusMessage = 'Ядро не запустилось';
        connectionHint = error.toString();
      });
    }
  }

  Future<void> _refresh({bool all = false}) async {
    try {
      final loadedStatus = await widget.bridge.status();
      List<String> loadedLogs = logs;
      try {
        loadedLogs = await widget.bridge.logs();
      } catch (_) {
        // Status is the authoritative health signal; keep the last log snapshot
        // if the optional log request is slow during startup or shutdown.
      }
      SubscriptionInfo loadedSubscription = subscription;
      List<RouteService> loadedRoutes = routes;
      AppConfig loadedAppConfig = appConfig;
      List<WireGuardInfo> loadedWireGuards = wireGuards;
      if (all) {
        try {
          loadedSubscription = await widget.bridge.subscription();
          loadedRoutes = await widget.bridge.routes();
          loadedAppConfig = AppConfig.fromJson(await widget.bridge.appConfig());
          loadedWireGuards = await widget.bridge.wireGuards();
        } catch (error) {
          if (!booting) {
            routeHint = 'Настройки ещё загружаются: ${_cleanError(error)}';
          }
        }
      }
      if (!mounted) {
        return;
      }
      setState(() {
        refreshFailureCount = 0;
        online = true;
        status = loadedStatus;
        logs = loadedLogs;
        subscription = loadedSubscription;
        routes = loadedRoutes.isEmpty ? fallbackRoutes : loadedRoutes;
        appConfig = loadedAppConfig;
        wireGuards = loadedWireGuards;
        if (all && !connectionBusy && !uiBusy) {
          routeHint = '';
        }
        if (all) {
          subscriptionController.text = loadedSubscription.url;
        }
        statusMessage = _statusLabel();
        if (!connectionBusy && !booting) {
          connectionHint = loadedStatus.connected
              ? 'VPN активен. Заблокированные сервисы идут через выбранный обход, остальное напрямую.'
              : '';
        }
      });
    } catch (error) {
      if (!mounted) {
        return;
      }
      final transient = booting || connectionBusy || uiBusy || quitting;
      final message = _cleanError(error);
      setState(() {
        refreshFailureCount += 1;
        if ((transient && online) || (online && refreshFailureCount < 3)) {
          connectionHint = transient
              ? 'Ждём ответ dropo-core: $message'
              : 'dropo-core отвечает медленно: $message';
          return;
        }
        online = false;
        statusMessage = 'Нет связи с dropo-core';
        connectionHint = message;
      });
    }
  }

  Future<void> _pollEvents() async {
    if (!online || quitting) {
      return;
    }
    try {
      final events = await widget.bridge.events(since: lastEventId);
      if (events.isEmpty || !mounted) {
        return;
      }
      setState(() {
        for (final event in events) {
          if (event.id > lastEventId) {
            lastEventId = event.id;
          }
          _applyEvent(event);
        }
      });
    } catch (_) {
      // Status polling will surface offline state; event polling stays quiet.
    }
  }

  Future<void> _ensureDependenciesReady() async {
    if (!mounted) {
      return;
    }
    if (!status.dependencies.managed || status.dependencies.ready) {
      return;
    }
    await _downloadDependencies();
  }

  void _scheduleStartupUpdateCheck() {
    if (startupUpdateCheckScheduled || !appConfig.checkUpdates) {
      return;
    }
    startupUpdateCheckScheduled = true;
    Future<void>.delayed(const Duration(seconds: 2), () async {
      if (!mounted || quitting) {
        return;
      }
      try {
        final result = await widget.bridge.checkUpdates();
        if (!mounted) {
          return;
        }
        if (result.success) {
          setState(() {
            updateInfo = result;
            if (result.hasUpdate && !connectionBusy && !uiBusy) {
              statusMessage = 'Доступна версия ${result.latestVersion}';
              connectionHint =
                  'Откройте About или настройки, чтобы перейти к GitHub Releases.';
            }
          });
        }
      } catch (_) {
        // Startup update checks are intentionally quiet.
      }
    });
  }

  void _applyEvent(BridgeEvent event) {
    switch (event.name) {
      case 'app-busy':
        final id = event.payload['id']?.toString() ?? 'core';
        final active = event.payload['active'] == true;
        final message = event.payload['message']?.toString() ?? '';
        if (active) {
          busyTasks[id] = message;
          if (id == 'vpn-connect' || id == 'vpn-disconnect') {
            connectionHint = message.isEmpty
                ? 'Выполняется операция...'
                : message;
            statusMessage = id == 'vpn-disconnect'
                ? 'Отключаем VPN'
                : 'Подбор работающих стратегий';
          }
        } else {
          busyTasks.remove(id);
        }
        break;
      case 'route-probe-start':
        routeHint = 'Подбираем рабочие методы обхода для сервисов...';
        connectionHint = routeHint;
        break;
      case 'route-probe-service':
        final name =
            event.payload['name']?.toString() ??
            event.payload['tag']?.toString() ??
            'сервис';
        final method =
            event.payload['methodLabel']?.toString() ??
            event.payload['methodTag']?.toString() ??
            'метод';
        final success = event.payload['success'] == true;
        routeHint = success
            ? '$name: выбран $method'
            : '$name: ищем следующий метод обхода';
        connectionHint = routeHint;
        break;
      case 'route-probe-candidate':
        final service =
            event.payload['service']?.toString() ??
            event.payload['serviceName']?.toString() ??
            event.payload['tag']?.toString() ??
            'сервис';
        final method =
            event.payload['methodLabel']?.toString() ??
            event.payload['label']?.toString() ??
            'метод';
        routeHint = 'Проверяем $service через $method...';
        connectionHint = routeHint;
        break;
      case 'deps-progress':
        final phase = event.payload['phase']?.toString() ?? 'Загрузка';
        final percent = _asInt(event.payload['percent']);
        depsProgress = percent > 0 ? '$phase $percent%' : phase;
        connectionHint = depsProgress;
        break;
    }
  }

  Future<void> _toggleConnection() async {
    await _runBusy(() async {
      final target = !status.connected;
      setState(() {
        busyTasks[target ? 'vpn-connect' : 'vpn-disconnect'] = target
            ? 'Подбираем рабочие стратегии обхода...'
            : 'Останавливаем VPN...';
        statusMessage = target
            ? 'Подбор работающих стратегий'
            : 'Отключаем VPN';
        connectionHint = target
            ? 'Проверяем конфиг, подбираем методы обхода и запускаем фоновые процессы.'
            : 'Останавливаем сетевые процессы и возвращаем системные настройки.';
      });
      Map<String, dynamic> result;
      try {
        result = await widget.bridge.setConnected(target);
      } on TimeoutException catch (error) {
        if (target) {
          await widget.bridge
              .setConnected(false)
              .catchError((_) => <String, dynamic>{});
        }
        if (mounted) {
          setState(() {
            statusMessage = 'Ошибка запуска VPN';
            connectionHint =
                'Запуск занял слишком много времени. Фоновые процессы остановлены, попробуйте снова. ${_cleanError(error)}';
          });
        }
        await _refresh(all: true);
        return;
      }
      final ok = result['success'] != false;
      if (!ok) {
        if (target) {
          await widget.bridge
              .setConnected(false)
              .catchError((_) => <String, dynamic>{});
        }
        setState(() {
          statusMessage = 'Ошибка';
          connectionHint =
              result['error']?.toString() ?? 'Операция не выполнена';
        });
      }
      await _refresh(all: true);
    }, clearConnectionBusy: true);
  }

  Future<void> _downloadDependencies() async {
    await _runBusy(() async {
      setState(() {
        statusMessage = 'Загружаем компоненты';
        connectionHint = 'Скачиваем и проверяем архив зависимостей...';
      });
      final result = await widget.bridge.downloadDependencies();
      setState(() {
        statusMessage = result['success'] == true
            ? 'Компоненты готовы'
            : 'Ошибка загрузки';
        connectionHint = result['success'] == true
            ? 'Зависимости скачаны, проверены и распакованы.'
            : result['error']?.toString() ?? 'Не удалось скачать компоненты';
        if (result['success'] == true) {
          depsProgress = '';
        }
      });
      await _refresh(all: true);
    });
  }

  Future<void> _checkUpdates() async {
    await _runBusy(() async {
      final result = await widget.bridge.checkUpdates();
      setState(() {
        updateInfo = result;
        statusMessage = result.success
            ? (result.hasUpdate
                  ? 'Доступна версия ${result.latestVersion}'
                  : 'Версия актуальна')
            : 'Ошибка обновления';
        connectionHint = result.success
            ? (result.hasUpdate
                  ? 'Откройте GitHub Releases и скачайте новую сборку.'
                  : 'Вы используете последнюю опубликованную версию.')
            : result.error;
      });
    });
  }

  Future<void> _runBusy(
    Future<void> Function() action, {
    bool clearConnectionBusy = false,
  }) async {
    if (uiBusy) {
      return;
    }
    setState(() => uiBusy = true);
    try {
      await action();
    } catch (error) {
      if (mounted) {
        setState(() {
          statusMessage = 'Ошибка';
          connectionHint = _cleanError(error);
        });
      }
    } finally {
      if (mounted) {
        setState(() {
          uiBusy = false;
          if (clearConnectionBusy) {
            busyTasks.remove('vpn-connect');
            busyTasks.remove('vpn-disconnect');
          }
        });
      }
    }
  }

  Future<void> _quitApp() async {
    if (quitting) {
      return;
    }
    setState(() {
      quitting = true;
      statusMessage = 'Закрываем dropo';
      connectionHint = 'Останавливаем VPN, WinDivert и фоновые процессы...';
    });
    try {
      final info = await widget.bridge.prepareQuit();
      if (!mounted) {
        return;
      }
      if (info.showNotice) {
        setState(() {
          connectionHint = 'Перед выходом проверьте proxy в Telegram.';
        });
        await _showTelegramExitNotice(info);
      } else {
        await _finalizeQuit();
      }
    } catch (error) {
      if (!mounted) {
        return;
      }
      setState(() {
        quitting = false;
        statusMessage = 'Не удалось закрыть приложение';
        connectionHint = _cleanError(error);
      });
    }
  }

  Future<void> _finalizeQuit() async {
    try {
      await widget.bridge.finalizeQuit();
    } catch (_) {
      // The core is expected to disappear here.
    }
    exit(0);
  }

  Future<void> _showTelegramExitNotice(TelegramExitInfo info) async {
    var remaining = 15;
    Timer? timer;
    await showDialog<void>(
      context: context,
      barrierDismissible: false,
      builder: (dialogContext) {
        return StatefulBuilder(
          builder: (context, setDialogState) {
            timer ??= Timer.periodic(const Duration(seconds: 1), (_) {
              if (!context.mounted) {
                return;
              }
              if (remaining <= 1) {
                timer?.cancel();
                Navigator.of(dialogContext).pop();
                unawaited(_finalizeQuit());
                return;
              }
              setDialogState(() => remaining -= 1);
            });
            return _AppDialog(
              width: 640,
              centered: true,
              heightFactor: 0.88,
              title: 'Проверьте proxy в Telegram',
              icon: Icons.telegram,
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  const Text(
                    'Если Telegram уже подключался к локальному proxy dropo, он может сохранить его после выхода. Удалите proxy в Telegram, если он больше не нужен.',
                    style: TextStyle(color: Color(0xFFD8E4E0), height: 1.35),
                  ),
                  const SizedBox(height: 14),
                  const Row(
                    children: [
                      Expanded(
                        child: _TelegramAssetShot(
                          title: '1. Тип соединения',
                          asset: 'assets/telegram-proxy-step1.png',
                        ),
                      ),
                      SizedBox(width: 12),
                      Expanded(
                        child: _TelegramAssetShot(
                          title: '2. Удалить proxy',
                          asset: 'assets/telegram-proxy-step2.png',
                        ),
                      ),
                    ],
                  ),
                  const SizedBox(height: 14),
                  ClipRRect(
                    borderRadius: BorderRadius.circular(999),
                    child: LinearProgressIndicator(
                      value: remaining / 15,
                      minHeight: 6,
                      backgroundColor: Colors.white.withValues(alpha: 0.08),
                      color: const Color(0xFF36D399),
                    ),
                  ),
                  const SizedBox(height: 7),
                  Text(
                    'Автоматическое закрытие через $remaining сек.',
                    textAlign: TextAlign.center,
                    style: const TextStyle(
                      color: Color(0xFF8A9B97),
                      fontSize: 12,
                    ),
                  ),
                  const SizedBox(height: 14),
                  Row(
                    children: [
                      Expanded(
                        child: _ActionButton(
                          label: 'Я проверю Telegram',
                          icon: Icons.open_in_new,
                          onPressed: () async {
                            timer?.cancel();
                            Navigator.of(dialogContext).pop();
                            await _finalizeQuit();
                          },
                        ),
                      ),
                      const SizedBox(width: 10),
                      Expanded(
                        child: _ActionButton(
                          label: 'Закрыть сейчас',
                          icon: Icons.logout,
                          danger: true,
                          onPressed: () async {
                            timer?.cancel();
                            Navigator.of(dialogContext).pop();
                            await _finalizeQuit();
                          },
                        ),
                      ),
                    ],
                  ),
                ],
              ),
            );
          },
        );
      },
    ).whenComplete(() => timer?.cancel());
  }

  String _statusLabel() {
    if (!online) {
      return 'dropo-core offline';
    }
    if (status.hasError) {
      return 'Требуется внимание';
    }
    if (busyTasks.containsKey('vpn-disconnect')) {
      return 'Отключаем VPN';
    }
    if (busyTasks.containsKey('vpn-connect')) {
      return 'Подбор работающих стратегий';
    }
    if (connectionBusy) {
      return status.connected ? 'VPN работает' : 'Подбор работающих стратегий';
    }
    if (status.connected) {
      return 'VPN активен';
    }
    return 'Отключено';
  }

  @override
  Widget build(BuildContext context) {
    final isBusy = connectionBusy || uiBusy || booting || quitting;
    final hintMessage = connectionHint.trim().isNotEmpty
        ? connectionHint
        : routeHint.trim().isNotEmpty
        ? routeHint
        : depsProgress;
    return Scaffold(
      body: Stack(
        children: [
          const Positioned.fill(child: _GradientBackdrop()),
          Positioned(
            left: 12,
            top: 12,
            child: SafeArea(child: _NetworkModePill(status: status)),
          ),
          SafeArea(
            child: Center(
              child: ConstrainedBox(
                constraints: const BoxConstraints(maxWidth: 320),
                child: Padding(
                  padding: const EdgeInsets.fromLTRB(12, 14, 12, 58),
                  child: Column(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      _LogoMark(
                        connected: status.connected,
                        connecting: isBusy,
                        error: status.hasError || (!online && !booting),
                      ),
                      const SizedBox(height: 12),
                      _Badges(
                        subscription: subscription,
                        wireGuardCount: wireGuards.length,
                        onSubscription: controlsDisabled
                            ? null
                            : _openSubscription,
                        onWireGuard: controlsDisabled ? null : _openWireGuard,
                      ),
                      const SizedBox(height: 12),
                      _PowerButton(
                        connected: status.connected,
                        busy: isBusy,
                        enabled: !controlsDisabled && !connectionBusy,
                        onPressed: _toggleConnection,
                      ),
                      const SizedBox(height: 12),
                      _ConnectionStatus(
                        connected: status.connected,
                        connecting: isBusy,
                        online: online,
                        hasError: status.hasError,
                        text: statusMessage,
                      ),
                      const SizedBox(height: 8),
                      _ConnectionHint(
                        visible: hintMessage.trim().isNotEmpty,
                        title: _hintTitle(),
                        message: hintMessage,
                      ),
                      const SizedBox(height: 12),
                      if (status.dependencies.managed &&
                          !status.dependencies.ready)
                        _DependencyStrip(
                          status: status.dependencies,
                          onDownload: controlsDisabled
                              ? null
                              : _downloadDependencies,
                        ),
                    ],
                  ),
                ),
              ),
            ),
          ),
          Positioned(
            left: 0,
            right: 0,
            bottom: 54,
            child: SafeArea(
              top: false,
              child: Center(
                child: _VersionStrip(version: status.version.fullVersion),
              ),
            ),
          ),
          Positioned(
            left: 0,
            right: 0,
            bottom: 0,
            child: SafeArea(
              top: false,
              child: _FooterBar(
                disabled: quitting,
                onSettings: _openSettings,
                onStats: _openStats,
                onLogs: _openLogs,
                onAbout: _openAbout,
                onExit: _quitApp,
              ),
            ),
          ),
        ],
      ),
    );
  }

  String _hintTitle() {
    if (quitting) {
      return 'Выход';
    }
    if (booting) {
      return 'Запуск';
    }
    if (busyTasks.containsKey('vpn-disconnect')) {
      return 'Отключение';
    }
    if (busyTasks.containsKey('vpn-connect')) {
      return 'Подбор работающих стратегий';
    }
    if (connectionBusy) {
      return status.connected ? 'Отключение' : 'Подбор маршрута';
    }
    if (!online) {
      return 'Bridge';
    }
    if (status.connected) {
      return 'Маршрутизация';
    }
    return 'Готово';
  }

  Future<void> _openSettings() async {
    final updated = await showDialog<AppConfig>(
      context: context,
      builder: (context) => _SettingsDialog(
        bridge: widget.bridge,
        initialConfig: appConfig,
        currentStatus: status,
        onCheckUpdates: () async {
          Navigator.of(context).pop(appConfig);
          await _checkUpdates();
        },
        onDownloadDependencies: () async {
          Navigator.of(context).pop(appConfig);
          await _downloadDependencies();
        },
      ),
    );
    if (updated != null && mounted) {
      setState(() => appConfig = updated);
      await _refresh(all: true);
    }
  }

  Future<void> _openSubscription() async {
    final changed = await showDialog<bool>(
      context: context,
      builder: (context) => _SubscriptionDialog(
        bridge: widget.bridge,
        subscription: subscription,
      ),
    );
    if (changed == true) {
      await _refresh(all: true);
    }
  }

  Future<void> _openWireGuard() async {
    final changed = await showDialog<bool>(
      context: context,
      builder: (context) => _WireGuardDialog(
        bridge: widget.bridge,
        initialConfigs: wireGuards,
        vpnRunning: status.connected || connectionBusy,
      ),
    );
    if (changed == true) {
      await _refresh(all: true);
    }
  }

  Future<void> _openStats() async {
    final stats = await widget.bridge.trafficStats().catchError(
      (_) => TrafficStatsInfo.empty,
    );
    if (!mounted) {
      return;
    }
    await showDialog<void>(
      context: context,
      builder: (context) => _StatsDialog(
        bridge: widget.bridge,
        initialStats: stats,
        status: status,
        subscription: subscription,
      ),
    );
  }

  Future<void> _openLogs() async {
    await _refresh();
    if (!mounted) {
      return;
    }
    await showDialog<void>(
      context: context,
      builder: (context) => _LogsDialog(
        logs: logs,
        onOpenFolder: () => widget.bridge.openLogsFolder(),
      ),
    );
  }

  Future<void> _openAbout() async {
    await showDialog<void>(
      context: context,
      builder: (context) => _AppDialog(
        title: 'About',
        icon: Icons.info_outline,
        child: Column(
          children: [
            const _LogoMark(connected: false, connecting: false, error: false),
            const SizedBox(height: 12),
            _FactRow(label: 'Версия', value: status.version.fullVersion),
            _LinkFactRow(
              label: 'Telegram',
              value: appConfig.telegramName,
              onPressed: () =>
                  widget.bridge.openExternal(appConfig.telegramUrl),
            ),
            _LinkFactRow(
              label: 'GitHub',
              value: appConfig.githubRepo,
              onPressed: () => widget.bridge.openExternal(appConfig.githubUrl),
            ),
            const SizedBox(height: 8),
            const Text(
              'Официальная сборка dropo. Скачивайте приложение только из GitHub Releases основного репозитория.',
              style: TextStyle(color: Color(0xFF9BB0AB), height: 1.35),
            ),
          ],
        ),
      ),
    );
  }
}

class _GradientBackdrop extends StatelessWidget {
  const _GradientBackdrop();

  @override
  Widget build(BuildContext context) {
    return DecoratedBox(
      decoration: const BoxDecoration(
        gradient: LinearGradient(
          begin: Alignment.topLeft,
          end: Alignment.bottomRight,
          colors: [Color(0xFF101617), Color(0xFF17322E), Color(0xFF231B27)],
          stops: [0, 0.48, 1],
        ),
      ),
      child: CustomPaint(painter: _GridPainter()),
    );
  }
}

class _GridPainter extends CustomPainter {
  @override
  void paint(Canvas canvas, Size size) {
    final paint = Paint()
      ..color = Colors.white.withValues(alpha: 0.035)
      ..strokeWidth = 1;
    const step = 34.0;
    for (double x = 0; x < size.width; x += step) {
      canvas.drawLine(Offset(x, 0), Offset(x, size.height), paint);
    }
    for (double y = 0; y < size.height; y += step) {
      canvas.drawLine(Offset(0, y), Offset(size.width, y), paint);
    }
  }

  @override
  bool shouldRepaint(covariant CustomPainter oldDelegate) => false;
}

class _LogoMark extends StatelessWidget {
  const _LogoMark({
    required this.connected,
    required this.connecting,
    required this.error,
  });

  final bool connected;
  final bool connecting;
  final bool error;

  @override
  Widget build(BuildContext context) {
    final gradient = error
        ? const LinearGradient(colors: [Color(0xFFEF4444), Color(0xFF991B1B)])
        : connected
        ? const LinearGradient(colors: [Color(0xFF22C55E), Color(0xFF15803D)])
        : connecting
        ? const LinearGradient(colors: [Color(0xFFF59E0B), Color(0xFFD97706)])
        : const LinearGradient(
            colors: [Color(0xFF1F8C78), Color(0xFF2F625B), Color(0xFFEF8F69)],
          );
    return AnimatedContainer(
      duration: const Duration(milliseconds: 260),
      width: 72,
      height: 72,
      decoration: BoxDecoration(
        gradient: gradient,
        borderRadius: BorderRadius.circular(18),
        boxShadow: [
          BoxShadow(
            color:
                (connected
                        ? const Color(0xFF22C55E)
                        : connecting
                        ? const Color(0xFFF59E0B)
                        : const Color(0xFF148F72))
                    .withValues(alpha: 0.34),
            blurRadius: 42,
            offset: const Offset(0, 16),
          ),
        ],
        border: Border.all(color: Colors.white.withValues(alpha: 0.12)),
      ),
      child: const Stack(
        alignment: Alignment.center,
        children: [
          Positioned(
            top: 13,
            child: Text(
              'Dr',
              style: TextStyle(fontSize: 27, fontWeight: FontWeight.w900),
            ),
          ),
          Positioned(
            bottom: 13,
            child: Text(
              'opo',
              style: TextStyle(
                fontSize: 13,
                fontWeight: FontWeight.w900,
                color: Color(0xFFC8F8D4),
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _Badges extends StatelessWidget {
  const _Badges({
    required this.subscription,
    required this.wireGuardCount,
    required this.onSubscription,
    required this.onWireGuard,
  });

  final SubscriptionInfo subscription;
  final int wireGuardCount;
  final VoidCallback? onSubscription;
  final VoidCallback? onWireGuard;

  @override
  Widget build(BuildContext context) {
    return Column(
      children: [
        _Badge(
          icon: Icons.vpn_key,
          label: subscription.hasSubscription
              ? 'VPN: ${subscription.proxyCount} proxy'
              : 'VPN-подписка не настроена',
          danger: !subscription.hasSubscription,
          onPressed: onSubscription,
        ),
        const SizedBox(height: 6),
        _Badge(
          icon: Icons.hub,
          label: wireGuardCount > 0
              ? 'Рабочие сети: $wireGuardCount'
              : 'Рабочие сети',
          cool: true,
          onPressed: onWireGuard,
        ),
      ],
    );
  }
}

class _NetworkModePill extends StatelessWidget {
  const _NetworkModePill({required this.status});

  final CoreStatus status;

  @override
  Widget build(BuildContext context) {
    final label = status.networkLabel.isEmpty
        ? status.networkMode
        : status.networkLabel;
    return ConstrainedBox(
      constraints: const BoxConstraints(maxWidth: 210),
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 9, vertical: 7),
        decoration: BoxDecoration(
          color: Colors.black.withValues(alpha: 0.36),
          borderRadius: BorderRadius.circular(12),
          border: Border.all(
            color: const Color(0xFF36D399).withValues(alpha: 0.22),
          ),
          boxShadow: [
            BoxShadow(
              color: Colors.black.withValues(alpha: 0.24),
              blurRadius: 24,
              offset: const Offset(0, 10),
            ),
          ],
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            const Icon(Icons.route, size: 11, color: Color(0xFFBAF7D0)),
            const SizedBox(width: 5),
            Flexible(
              child: Text(
                label,
                maxLines: 1,
                overflow: TextOverflow.ellipsis,
                style: const TextStyle(
                  color: Color(0xFFBAF7D0),
                  fontSize: 10,
                  fontWeight: FontWeight.w800,
                ),
              ),
            ),
          ],
        ),
      ),
    );
  }
}

class _Badge extends StatelessWidget {
  const _Badge({
    required this.icon,
    required this.label,
    this.onPressed,
    this.danger = false,
    this.cool = false,
  });

  final IconData icon;
  final String label;
  final VoidCallback? onPressed;
  final bool danger;
  final bool cool;

  @override
  Widget build(BuildContext context) {
    final color = danger
        ? const Color(0xFFFCA5A5)
        : cool
        ? const Color(0xFF9BE7F5)
        : const Color(0xFFBAF7D0);
    return MouseRegion(
      cursor: onPressed == null
          ? SystemMouseCursors.basic
          : SystemMouseCursors.click,
      child: GestureDetector(
        onTap: onPressed,
        child: ConstrainedBox(
          constraints: const BoxConstraints(maxWidth: 280),
          child: Container(
            width: double.infinity,
            padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 7),
            decoration: BoxDecoration(
              color: color.withValues(alpha: 0.11),
              borderRadius: BorderRadius.circular(18),
              border: Border.all(color: color.withValues(alpha: 0.28)),
            ),
            child: Row(
              children: [
                Icon(icon, size: 14, color: color),
                const SizedBox(width: 7),
                Expanded(
                  child: Text(
                    label,
                    maxLines: 1,
                    overflow: TextOverflow.ellipsis,
                    style: TextStyle(
                      color: color,
                      fontSize: 11,
                      fontWeight: FontWeight.w700,
                    ),
                  ),
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

class _PowerButton extends StatefulWidget {
  const _PowerButton({
    required this.connected,
    required this.busy,
    required this.enabled,
    required this.onPressed,
  });

  final bool connected;
  final bool busy;
  final bool enabled;
  final VoidCallback? onPressed;

  @override
  State<_PowerButton> createState() => _PowerButtonState();
}

class _PowerButtonState extends State<_PowerButton>
    with SingleTickerProviderStateMixin {
  late final AnimationController controller;

  @override
  void initState() {
    super.initState();
    controller = AnimationController(
      vsync: this,
      duration: const Duration(milliseconds: 900),
    )..repeat();
  }

  @override
  void dispose() {
    controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    final activeColor = widget.connected
        ? const Color(0xFF22C55E)
        : widget.busy
        ? const Color(0xFFF59E0B)
        : const Color(0xFF4A5568);
    return MouseRegion(
      cursor: widget.enabled
          ? SystemMouseCursors.click
          : (widget.busy
                ? SystemMouseCursors.progress
                : SystemMouseCursors.basic),
      child: GestureDetector(
        onTap: widget.enabled ? widget.onPressed : null,
        child: AnimatedScale(
          duration: const Duration(milliseconds: 140),
          scale: widget.enabled ? 1 : 0.99,
          child: AnimatedContainer(
            duration: const Duration(milliseconds: 260),
            width: 122,
            height: 122,
            decoration: BoxDecoration(
              shape: BoxShape.circle,
              gradient: LinearGradient(
                begin: Alignment.topLeft,
                end: Alignment.bottomRight,
                colors: widget.connected
                    ? const [Color(0xFF1A4D2E), Color(0xFF0D2818)]
                    : widget.busy
                    ? const [Color(0xFF3B2A15), Color(0xFF17140F)]
                    : const [Color(0xFF1C2F2D), Color(0xFF111B1B)],
              ),
              border: Border.all(
                color: activeColor.withValues(alpha: widget.busy ? 0.34 : 0.45),
                width: 3,
              ),
              boxShadow: [
                const BoxShadow(
                  color: Colors.black54,
                  blurRadius: 30,
                  offset: Offset(12, 12),
                ),
                BoxShadow(
                  color: activeColor.withValues(
                    alpha: widget.connected || widget.busy ? 0.26 : 0.08,
                  ),
                  blurRadius: 52,
                ),
              ],
            ),
            child: Stack(
              alignment: Alignment.center,
              children: [
                if (widget.busy)
                  RotationTransition(
                    turns: controller,
                    child: SizedBox(
                      width: 58,
                      height: 58,
                      child: CircularProgressIndicator(
                        strokeWidth: 3,
                        color: const Color(0xFF8EE7B6),
                        backgroundColor: Colors.white.withValues(alpha: 0.12),
                      ),
                    ),
                  )
                else
                  Icon(
                    Icons.power_settings_new,
                    size: 52,
                    color: activeColor,
                    shadows: widget.connected
                        ? [
                            const Shadow(
                              color: Color(0xAA22C55E),
                              blurRadius: 16,
                            ),
                          ]
                        : null,
                  ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}

class _ConnectionStatus extends StatelessWidget {
  const _ConnectionStatus({
    required this.connected,
    required this.connecting,
    required this.online,
    required this.hasError,
    required this.text,
  });

  final bool connected;
  final bool connecting;
  final bool online;
  final bool hasError;
  final String text;

  @override
  Widget build(BuildContext context) {
    final color = !online || hasError
        ? const Color(0xFFFCA5A5)
        : connecting
        ? const Color(0xFFF59E0B)
        : connected
        ? const Color(0xFF22C55E)
        : const Color(0xFF8892B0);
    return AnimatedDefaultTextStyle(
      duration: const Duration(milliseconds: 180),
      style: TextStyle(
        color: color,
        fontSize: 15,
        fontWeight: FontWeight.w700,
        shadows: connected || connecting
            ? [Shadow(color: color.withValues(alpha: 0.45), blurRadius: 18)]
            : null,
      ),
      child: Text(text, textAlign: TextAlign.center),
    );
  }
}

class _ConnectionHint extends StatelessWidget {
  const _ConnectionHint({
    required this.visible,
    required this.title,
    required this.message,
  });

  final bool visible;
  final String title;
  final String message;

  @override
  Widget build(BuildContext context) {
    return AnimatedSwitcher(
      duration: const Duration(milliseconds: 180),
      child: !visible
          ? const SizedBox(height: 0)
          : Container(
              key: ValueKey('$title$message'),
              width: double.infinity,
              padding: const EdgeInsets.all(11),
              decoration: BoxDecoration(
                color: const Color(0xFF2A2218).withValues(alpha: 0.72),
                borderRadius: BorderRadius.circular(12),
                border: Border.all(
                  color: const Color(0xFFF59E0B).withValues(alpha: 0.22),
                ),
              ),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(
                    title.toUpperCase(),
                    style: const TextStyle(
                      color: Color(0xFFF8D38B),
                      fontSize: 10,
                      fontWeight: FontWeight.w800,
                    ),
                  ),
                  const SizedBox(height: 3),
                  Text(
                    message,
                    style: const TextStyle(
                      color: Color(0xFFD8E4E0),
                      fontSize: 12,
                      height: 1.35,
                    ),
                  ),
                ],
              ),
            ),
    );
  }
}

class _DependencyStrip extends StatelessWidget {
  const _DependencyStrip({required this.status, required this.onDownload});

  final DepsStatus status;
  final VoidCallback? onDownload;

  @override
  Widget build(BuildContext context) {
    if (!status.managed) {
      return const SizedBox.shrink();
    }
    if (status.ready) {
      return const SizedBox.shrink();
    }
    return MouseRegion(
      cursor: onDownload == null
          ? SystemMouseCursors.basic
          : SystemMouseCursors.click,
      child: GestureDetector(
        onTap: onDownload,
        child: _MiniStrip(
          icon: Icons.download,
          color: const Color(0xFFF59E0B),
          text: 'Нужны компоненты ${status.sizeMb} MB. Нажмите, чтобы скачать.',
        ),
      ),
    );
  }
}

class _VersionStrip extends StatelessWidget {
  const _VersionStrip({required this.version});

  final String version;

  @override
  Widget build(BuildContext context) {
    return Text(
      'v$version',
      textAlign: TextAlign.center,
      style: const TextStyle(
        color: Color(0xFF7F8A95),
        fontSize: 11,
        fontWeight: FontWeight.w600,
      ),
    );
  }
}

class _MiniStrip extends StatelessWidget {
  const _MiniStrip({
    required this.icon,
    required this.color,
    required this.text,
  });

  final IconData icon;
  final Color color;
  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      width: double.infinity,
      padding: const EdgeInsets.symmetric(horizontal: 11, vertical: 9),
      decoration: BoxDecoration(
        color: Colors.black.withValues(alpha: 0.22),
        borderRadius: BorderRadius.circular(12),
        border: Border.all(color: Colors.white.withValues(alpha: 0.08)),
      ),
      child: Row(
        children: [
          Icon(icon, size: 16, color: color),
          const SizedBox(width: 8),
          Expanded(
            child: Text(
              text,
              maxLines: 2,
              overflow: TextOverflow.ellipsis,
              style: const TextStyle(color: Color(0xFFB7CAC5), fontSize: 11),
            ),
          ),
        ],
      ),
    );
  }
}

class _FooterBar extends StatelessWidget {
  const _FooterBar({
    required this.disabled,
    required this.onSettings,
    required this.onStats,
    required this.onLogs,
    required this.onAbout,
    required this.onExit,
  });

  final bool disabled;
  final VoidCallback onSettings;
  final VoidCallback onStats;
  final VoidCallback onLogs;
  final VoidCallback onAbout;
  final VoidCallback onExit;

  @override
  Widget build(BuildContext context) {
    return Container(
      height: 46,
      decoration: BoxDecoration(
        color: Colors.black.withValues(alpha: 0.20),
        border: Border(
          top: BorderSide(color: Colors.white.withValues(alpha: 0.08)),
        ),
      ),
      child: Row(
        children: [
          const SizedBox(width: 6),
          _FooterButton(
            icon: Icons.settings,
            label: 'Настройки',
            onPressed: disabled ? null : onSettings,
          ),
          _FooterButton(
            icon: Icons.query_stats,
            label: 'Статистика',
            onPressed: disabled ? null : onStats,
          ),
          _FooterButton(
            icon: Icons.article,
            label: 'Логи',
            onPressed: disabled ? null : onLogs,
          ),
          _FooterButton(
            icon: Icons.info_outline,
            label: 'About',
            onPressed: disabled ? null : onAbout,
          ),
          const Spacer(),
          _FooterButton(
            icon: Icons.logout,
            label: 'Выход',
            danger: true,
            onPressed: disabled ? null : onExit,
          ),
          const SizedBox(width: 6),
        ],
      ),
    );
  }
}

class _FooterButton extends StatelessWidget {
  const _FooterButton({
    required this.icon,
    required this.label,
    required this.onPressed,
    this.danger = false,
  });

  final IconData icon;
  final String label;
  final VoidCallback? onPressed;
  final bool danger;

  @override
  Widget build(BuildContext context) {
    final color = danger ? const Color(0xFFFCA5A5) : const Color(0xFF9AA8A5);
    return MouseRegion(
      cursor: onPressed == null
          ? SystemMouseCursors.basic
          : SystemMouseCursors.click,
      child: TextButton.icon(
        onPressed: onPressed,
        icon: Icon(icon, size: 15),
        label: Text(label, overflow: TextOverflow.ellipsis),
        style: _withClickCursor(
          TextButton.styleFrom(
            foregroundColor: color,
            disabledForegroundColor: color.withValues(alpha: 0.35),
            textStyle: const TextStyle(
              fontSize: 10,
              fontWeight: FontWeight.w700,
            ),
            padding: const EdgeInsets.symmetric(horizontal: 7),
            minimumSize: const Size(0, 34),
          ),
        ),
      ),
    );
  }
}

class _AppDialog extends StatelessWidget {
  const _AppDialog({
    required this.title,
    required this.icon,
    required this.child,
    this.width = 460,
    this.centered = false,
    this.heightFactor = 0.9,
  });

  final String title;
  final IconData icon;
  final Widget child;
  final double width;
  final bool centered;
  final double heightFactor;

  @override
  Widget build(BuildContext context) {
    final media = MediaQuery.of(context);
    return Dialog(
      alignment: centered ? Alignment.center : Alignment.centerLeft,
      backgroundColor: Colors.transparent,
      insetPadding: centered
          ? const EdgeInsets.symmetric(horizontal: 22, vertical: 24)
          : EdgeInsets.zero,
      child: ConstrainedBox(
        constraints: BoxConstraints(
          maxHeight: centered
              ? media.size.height * heightFactor.clamp(0.45, 0.96)
              : media.size.height,
        ),
        child: Container(
          width: width.clamp(320.0, media.size.width * 0.92).toDouble(),
          height: centered ? null : media.size.height,
          padding: EdgeInsets.fromLTRB(
            22,
            centered ? 20 : media.padding.top + 18,
            22,
            centered ? 20 : media.padding.bottom + 18,
          ),
          decoration: BoxDecoration(
            gradient: const LinearGradient(
              begin: Alignment.topLeft,
              end: Alignment.bottomRight,
              colors: [Color(0xFA142121), Color(0xFA11181A), Color(0xFA211921)],
              stops: [0, 0.58, 1],
            ),
            borderRadius: centered
                ? BorderRadius.circular(18)
                : const BorderRadius.horizontal(right: Radius.circular(18)),
            border: Border.all(color: Colors.white.withValues(alpha: 0.12)),
            boxShadow: [
              BoxShadow(
                color: Colors.black.withValues(alpha: 0.58),
                blurRadius: centered ? 52 : 70,
                offset: centered ? const Offset(0, 18) : const Offset(18, 0),
              ),
            ],
          ),
          child: Column(
            mainAxisSize: centered ? MainAxisSize.min : MainAxisSize.max,
            crossAxisAlignment: CrossAxisAlignment.stretch,
            children: [
              Row(
                children: [
                  Icon(icon, color: const Color(0xFFBAF7D0)),
                  const SizedBox(width: 10),
                  Expanded(
                    child: Text(
                      title,
                      style: const TextStyle(
                        fontSize: 18,
                        fontWeight: FontWeight.w800,
                      ),
                    ),
                  ),
                  IconButton(
                    onPressed: () => Navigator.of(context).pop(),
                    icon: const Icon(Icons.close),
                    tooltip: 'Закрыть',
                    mouseCursor: SystemMouseCursors.click,
                  ),
                ],
              ),
              const SizedBox(height: 14),
              Flexible(
                child: Scrollbar(
                  thumbVisibility: true,
                  child: SingleChildScrollView(child: child),
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _LogsDialog extends StatelessWidget {
  const _LogsDialog({required this.logs, required this.onOpenFolder});

  final List<String> logs;
  final Future<void> Function() onOpenFolder;

  @override
  Widget build(BuildContext context) {
    final text = logs.isEmpty ? 'Логи пока пустые' : logs.join('\n');
    return _AppDialog(
      width: 760,
      title: 'Логи',
      icon: Icons.article,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Row(
            children: [
              const Expanded(
                child: Text(
                  'Текст можно выделять мышью и копировать.',
                  style: TextStyle(color: Color(0xFF9BB0AB), fontSize: 12),
                ),
              ),
              _ActionButton(
                label: 'Копировать всё',
                icon: Icons.copy,
                compact: true,
                onPressed: () async {
                  await Clipboard.setData(ClipboardData(text: text));
                  if (context.mounted) {
                    ScaffoldMessenger.maybeOf(context)?.showSnackBar(
                      const SnackBar(content: Text('Логи скопированы')),
                    );
                  }
                },
              ),
            ],
          ),
          const SizedBox(height: 10),
          Container(
            height: 420,
            padding: const EdgeInsets.all(12),
            decoration: BoxDecoration(
              color: Colors.black.withValues(alpha: 0.42),
              borderRadius: BorderRadius.circular(8),
              border: Border.all(color: Colors.white.withValues(alpha: 0.08)),
            ),
            child: Scrollbar(
              thumbVisibility: true,
              child: SingleChildScrollView(
                child: SelectionArea(
                  child: SelectableText(
                    text,
                    style: const TextStyle(
                      fontFamily: 'Consolas',
                      fontSize: 12,
                      height: 1.35,
                      color: Color(0xFFC8D8D5),
                    ),
                  ),
                ),
              ),
            ),
          ),
          const SizedBox(height: 12),
          Row(
            children: [
              Expanded(
                child: _ActionButton(
                  label: 'Открыть папку с логами',
                  icon: Icons.folder_open,
                  onPressed: () async {
                    await onOpenFolder();
                  },
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: _ActionButton(
                  label: 'Закрыть',
                  icon: Icons.close,
                  onPressed: () => Navigator.of(context).pop(),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _ActionButton extends StatelessWidget {
  const _ActionButton({
    required this.label,
    required this.icon,
    required this.onPressed,
    this.danger = false,
    this.compact = false,
    this.secondary = false,
  });

  final String label;
  final IconData icon;
  final VoidCallback? onPressed;
  final bool danger;
  final bool compact;
  final bool secondary;

  @override
  Widget build(BuildContext context) {
    return MouseRegion(
      cursor: onPressed == null
          ? SystemMouseCursors.basic
          : SystemMouseCursors.click,
      child: FilledButton.icon(
        onPressed: onPressed,
        icon: Icon(icon, size: compact ? 15 : 18),
        label: Text(label),
        style: _withClickCursor(
          FilledButton.styleFrom(
            backgroundColor: danger
                ? const Color(0xFF7F1D1D)
                : secondary
                ? const Color(0xFF263331)
                : const Color(0xFF1F8C78),
            foregroundColor: secondary ? const Color(0xFFD4E5E0) : Colors.white,
            padding: EdgeInsets.symmetric(
              horizontal: compact ? 10 : 14,
              vertical: compact ? 8 : 13,
            ),
            textStyle: TextStyle(
              fontSize: compact ? 12 : 13,
              fontWeight: FontWeight.w800,
            ),
            shape: RoundedRectangleBorder(
              borderRadius: BorderRadius.circular(8),
            ),
          ),
        ),
      ),
    );
  }
}

class _InfoBand extends StatelessWidget {
  const _InfoBand({
    required this.icon,
    required this.title,
    required this.body,
  });

  final IconData icon;
  final String title;
  final String body;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.black.withValues(alpha: 0.24),
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: Colors.white.withValues(alpha: 0.08)),
      ),
      child: Row(
        children: [
          Icon(icon, color: const Color(0xFF36D399)),
          const SizedBox(width: 10),
          Expanded(
            child: Column(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Text(
                  title,
                  style: const TextStyle(fontWeight: FontWeight.w800),
                ),
                const SizedBox(height: 3),
                Text(body, style: const TextStyle(color: Color(0xFF9CB0AD))),
              ],
            ),
          ),
        ],
      ),
    );
  }
}

class _SettingsDialog extends StatefulWidget {
  const _SettingsDialog({
    required this.bridge,
    required this.initialConfig,
    required this.currentStatus,
    required this.onCheckUpdates,
    required this.onDownloadDependencies,
  });

  final CoreBridge bridge;
  final AppConfig initialConfig;
  final CoreStatus currentStatus;
  final VoidCallback onCheckUpdates;
  final VoidCallback onDownloadDependencies;

  @override
  State<_SettingsDialog> createState() => _SettingsDialogState();
}

class _SettingsDialogState extends State<_SettingsDialog> {
  late AppConfig config = widget.initialConfig;
  String statusText = '';
  bool saving = false;

  Future<void> _saveGeneral(AppConfig updated) async {
    setState(() {
      config = updated;
      saving = true;
      statusText = 'Сохраняем настройки...';
    });
    final result = await widget.bridge.saveAppConfig(updated);
    if (!mounted) {
      return;
    }
    setState(() {
      saving = false;
      statusText = result['success'] == false
          ? result['error']?.toString() ?? 'Не удалось сохранить настройки'
          : 'Настройки применены';
    });
  }

  Future<void> _applySpecial(
    Future<Map<String, dynamic>> Function() action,
    AppConfig updated,
  ) async {
    setState(() {
      saving = true;
      statusText = 'Применяем настройки...';
    });
    final result = await action();
    if (!mounted) {
      return;
    }
    setState(() {
      saving = false;
      if (result['success'] == false) {
        statusText = result['error']?.toString() ?? 'Не удалось применить';
      } else {
        config = updated;
        statusText = result['message']?.toString() ?? 'Настройки применены';
      }
    });
  }

  Future<void> _runQuickCheck() async {
    setState(() {
      saving = true;
      statusText = 'Запускаем встроенную проверку сервисов...';
    });
    final result = await widget.bridge.runQuickCheck();
    if (!mounted) {
      return;
    }
    final total = _asInt(result['total']);
    final failed = _asInt(result['failedCount']);
    setState(() {
      saving = false;
      statusText = result['success'] == true
          ? 'Проверка завершена: $total сервисов доступны.'
          : 'Проверка завершена с предупреждениями: ошибок $failed из $total.';
    });
  }

  Future<void> _captureFingerprint() async {
    setState(() {
      saving = true;
      statusText = 'Снимаем отпечаток блокировок. VPN должен быть отключён...';
    });
    final result = await widget.bridge.captureFingerprint();
    if (!mounted) {
      return;
    }
    setState(() {
      saving = false;
      statusText = result['success'] == true
          ? 'Отпечаток сохранён: ${result['path'] ?? 'файл создан'}.'
          : result['error']?.toString() ?? 'Не удалось снять отпечаток.';
    });
  }

  Future<void> _openConfigFolder() async {
    await widget.bridge.openConfigFolder();
    if (mounted) {
      setState(() => statusText = 'Открыта папка конфигурации.');
    }
  }

  Future<void> _openFingerprintFolder() async {
    await widget.bridge.openFingerprintFolder();
    if (mounted) {
      setState(() => statusText = 'Открыта папка отпечатков.');
    }
  }

  @override
  Widget build(BuildContext context) {
    return _AppDialog(
      title: 'Настройки',
      icon: Icons.settings,
      width: 612,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Text(
            'Настройки приложения',
            style: TextStyle(color: Color(0xFF8892B0), fontSize: 12),
          ),
          const SizedBox(height: 14),
          _SettingsGroup(
            title: 'Общие',
            children: [
              _SwitchSetting(
                title: 'Автозапуск',
                description: 'Запускать при входе в систему',
                value: config.autoStart,
                onChanged: saving
                    ? null
                    : (value) =>
                          _saveGeneral(config.copyWith(autoStart: value)),
              ),
              _SwitchSetting(
                title: 'Уведомления',
                description: 'Показывать уведомления о подключении',
                value: config.notifications,
                onChanged: saving
                    ? null
                    : (value) =>
                          _saveGeneral(config.copyWith(notifications: value)),
              ),
              _SwitchSetting(
                title: 'Логирование sing-box',
                description: 'Записывать логи в файл',
                value: config.enableLogging,
                onChanged: saving
                    ? null
                    : (value) =>
                          _saveGeneral(config.copyWith(enableLogging: value)),
              ),
              _SelectSetting(
                title: 'Уровень логирования',
                description: 'Детализация логов sing-box',
                value: config.logLevel,
                options: const {
                  'error': 'Error',
                  'warn': 'Warn',
                  'info': 'Info',
                  'debug': 'Debug',
                  'trace': 'Trace',
                },
                onChanged: saving
                    ? null
                    : (value) => _saveGeneral(config.copyWith(logLevel: value)),
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Подписка',
            children: [
              _SwitchSetting(
                title: 'Авто-обновление',
                description: 'Обновлять подписку автоматически',
                value: config.autoUpdateSub,
                onChanged: saving
                    ? null
                    : (value) =>
                          _saveGeneral(config.copyWith(autoUpdateSub: value)),
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Обновления',
            children: [
              _SwitchSetting(
                title: 'Проверять обновления',
                description: 'Уведомлять о новых версиях',
                value: config.checkUpdates,
                onChanged: saving
                    ? null
                    : (value) =>
                          _saveGeneral(config.copyWith(checkUpdates: value)),
              ),
              _ButtonSetting(
                title: 'Проверить сейчас',
                description: 'Открыть проверку GitHub Releases',
                label: 'Проверить',
                icon: Icons.system_update_alt,
                onPressed: widget.onCheckUpdates,
              ),
              _ButtonSetting(
                title: 'Компоненты',
                description: widget.currentStatus.dependencies.ready
                    ? 'Зависимости загружены'
                    : 'Скачать архив зависимостей',
                label: 'Скачать',
                icon: Icons.download,
                onPressed: widget.onDownloadDependencies,
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Внешний вид',
            children: [
              _SelectSetting(
                title: 'Тема',
                description: 'Оформление приложения',
                value: config.theme,
                options: const {
                  'dark': 'Тёмная',
                  'light': 'Светлая',
                  'system': 'Системная',
                },
                onChanged: saving
                    ? null
                    : (value) => _saveGeneral(config.copyWith(theme: value)),
              ),
              _SelectSetting(
                title: 'Язык',
                description: 'Язык интерфейса',
                value: config.language,
                options: const {'ru': 'Русский', 'en': 'English'},
                onChanged: saving
                    ? null
                    : (value) => _saveGeneral(config.copyWith(language: value)),
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Маршрутизация',
            children: [
              _SelectSetting(
                title: 'Режим маршрутизации',
                description: _routingModeDescription(config.routingMode),
                value: config.routingMode,
                stacked: true,
                options: const {
                  'blocked_only': 'Только заблокированные',
                  'except_russia': 'Всё кроме России',
                  'all_traffic': 'Весь трафик',
                },
                onChanged: saving
                    ? null
                    : (value) => _applySpecial(
                        () => widget.bridge.setRoutingMode(value),
                        config.copyWith(routingMode: value),
                      ),
              ),
              _SwitchSetting(
                title: 'Открывать все иностранные сайты через VPN/обход',
                description:
                    'RU-сервисы остаются напрямую, остальное идёт через обход.',
                value: config.routingMode == 'except_russia',
                onChanged: saving
                    ? null
                    : (value) => _applySpecial(
                        () => widget.bridge.setRoutingMode(
                          value ? 'except_russia' : 'blocked_only',
                        ),
                        config.copyWith(
                          routingMode: value ? 'except_russia' : 'blocked_only',
                        ),
                      ),
              ),
              _SwitchSetting(
                title: 'Скрывать RU-трафик от провайдера',
                description:
                    'Российские сайты по умолчанию идут напрямую. Включите, чтобы завернуть их в VPN/прокси.',
                value: config.hideRuTraffic,
                onChanged: saving
                    ? null
                    : (value) => _applySpecial(
                        () => widget.bridge.setHideRuTraffic(
                          value,
                          config.ruProxyAddress,
                        ),
                        config.copyWith(hideRuTraffic: value),
                      ),
              ),
              if (config.hideRuTraffic)
                _TextSetting(
                  title: 'Адрес прокси для RU-трафика',
                  description: 'Необязательно: vless://, trojan:// или ss://',
                  initialValue: config.ruProxyAddress,
                  onSubmitted: (value) => _applySpecial(
                    () => widget.bridge.setHideRuTraffic(true, value),
                    config.copyWith(ruProxyAddress: value),
                  ),
                ),
              _SelectSetting(
                title: 'Сетевой режим',
                description: _networkModeDescription(config.networkMode),
                value: config.networkMode,
                stacked: true,
                options: const {
                  'auto': 'Auto',
                  'deep_windows': 'Deep Windows',
                  'compat_tun': 'Compatibility TUN',
                },
                onChanged: saving
                    ? null
                    : (value) => _applySpecial(
                        () => widget.bridge.setNetworkMode(value),
                        config.copyWith(networkMode: value),
                      ),
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Бесплатный доступ',
            children: [
              _SwitchSetting(
                title: 'Не использовать бесплатные методы',
                description:
                    'Если включено, для обхода блокировок потребуется рабочая VPN/подписка.',
                value: config.disableFreeAccess,
                onChanged: saving
                    ? null
                    : (value) => _applySpecial(
                        () => widget.bridge.setDisableFreeAccess(value),
                        config.copyWith(disableFreeAccess: value),
                      ),
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Диагностика',
            children: [
              _ButtonSetting(
                title: 'Проверить сервисы',
                description:
                    'Быстрая проверка доступности заблокированных и прямых сервисов.',
                label: 'Проверить',
                icon: Icons.search,
                onPressed: saving ? null : () => unawaited(_runQuickCheck()),
              ),
              _ButtonSetting(
                title: 'Отпечаток блокировки',
                description:
                    'Снимает RST/таймаут/IP/DNS-поведение провайдера. Запускайте при отключённом VPN.',
                label: 'Снять',
                icon: Icons.fingerprint,
                onPressed: saving
                    ? null
                    : () => unawaited(_captureFingerprint()),
              ),
              _ButtonSetting(
                title: 'Папка отпечатков',
                description: 'Открыть файлы DPI-отпечатков для отправки.',
                label: 'Открыть',
                icon: Icons.folder_special,
                onPressed: () => unawaited(_openFingerprintFolder()),
              ),
            ],
          ),
          _SettingsGroup(
            title: 'Конфигурация',
            children: [
              _ButtonSetting(
                title: 'Папка конфигурации',
                description: 'Открыть runtime-файлы, настройки и шаблоны.',
                label: 'Открыть',
                icon: Icons.folder_open,
                onPressed: () => unawaited(_openConfigFolder()),
              ),
            ],
          ),
          if (statusText.isNotEmpty) ...[
            const SizedBox(height: 6),
            _InfoBand(
              icon: saving ? Icons.hourglass_top : Icons.info_outline,
              title: saving ? 'Выполняется' : 'Статус',
              body: statusText,
            ),
          ],
          const SizedBox(height: 12),
          _ActionButton(
            label: 'Закрыть',
            icon: Icons.close,
            onPressed: () => Navigator.of(context).pop(config),
          ),
        ],
      ),
    );
  }
}

class _SettingsGroup extends StatelessWidget {
  const _SettingsGroup({required this.title, required this.children});

  final String title;
  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 16),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            title.toUpperCase(),
            style: const TextStyle(
              color: Color(0xFF7B8F89),
              fontSize: 11,
              fontWeight: FontWeight.w800,
              letterSpacing: 0.5,
            ),
          ),
          const SizedBox(height: 8),
          ...children,
        ],
      ),
    );
  }
}

class _SettingShell extends StatelessWidget {
  const _SettingShell({
    required this.title,
    required this.description,
    required this.trailing,
    this.stacked = false,
  });

  final String title;
  final String description;
  final Widget trailing;
  final bool stacked;

  @override
  Widget build(BuildContext context) {
    final label = Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          title,
          maxLines: 2,
          overflow: TextOverflow.ellipsis,
          style: const TextStyle(fontSize: 12, fontWeight: FontWeight.w800),
        ),
        const SizedBox(height: 3),
        Text(
          description,
          maxLines: 3,
          overflow: TextOverflow.ellipsis,
          style: const TextStyle(
            color: Color(0xFF7F918C),
            fontSize: 10,
            height: 1.25,
          ),
        ),
      ],
    );
    return Container(
      margin: const EdgeInsets.only(bottom: 7),
      padding: const EdgeInsets.symmetric(horizontal: 12, vertical: 11),
      decoration: BoxDecoration(
        color: Colors.white.withValues(alpha: 0.055),
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: Colors.white.withValues(alpha: 0.06)),
      ),
      child: stacked
          ? Column(
              crossAxisAlignment: CrossAxisAlignment.stretch,
              children: [label, const SizedBox(height: 8), trailing],
            )
          : Row(
              children: [
                Expanded(child: label),
                const SizedBox(width: 12),
                trailing,
              ],
            ),
    );
  }
}

class _SwitchSetting extends StatelessWidget {
  const _SwitchSetting({
    required this.title,
    required this.description,
    required this.value,
    required this.onChanged,
  });

  final String title;
  final String description;
  final bool value;
  final ValueChanged<bool>? onChanged;

  @override
  Widget build(BuildContext context) {
    return _SettingShell(
      title: title,
      description: description,
      trailing: Switch(value: value, onChanged: onChanged),
    );
  }
}

class _SelectSetting extends StatelessWidget {
  const _SelectSetting({
    required this.title,
    required this.description,
    required this.value,
    required this.options,
    required this.onChanged,
    this.stacked = false,
  });

  final String title;
  final String description;
  final String value;
  final Map<String, String> options;
  final ValueChanged<String>? onChanged;
  final bool stacked;

  @override
  Widget build(BuildContext context) {
    final field = DropdownButtonFormField<String>(
      initialValue: options.containsKey(value) ? value : options.keys.first,
      isDense: true,
      dropdownColor: const Color(0xFF14211F),
      decoration: _fieldDecoration(),
      items: [
        for (final entry in options.entries)
          DropdownMenuItem(value: entry.key, child: Text(entry.value)),
      ],
      onChanged: onChanged == null ? null : (value) => onChanged!(value!),
    );
    return _SettingShell(
      title: title,
      description: description,
      stacked: stacked,
      trailing: stacked ? field : SizedBox(width: 178, child: field),
    );
  }
}

class _TextSetting extends StatefulWidget {
  const _TextSetting({
    required this.title,
    required this.description,
    required this.initialValue,
    required this.onSubmitted,
  });

  final String title;
  final String description;
  final String initialValue;
  final ValueChanged<String> onSubmitted;

  @override
  State<_TextSetting> createState() => _TextSettingState();
}

class _TextSettingState extends State<_TextSetting> {
  late final controller = TextEditingController(text: widget.initialValue);

  @override
  void dispose() {
    controller.dispose();
    super.dispose();
  }

  @override
  Widget build(BuildContext context) {
    return _SettingShell(
      title: widget.title,
      description: widget.description,
      stacked: true,
      trailing: TextField(
        controller: controller,
        decoration: _fieldDecoration(hint: 'vless://...'),
        onSubmitted: widget.onSubmitted,
      ),
    );
  }
}

class _ButtonSetting extends StatelessWidget {
  const _ButtonSetting({
    required this.title,
    required this.description,
    required this.label,
    required this.icon,
    required this.onPressed,
  });

  final String title;
  final String description;
  final String label;
  final IconData icon;
  final VoidCallback? onPressed;

  @override
  Widget build(BuildContext context) {
    return _SettingShell(
      title: title,
      description: description,
      trailing: _ActionButton(
        label: label,
        icon: icon,
        compact: true,
        onPressed: onPressed,
      ),
    );
  }
}

class _SubscriptionDialog extends StatefulWidget {
  const _SubscriptionDialog({required this.bridge, required this.subscription});

  final CoreBridge bridge;
  final SubscriptionInfo subscription;

  @override
  State<_SubscriptionDialog> createState() => _SubscriptionDialogState();
}

class _SubscriptionDialogState extends State<_SubscriptionDialog> {
  late final controller = TextEditingController(text: widget.subscription.url);
  String statusText = '';
  String statusKind = '';
  bool busy = false;
  List<Map<String, dynamic>> proxyCandidates = const [];
  int selectedProxy = 0;
  int closeRemaining = 0;
  Timer? closeTimer;

  @override
  void dispose() {
    closeTimer?.cancel();
    controller.dispose();
    super.dispose();
  }

  bool _looksDirectLink(String value) {
    final lower = value.trim().toLowerCase();
    return lower.startsWith('vless://') ||
        lower.startsWith('trojan://') ||
        lower.startsWith('ss://') ||
        lower.startsWith('vmess://') ||
        lower.startsWith('hysteria2://') ||
        lower.startsWith('hy2://') ||
        lower.startsWith('tuic://');
  }

  List<Map<String, dynamic>> _proxyListFrom(dynamic raw) {
    if (raw is! List) {
      return const [];
    }
    return raw
        .whereType<Map>()
        .map(
          (item) => item.map((key, value) => MapEntry(key.toString(), value)),
        )
        .toList(growable: false);
  }

  String _proxyRawAt(int index) {
    if (index < 0 || index >= proxyCandidates.length) {
      return controller.text.trim();
    }
    final raw = proxyCandidates[index]['raw']?.toString().trim() ?? '';
    return raw.isEmpty ? controller.text.trim() : raw;
  }

  void _cancelAutoClose() {
    closeTimer?.cancel();
    closeTimer = null;
    if (closeRemaining > 0 && mounted) {
      setState(() => closeRemaining = 0);
    }
  }

  void _startAutoClose() {
    closeTimer?.cancel();
    setState(() => closeRemaining = 8);
    closeTimer = Timer.periodic(const Duration(seconds: 1), (_) {
      if (!mounted) {
        return;
      }
      if (closeRemaining <= 1) {
        closeTimer?.cancel();
        Navigator.of(context).pop(true);
        return;
      }
      setState(() => closeRemaining -= 1);
    });
  }

  Future<void> _paste() async {
    _cancelAutoClose();
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    if (data?.text != null) {
      controller.text = data!.text!;
    }
  }

  Future<void> _test() async {
    _cancelAutoClose();
    final value = controller.text.trim();
    if (value.isEmpty) {
      setState(() {
        statusKind = 'error';
        statusText = 'Вставьте ссылку на подписку или прямой proxy.';
      });
      return;
    }
    setState(() {
      busy = true;
      statusKind = 'loading';
      statusText = 'Проверяем подключение...';
      proxyCandidates = const [];
    });
    final result = await widget.bridge.testSubscription(value);
    if (!mounted) {
      return;
    }
    final ok = result['success'] == true;
    final direct =
        result['isDirectLink'] == true ||
        result['is_direct_link'] == true ||
        _looksDirectLink(value);
    final candidates = _proxyListFrom(result['proxies']);
    if (!ok) {
      setState(() {
        busy = false;
        statusKind = 'error';
        statusText =
            result['error']?.toString() ?? 'Подключение не прошло проверку';
      });
      return;
    }

    final saveValue = direct
        ? value
        : (candidates.isNotEmpty
              ? (candidates.first['raw']?.toString().trim().isNotEmpty == true
                    ? candidates.first['raw'].toString()
                    : value)
              : value);
    setState(() => statusText = 'Проверка успешна. Сохраняем подключение...');
    final saved = await widget.bridge.saveSubscription(saveValue);
    if (!mounted) {
      return;
    }
    if (saved['success'] == false) {
      setState(() {
        busy = false;
        statusKind = 'error';
        statusText = saved['error']?.toString() ?? 'Не удалось сохранить';
      });
      return;
    }

    if (direct) {
      Navigator.of(context).pop(true);
      return;
    }

    setState(() {
      busy = false;
      proxyCandidates = candidates;
      selectedProxy = 0;
      statusKind = 'success';
      statusText =
          'Найдено ${_asInt(result['count'])} proxy. Первое подключение сохранено.';
    });
    _startAutoClose();
  }

  Future<void> _selectProxy(int index) async {
    _cancelAutoClose();
    setState(() {
      selectedProxy = index;
      busy = true;
      statusKind = 'loading';
      statusText = 'Сохраняем выбранное подключение...';
    });
    final result = await widget.bridge.saveSubscription(_proxyRawAt(index));
    if (!mounted) {
      return;
    }
    if (result['success'] == false) {
      setState(() {
        busy = false;
        statusKind = 'error';
        statusText = result['error']?.toString() ?? 'Не удалось сохранить';
      });
      return;
    }
    setState(() {
      busy = false;
      statusKind = 'success';
      statusText = 'Выбранное подключение сохранено.';
    });
  }

  Future<void> _remove() async {
    _cancelAutoClose();
    controller.clear();
    setState(() {
      busy = true;
      statusKind = 'loading';
      statusText = 'Удаляем VPN-подписку...';
    });
    final result = await widget.bridge.saveSubscription('');
    if (!mounted) {
      return;
    }
    if (result['success'] == false) {
      setState(() {
        busy = false;
        statusKind = 'error';
        statusText = result['error']?.toString() ?? 'Не удалось удалить';
      });
      return;
    }
    Navigator.of(context).pop(true);
  }

  @override
  Widget build(BuildContext context) {
    return Listener(
      onPointerDown: (_) {
        if (closeRemaining > 0) {
          _cancelAutoClose();
        }
      },
      child: _AppDialog(
        title: 'Добавить VPN',
        icon: Icons.link,
        width: 560,
        centered: true,
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            const Text(
              'Вставьте ссылку на подписку или прямой VLESS/Trojan/SS proxy.',
              style: TextStyle(color: Color(0xFF8892B0), fontSize: 12),
            ),
            if (widget.subscription.hasSubscription) ...[
              const SizedBox(height: 12),
              Container(
                padding: const EdgeInsets.all(10),
                decoration: BoxDecoration(
                  color: Colors.black.withValues(alpha: 0.22),
                  borderRadius: BorderRadius.circular(8),
                ),
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    const Text(
                      'Текущая подписка:',
                      style: TextStyle(color: Color(0xFF6B7280), fontSize: 11),
                    ),
                    const SizedBox(height: 4),
                    SelectableText(
                      widget.subscription.url,
                      style: const TextStyle(
                        color: Color(0xFFB7CAC5),
                        fontSize: 11,
                      ),
                    ),
                  ],
                ),
              ),
            ],
            const SizedBox(height: 12),
            TextField(
              controller: controller,
              minLines: 1,
              maxLines: 4,
              decoration: _fieldDecoration(
                hint: 'https://... или vless://...',
                suffixIcon: IconButton(
                  onPressed: busy ? null : _paste,
                  icon: const Icon(Icons.content_paste),
                  tooltip: 'Вставить из буфера',
                  mouseCursor: busy
                      ? SystemMouseCursors.basic
                      : SystemMouseCursors.click,
                ),
              ),
            ),
            if (proxyCandidates.isNotEmpty) ...[
              const SizedBox(height: 12),
              const _StatsSectionTitle('Найденные подключения'),
              for (var i = 0; i < proxyCandidates.length && i < 8; i++)
                _ProxyCandidateTile(
                  proxy: proxyCandidates[i],
                  selected: selectedProxy == i,
                  onTap: busy ? null : () => _selectProxy(i),
                ),
            ],
            if (statusText.isNotEmpty) ...[
              const SizedBox(height: 12),
              _StatusBox(kind: statusKind, text: statusText),
            ],
            if (closeRemaining > 0) ...[
              const SizedBox(height: 12),
              ClipRRect(
                borderRadius: BorderRadius.circular(999),
                child: LinearProgressIndicator(
                  value: closeRemaining / 8,
                  minHeight: 6,
                  backgroundColor: Colors.white.withValues(alpha: 0.08),
                  color: const Color(0xFF36D399),
                ),
              ),
              const SizedBox(height: 6),
              Text(
                'Окно закроется через $closeRemaining сек. Любой клик остановит таймер.',
                textAlign: TextAlign.center,
                style: const TextStyle(color: Color(0xFF8A9B97), fontSize: 11),
              ),
            ],
            const SizedBox(height: 16),
            Row(
              children: [
                Expanded(
                  child: _DialogAction(
                    label: 'Отмена',
                    icon: Icons.close,
                    onPressed: busy
                        ? null
                        : () => Navigator.of(context).pop(false),
                  ),
                ),
                const SizedBox(width: 10),
                Expanded(
                  child: _DialogAction(
                    label: 'Проверить',
                    icon: Icons.fact_check,
                    primary: true,
                    onPressed: busy ? null : _test,
                  ),
                ),
                if (widget.subscription.hasSubscription) ...[
                  const SizedBox(width: 10),
                  Expanded(
                    child: _DialogAction(
                      label: 'Удалить',
                      icon: Icons.delete,
                      danger: true,
                      onPressed: busy ? null : _remove,
                    ),
                  ),
                ],
              ],
            ),
          ],
        ),
      ),
    );
  }
}

class _ProxyCandidateTile extends StatelessWidget {
  const _ProxyCandidateTile({
    required this.proxy,
    required this.selected,
    required this.onTap,
  });

  final Map<String, dynamic> proxy;
  final bool selected;
  final VoidCallback? onTap;

  @override
  Widget build(BuildContext context) {
    final type = proxy['type']?.toString().toUpperCase() ?? 'VPN';
    final name = proxy['name']?.toString();
    final server = proxy['server']?.toString() ?? '';
    final port = _asInt(proxy['port']);
    return MouseRegion(
      cursor: onTap == null
          ? SystemMouseCursors.basic
          : SystemMouseCursors.click,
      child: GestureDetector(
        onTap: onTap,
        child: Container(
          margin: const EdgeInsets.only(bottom: 8),
          padding: const EdgeInsets.all(11),
          decoration: BoxDecoration(
            color: selected
                ? const Color(0xFF1F8C78).withValues(alpha: 0.28)
                : Colors.white.withValues(alpha: 0.055),
            borderRadius: BorderRadius.circular(8),
            border: Border.all(
              color: selected
                  ? const Color(0xFF36D399).withValues(alpha: 0.46)
                  : Colors.white.withValues(alpha: 0.08),
            ),
          ),
          child: Row(
            children: [
              Icon(
                selected ? Icons.check_circle : Icons.radio_button_unchecked,
                size: 18,
                color: selected
                    ? const Color(0xFF86EFAC)
                    : const Color(0xFF7F918C),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      (name == null || name.isEmpty) ? '$type proxy' : name,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(
                        fontSize: 12,
                        fontWeight: FontWeight.w800,
                      ),
                    ),
                    const SizedBox(height: 2),
                    Text(
                      port > 0 ? '$type • $server:$port' : '$type • $server',
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(
                        color: Color(0xFF8EA2A0),
                        fontSize: 10,
                      ),
                    ),
                  ],
                ),
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _StatsDialog extends StatefulWidget {
  const _StatsDialog({
    required this.bridge,
    required this.initialStats,
    required this.status,
    required this.subscription,
  });

  final CoreBridge bridge;
  final TrafficStatsInfo initialStats;
  final CoreStatus status;
  final SubscriptionInfo subscription;

  @override
  State<_StatsDialog> createState() => _StatsDialogState();
}

class _StatsDialogState extends State<_StatsDialog> {
  late TrafficStatsInfo stats = widget.initialStats;
  Timer? timer;

  @override
  void initState() {
    super.initState();
    timer = Timer.periodic(const Duration(seconds: 2), (_) => _refresh());
  }

  @override
  void dispose() {
    timer?.cancel();
    super.dispose();
  }

  Future<void> _refresh() async {
    try {
      final loaded = await widget.bridge.trafficStats();
      if (mounted) {
        setState(() => stats = loaded);
      }
    } catch (_) {}
  }

  Future<void> _reset() async {
    await widget.bridge.resetTrafficStats();
    await _refresh();
  }

  @override
  Widget build(BuildContext context) {
    return _AppDialog(
      title: 'Статистика',
      icon: Icons.query_stats,
      width: 520,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Text(
            'Статистика использования VPN',
            style: TextStyle(color: Color(0xFF8892B0), fontSize: 12),
          ),
          const SizedBox(height: 14),
          _ChartPlaceholder(
            title: 'Скорость (последние 30 сек)',
            upload: stats.current.uploaded,
            download: stats.current.downloaded,
          ),
          const SizedBox(height: 10),
          _ChartPlaceholder(
            title: 'Трафик за сессию',
            upload: stats.current.uploaded,
            download: stats.current.downloaded,
            compact: true,
          ),
          const SizedBox(height: 14),
          const _StatsSectionTitle('Текущая сессия'),
          _StatsGrid(
            children: [
              _StatCard(
                icon: Icons.arrow_upward,
                value: stats.current.uploadedStr,
                label: 'Отправлено',
                color: const Color(0xFF36D399),
              ),
              _StatCard(
                icon: Icons.arrow_downward,
                value: stats.current.downloadedStr,
                label: 'Получено',
                color: const Color(0xFF60A5FA),
              ),
              _StatCard(
                icon: Icons.timer,
                value: stats.current.durationStr,
                label: 'Длительность',
                color: const Color(0xFFF59E0B),
              ),
            ],
          ),
          const SizedBox(height: 14),
          const _StatsSectionTitle('Всего'),
          _StatsGrid(
            children: [
              _StatCard(
                icon: Icons.arrow_upward,
                value: stats.total.uploadedStr,
                label: 'Отправлено',
                color: const Color(0xFF36D399),
              ),
              _StatCard(
                icon: Icons.arrow_downward,
                value: stats.total.downloadedStr,
                label: 'Получено',
                color: const Color(0xFF60A5FA),
              ),
              _StatCard(
                icon: Icons.timer,
                value: stats.total.durationStr,
                label: 'Время онлайн',
                color: const Color(0xFFF59E0B),
              ),
              _StatCard(
                icon: Icons.repeat,
                value: '${stats.sessions}',
                label: 'Сессий',
                color: const Color(0xFFA855F7),
              ),
            ],
          ),
          const SizedBox(height: 14),
          _InfoBand(
            icon: widget.status.connected
                ? Icons.shield
                : Icons.shield_outlined,
            title: widget.status.connected ? 'VPN активен' : 'Отключено',
            body:
                '${widget.status.networkLabel} • ${widget.subscription.hasSubscription ? '${widget.subscription.proxyCount} proxy' : 'подписка не настроена'}',
          ),
          const SizedBox(height: 12),
          Row(
            children: [
              Expanded(
                child: _ActionButton(
                  label: 'Сбросить',
                  icon: Icons.restart_alt,
                  danger: true,
                  onPressed: _reset,
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: _ActionButton(
                  label: 'Закрыть',
                  icon: Icons.close,
                  onPressed: () => Navigator.of(context).pop(),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _WireGuardDialog extends StatefulWidget {
  const _WireGuardDialog({
    required this.bridge,
    required this.initialConfigs,
    required this.vpnRunning,
  });

  final CoreBridge bridge;
  final List<WireGuardInfo> initialConfigs;
  final bool vpnRunning;

  @override
  State<_WireGuardDialog> createState() => _WireGuardDialogState();
}

class _WireGuardDialogState extends State<_WireGuardDialog> {
  late List<WireGuardInfo> configs = widget.initialConfigs;
  bool changed = false;

  Future<void> _reload() async {
    final loaded = await widget.bridge.wireGuards();
    if (mounted) {
      setState(() => configs = loaded);
    }
  }

  Future<void> _edit([WireGuardInfo? item]) async {
    WireGuardInfo? full = item;
    if (item != null) {
      full = await widget.bridge.wireGuardConfig(item.tag) ?? item;
    }
    if (!mounted) {
      return;
    }
    final saved = await showDialog<bool>(
      context: context,
      builder: (context) => _WireGuardEditDialog(
        bridge: widget.bridge,
        config: full,
        vpnRunning: widget.vpnRunning,
      ),
    );
    if (saved == true) {
      changed = true;
      await _reload();
    }
  }

  @override
  Widget build(BuildContext context) {
    return _AppDialog(
      title: 'Рабочие сети (WireGuard)',
      icon: Icons.hub,
      width: 580,
      centered: true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Text(
            'Все конфиги активны одновременно. Трафик направляется по AllowedIPs.',
            style: TextStyle(color: Color(0xFF8892B0), fontSize: 12),
          ),
          const SizedBox(height: 14),
          if (configs.isEmpty)
            const _EmptyState(text: 'Нет добавленных рабочих сетей')
          else
            for (final item in configs)
              _WireGuardTile(
                item: item,
                onEdit: widget.vpnRunning ? null : () => _edit(item),
              ),
          if (widget.vpnRunning) ...[
            const SizedBox(height: 10),
            const _StatusBox(
              kind: 'warning',
              text: 'Редактирование WireGuard доступно после отключения VPN.',
            ),
          ],
          const SizedBox(height: 16),
          Row(
            children: [
              Expanded(
                child: _ActionButton(
                  label: 'Закрыть',
                  icon: Icons.close,
                  onPressed: () => Navigator.of(context).pop(changed),
                ),
              ),
              const SizedBox(width: 10),
              Expanded(
                child: _ActionButton(
                  label: 'Добавить',
                  icon: Icons.add,
                  onPressed: widget.vpnRunning ? null : () => _edit(),
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _WireGuardEditDialog extends StatefulWidget {
  const _WireGuardEditDialog({
    required this.bridge,
    required this.config,
    required this.vpnRunning,
  });

  final CoreBridge bridge;
  final WireGuardInfo? config;
  final bool vpnRunning;

  @override
  State<_WireGuardEditDialog> createState() => _WireGuardEditDialogState();
}

class _WireGuardEditDialogState extends State<_WireGuardEditDialog> {
  late final tagController = TextEditingController(
    text: widget.config?.tag ?? '',
  );
  late final nameController = TextEditingController(
    text: widget.config?.name ?? '',
  );
  late final configController = TextEditingController(
    text: widget.config?.config ?? '',
  );
  String statusText = '';
  String statusKind = '';
  bool busy = false;
  bool parsedOk = false;

  bool get editing => widget.config != null;

  @override
  void dispose() {
    tagController.dispose();
    nameController.dispose();
    configController.dispose();
    super.dispose();
  }

  Future<void> _paste(TextEditingController controller) async {
    final data = await Clipboard.getData(Clipboard.kTextPlain);
    if (data?.text != null) {
      controller.text = data!.text!;
    }
  }

  Future<void> _parseAndSave() async {
    if (configController.text.trim().isEmpty) {
      setState(() {
        statusKind = 'error';
        statusText = 'Вставьте WireGuard конфиг.';
      });
      return;
    }
    setState(() {
      busy = true;
      statusKind = 'loading';
      statusText = 'Проверяем WireGuard конфиг...';
    });
    final result = await widget.bridge.parseWireGuard(configController.text);
    if (!mounted) {
      return;
    }
    setState(() {
      parsedOk = result['success'] == true;
      statusKind = parsedOk ? 'success' : 'error';
      statusText = parsedOk
          ? 'Конфиг корректен: ${result['endpoint'] ?? 'endpoint найден'}'
          : result['error']?.toString() ?? 'Конфиг не прошёл проверку';
    });
    if (!parsedOk) {
      setState(() => busy = false);
      return;
    }
    await _save();
  }

  Future<void> _save() async {
    setState(() {
      busy = true;
      statusKind = 'loading';
      statusText = 'Сохраняем WireGuard...';
    });
    final tag = tagController.text.trim();
    final name = nameController.text.trim();
    final configText = configController.text;
    final result = editing
        ? await widget.bridge.updateWireGuard(
            widget.config!.tag,
            tag,
            name,
            configText,
          )
        : await widget.bridge.addWireGuard(tag, name, configText);
    if (!mounted) {
      return;
    }
    if (result['success'] == false) {
      setState(() {
        busy = false;
        statusKind = 'error';
        statusText = result['error']?.toString() ?? 'Не удалось сохранить';
      });
      return;
    }
    Navigator.of(context).pop(true);
  }

  Future<void> _delete() async {
    if (!editing) {
      return;
    }
    final result = await widget.bridge.deleteWireGuard(widget.config!.tag);
    if (!mounted) {
      return;
    }
    if (result['success'] == false) {
      setState(() {
        statusKind = 'error';
        statusText = result['error']?.toString() ?? 'Не удалось удалить';
      });
      return;
    }
    Navigator.of(context).pop(true);
  }

  @override
  Widget build(BuildContext context) {
    return _AppDialog(
      title: editing ? 'Редактировать WireGuard' : 'Добавить WireGuard',
      icon: editing ? Icons.edit : Icons.add,
      width: 580,
      centered: true,
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          const Text(
            'Вставьте стандартный WireGuard конфиг или заполните поля вручную.',
            style: TextStyle(color: Color(0xFF8892B0), fontSize: 12),
          ),
          const SizedBox(height: 12),
          _LabeledField(
            label: 'Тег (латиница, без пробелов) *',
            controller: tagController,
            hint: 'dropo-internal',
            onPaste: () => _paste(tagController),
          ),
          const SizedBox(height: 10),
          _LabeledField(
            label: 'Название',
            controller: nameController,
            hint: 'dropo офис',
            onPaste: () => _paste(nameController),
          ),
          const SizedBox(height: 10),
          _LabeledField(
            label: 'WireGuard конфиг',
            controller: configController,
            hint:
                '[Interface]\nPrivateKey = ...\nAddress = ...\n\n[Peer]\nPublicKey = ...',
            minLines: 8,
            maxLines: 14,
            onPaste: () => _paste(configController),
          ),
          if (statusText.isNotEmpty) ...[
            const SizedBox(height: 12),
            _StatusBox(kind: statusKind, text: statusText),
          ],
          const SizedBox(height: 16),
          Row(
            children: [
              Expanded(
                child: _DialogAction(
                  label: 'Отмена',
                  icon: Icons.close,
                  onPressed: busy
                      ? null
                      : () => Navigator.of(context).pop(false),
                ),
              ),
              if (editing) ...[
                const SizedBox(width: 10),
                Expanded(
                  child: _DialogAction(
                    label: 'Удалить',
                    icon: Icons.delete,
                    danger: true,
                    onPressed: busy ? null : _delete,
                  ),
                ),
              ],
              const SizedBox(width: 10),
              Expanded(
                child: _DialogAction(
                  label: 'Проверить',
                  icon: Icons.fact_check,
                  primary: true,
                  onPressed: busy || widget.vpnRunning ? null : _parseAndSave,
                ),
              ),
            ],
          ),
        ],
      ),
    );
  }
}

class _DialogAction extends StatelessWidget {
  const _DialogAction({
    required this.label,
    required this.icon,
    required this.onPressed,
    this.primary = false,
    this.danger = false,
  });

  final String label;
  final IconData icon;
  final VoidCallback? onPressed;
  final bool primary;
  final bool danger;

  @override
  Widget build(BuildContext context) {
    return SizedBox(
      width: double.infinity,
      child: danger || primary
          ? _ActionButton(
              label: label,
              icon: icon,
              danger: danger,
              secondary: !primary && !danger,
              onPressed: onPressed,
            )
          : _ActionButton(
              label: label,
              icon: icon,
              secondary: true,
              onPressed: onPressed,
            ),
    );
  }
}

class _StatusBox extends StatelessWidget {
  const _StatusBox({required this.kind, required this.text});

  final String kind;
  final String text;

  @override
  Widget build(BuildContext context) {
    final color = switch (kind) {
      'success' => const Color(0xFF86EFAC),
      'error' => const Color(0xFFFCA5A5),
      'warning' => const Color(0xFFFCD34D),
      'loading' => const Color(0xFFFCD34D),
      _ => const Color(0xFFB7CAC5),
    };
    return Container(
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: color.withValues(alpha: 0.10),
        borderRadius: BorderRadius.circular(8),
        border: Border.all(color: color.withValues(alpha: 0.30)),
      ),
      child: SelectableText(
        text,
        style: TextStyle(color: color, fontSize: 12, height: 1.35),
      ),
    );
  }
}

class _StatsSectionTitle extends StatelessWidget {
  const _StatsSectionTitle(this.text);

  final String text;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.only(bottom: 8),
      child: Text(
        text.toUpperCase(),
        style: const TextStyle(
          color: Color(0xFF4A5568),
          fontSize: 10,
          fontWeight: FontWeight.w800,
          letterSpacing: 0.5,
        ),
      ),
    );
  }
}

class _StatsGrid extends StatelessWidget {
  const _StatsGrid({required this.children});

  final List<Widget> children;

  @override
  Widget build(BuildContext context) {
    return GridView.count(
      crossAxisCount: 2,
      mainAxisSpacing: 10,
      crossAxisSpacing: 10,
      childAspectRatio: 1.62,
      shrinkWrap: true,
      physics: const NeverScrollableScrollPhysics(),
      children: children,
    );
  }
}

class _StatCard extends StatelessWidget {
  const _StatCard({
    required this.icon,
    required this.value,
    required this.label,
    required this.color,
  });

  final IconData icon;
  final String value;
  final String label;
  final Color color;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.black.withValues(alpha: 0.22),
        borderRadius: BorderRadius.circular(14),
        border: Border.all(color: Colors.white.withValues(alpha: 0.06)),
      ),
      child: Column(
        mainAxisAlignment: MainAxisAlignment.center,
        children: [
          Icon(icon, color: color, size: 18),
          const SizedBox(height: 5),
          Text(
            value,
            maxLines: 1,
            overflow: TextOverflow.ellipsis,
            style: TextStyle(
              color: color,
              fontSize: 17,
              fontWeight: FontWeight.w900,
            ),
          ),
          const SizedBox(height: 2),
          Text(
            label,
            style: const TextStyle(color: Color(0xFF7F918C), fontSize: 10),
          ),
        ],
      ),
    );
  }
}

class _ChartPlaceholder extends StatelessWidget {
  const _ChartPlaceholder({
    required this.title,
    required this.upload,
    required this.download,
    this.compact = false,
  });

  final String title;
  final int upload;
  final int download;
  final bool compact;

  @override
  Widget build(BuildContext context) {
    final total = (upload + download).clamp(1, 1 << 62);
    final up = (upload / total).clamp(0.08, 1.0);
    final down = (download / total).clamp(0.08, 1.0);
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        color: Colors.black.withValues(alpha: 0.20),
        borderRadius: BorderRadius.circular(12),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            title,
            style: const TextStyle(
              color: Color(0xFF8EA2A0),
              fontSize: 11,
              fontWeight: FontWeight.w800,
            ),
          ),
          const SizedBox(height: 10),
          _MeterLine(color: const Color(0xFF36D399), factor: up),
          SizedBox(height: compact ? 5 : 8),
          _MeterLine(color: const Color(0xFF60A5FA), factor: down),
        ],
      ),
    );
  }
}

class _MeterLine extends StatelessWidget {
  const _MeterLine({required this.color, required this.factor});

  final Color color;
  final double factor;

  @override
  Widget build(BuildContext context) {
    return FractionallySizedBox(
      alignment: Alignment.centerLeft,
      widthFactor: factor,
      child: Container(
        height: 6,
        decoration: BoxDecoration(
          color: color.withValues(alpha: 0.8),
          borderRadius: BorderRadius.circular(999),
        ),
      ),
    );
  }
}

class _WireGuardTile extends StatelessWidget {
  const _WireGuardTile({required this.item, required this.onEdit});

  final WireGuardInfo item;
  final VoidCallback? onEdit;

  @override
  Widget build(BuildContext context) {
    final networks = item.allowedIps.take(3).join(', ');
    return MouseRegion(
      cursor: onEdit == null
          ? SystemMouseCursors.basic
          : SystemMouseCursors.click,
      child: GestureDetector(
        onTap: onEdit,
        child: Container(
          margin: const EdgeInsets.only(bottom: 8),
          padding: const EdgeInsets.all(12),
          decoration: BoxDecoration(
            color: Colors.white.withValues(alpha: 0.055),
            borderRadius: BorderRadius.circular(8),
            border: Border.all(color: Colors.white.withValues(alpha: 0.06)),
          ),
          child: Row(
            children: [
              const Icon(Icons.hub, color: Color(0xFFBAF7D0), size: 20),
              const SizedBox(width: 10),
              Expanded(
                child: Column(
                  crossAxisAlignment: CrossAxisAlignment.start,
                  children: [
                    Text(
                      item.name.isEmpty ? item.tag : item.name,
                      style: const TextStyle(
                        fontWeight: FontWeight.w800,
                        fontSize: 13,
                      ),
                    ),
                    const SizedBox(height: 2),
                    Text(
                      item.endpoint.isEmpty ? item.tag : item.endpoint,
                      maxLines: 1,
                      overflow: TextOverflow.ellipsis,
                      style: const TextStyle(
                        color: Color(0xFF8EA2A0),
                        fontSize: 10,
                      ),
                    ),
                    if (networks.isNotEmpty)
                      Text(
                        networks,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: const TextStyle(
                          color: Color(0xFF4A5568),
                          fontSize: 9,
                        ),
                      ),
                  ],
                ),
              ),
              IconButton(
                onPressed: onEdit,
                icon: const Icon(Icons.edit, size: 16),
                tooltip: 'Редактировать',
                mouseCursor: onEdit == null
                    ? SystemMouseCursors.basic
                    : SystemMouseCursors.click,
              ),
            ],
          ),
        ),
      ),
    );
  }
}

class _LabeledField extends StatelessWidget {
  const _LabeledField({
    required this.label,
    required this.controller,
    required this.hint,
    required this.onPaste,
    this.minLines = 1,
    this.maxLines = 1,
  });

  final String label;
  final TextEditingController controller;
  final String hint;
  final VoidCallback onPaste;
  final int minLines;
  final int maxLines;

  @override
  Widget build(BuildContext context) {
    return Column(
      crossAxisAlignment: CrossAxisAlignment.start,
      children: [
        Text(
          label,
          style: const TextStyle(color: Color(0xFF8892B0), fontSize: 11),
        ),
        const SizedBox(height: 4),
        TextField(
          controller: controller,
          minLines: minLines,
          maxLines: maxLines,
          decoration: _fieldDecoration(
            hint: hint,
            suffixIcon: IconButton(
              onPressed: onPaste,
              icon: const Icon(Icons.content_paste),
              tooltip: 'Вставить из буфера',
              mouseCursor: SystemMouseCursors.click,
            ),
          ),
        ),
      ],
    );
  }
}

class _EmptyState extends StatelessWidget {
  const _EmptyState({required this.text});

  final String text;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.symmetric(vertical: 24),
      alignment: Alignment.center,
      child: Text(
        text,
        style: const TextStyle(color: Color(0xFF4A5568), fontSize: 12),
      ),
    );
  }
}

class _FactRow extends StatelessWidget {
  const _FactRow({required this.label, required this.value});

  final String label;
  final String value;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 126,
            child: Text(
              label,
              style: const TextStyle(color: Color(0xFF8EA2A0)),
            ),
          ),
          Expanded(
            child: SelectableText(
              value,
              textAlign: TextAlign.right,
              style: const TextStyle(color: Color(0xFFE8F5F1)),
            ),
          ),
        ],
      ),
    );
  }
}

class _LinkFactRow extends StatelessWidget {
  const _LinkFactRow({
    required this.label,
    required this.value,
    required this.onPressed,
  });

  final String label;
  final String value;
  final Future<void> Function() onPressed;

  @override
  Widget build(BuildContext context) {
    return Padding(
      padding: const EdgeInsets.symmetric(vertical: 8),
      child: Row(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          SizedBox(
            width: 126,
            child: Text(
              label,
              style: const TextStyle(color: Color(0xFF8EA2A0)),
            ),
          ),
          Expanded(
            child: Align(
              alignment: Alignment.centerRight,
              child: TextButton.icon(
                onPressed: () => unawaited(onPressed()),
                icon: const Icon(Icons.open_in_new, size: 14),
                label: Text(value, overflow: TextOverflow.ellipsis),
                style: _withClickCursor(
                  TextButton.styleFrom(
                    foregroundColor: const Color(0xFFBAF7D0),
                    padding: EdgeInsets.zero,
                    minimumSize: const Size(0, 28),
                    tapTargetSize: MaterialTapTargetSize.shrinkWrap,
                  ),
                ),
              ),
            ),
          ),
        ],
      ),
    );
  }
}

class _TelegramAssetShot extends StatelessWidget {
  const _TelegramAssetShot({required this.title, required this.asset});

  final String title;
  final String asset;

  @override
  Widget build(BuildContext context) {
    return Container(
      padding: const EdgeInsets.all(10),
      decoration: BoxDecoration(
        color: const Color(0xFF0A1112),
        borderRadius: BorderRadius.circular(14),
        border: Border.all(color: Colors.white.withValues(alpha: 0.12)),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.stretch,
        children: [
          Text(
            title,
            textAlign: TextAlign.center,
            style: const TextStyle(
              color: Color(0xFFBAF7D0),
              fontSize: 11,
              fontWeight: FontWeight.w800,
            ),
          ),
          const SizedBox(height: 8),
          ClipRRect(
            borderRadius: BorderRadius.circular(10),
            child: Image.asset(asset, fit: BoxFit.cover),
          ),
        ],
      ),
    );
  }
}

InputDecoration _fieldDecoration({String? hint, Widget? suffixIcon}) {
  return InputDecoration(
    hintText: hint,
    suffixIcon: suffixIcon,
    filled: true,
    fillColor: Colors.black.withValues(alpha: 0.30),
    contentPadding: const EdgeInsets.symmetric(horizontal: 12, vertical: 12),
    border: OutlineInputBorder(
      borderRadius: BorderRadius.circular(8),
      borderSide: BorderSide(color: Colors.white.withValues(alpha: 0.10)),
    ),
    enabledBorder: OutlineInputBorder(
      borderRadius: BorderRadius.circular(8),
      borderSide: BorderSide(color: Colors.white.withValues(alpha: 0.10)),
    ),
    focusedBorder: OutlineInputBorder(
      borderRadius: BorderRadius.circular(8),
      borderSide: const BorderSide(color: Color(0xFF36D399)),
    ),
  );
}

String _routingModeDescription(String mode) {
  return switch (mode) {
    'except_russia' =>
      'Весь зарубежный трафик через VPN, российские сайты напрямую.',
    'all_traffic' =>
      'Весь трафик через VPN. Максимальная приватность, высокая нагрузка.',
    _ =>
      'Через VPN идут только заблокированные сайты. Минимальная нагрузка на VPN.',
  };
}

String _networkModeDescription(String mode) {
  return switch (mode) {
    'deep_windows' =>
      'WinDivert/zapret transparent engine без лишнего TUN там, где это возможно.',
    'compat_tun' =>
      'Классический sing-box TUN fallback для совместимости с подписками.',
    _ =>
      'Auto использует Deep Windows, если компоненты доступны, иначе Compatibility TUN.',
  };
}

Map<String, dynamic> _asMap(Object? value) {
  if (value is Map<String, dynamic>) {
    return value;
  }
  if (value is Map) {
    return value.map((key, val) => MapEntry(key.toString(), val));
  }
  return const {};
}

List<String> _asStringList(Object? value) {
  if (value is List) {
    return value.map((item) => item.toString()).toList(growable: false);
  }
  if (value is String && value.trim().isNotEmpty) {
    return value
        .split(',')
        .map((item) => item.trim())
        .where((item) => item.isNotEmpty)
        .toList(growable: false);
  }
  return const [];
}

int _asInt(Object? value) {
  if (value is int) {
    return value;
  }
  if (value is num) {
    return value.toInt();
  }
  return int.tryParse(value?.toString() ?? '') ?? 0;
}

String _cleanError(Object error) {
  final text = error.toString();
  return text
      .replaceFirst('HttpException: ', '')
      .replaceFirst('TimeoutException after ', 'Таймаут: ');
}

const List<RouteService> fallbackRoutes = [
  RouteService(
    tag: 'openai',
    name: 'ChatGPT / Claude / Copilot',
    method: 'VPN forced',
    requiresVpn: true,
  ),
  RouteService(
    tag: 'youtube',
    name: 'YouTube',
    method: 'Free bypass first',
    requiresVpn: false,
  ),
  RouteService(
    tag: 'discord',
    name: 'Discord',
    method: 'Free bypass first',
    requiresVpn: false,
  ),
  RouteService(
    tag: 'google',
    name: 'Google Search',
    method: 'Direct',
    requiresVpn: false,
  ),
  RouteService(
    tag: 'gosuslugi',
    name: 'Gosuslugi and RU services',
    method: 'Direct',
    requiresVpn: false,
  ),
];
