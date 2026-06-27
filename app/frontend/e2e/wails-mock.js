(() => {
  const options = window.__dropoMockOptions || {};
  const defaultDelayMs = Number(options.delayMs ?? 5);
  const connectionDelayMs = Number(options.connectionDelayMs ?? defaultDelayMs);
  const statusDelayMs = Number(options.statusDelayMs ?? defaultDelayMs);
  const routeSummaryDelayMs = Number(options.routeSummaryDelayMs ?? defaultDelayMs);
  const delay = (value, ms = defaultDelayMs) => new Promise((resolve) => setTimeout(() => resolve(value), ms));
  const quickCheckServiceNames = [
    'Yandex',
    'Yandex Mail',
    'VK',
    'Ozon',
    'Sber',
    'Gosuslugi',
    'Rutube',
    'Habr',
    'Google',
    'GitHub',
    'Wikipedia',
    'StackOverflow',
    'Discord',
    'Discord API',
    'Discord CDN',
    'YouTube',
    'YouTube API',
    'YouTube Images',
    'YouTube video',
    'Instagram',
    'Facebook',
    'X',
    'LinkedIn',
    'Spotify',
    'Twitch',
    'Telegram',
    'Signal',
    'WhatsApp Web',
    'WhatsApp CDN',
    'FaceTime',
    'Viber',
    'Snapchat',
    'TikTok',
    'ChatGPT',
    'OpenAI API',
    'Claude',
    'Claude API',
    'Copilot proxy',
    'Cursor API',
  ];

  const state = {
    running: false,
    connecting: false,
    appConfig: {
      success: true,
      appVersion: '2.0.0',
      singboxVersion: '1.13.0-alpha.27',
      buildHash: 'visual',
      buildTime: '2026-06-22 00:00:00',
      autoStart: false,
      notifications: true,
      checkUpdates: false,
      autoUpdateSub: true,
      enableLogging: true,
      theme: 'dark',
      language: 'ru',
      logLevel: 'trace',
      subUpdateInterval: 24,
    },
    subscription: {
      hasSubscription: Boolean(options.initialSubscription),
      url: options.initialSubscription || '',
      proxyCount: options.initialSubscription ? 3 : 0,
    },
    wireguards: [],
    profiles: [
      { id: 1, name: 'Мой', subscription: Boolean(options.initialSubscription), proxyCount: options.initialSubscription ? 3 : 0, wireguards: [] },
      { id: 2, name: 'Work', subscription: true, proxyCount: 3, wireguards: [] },
    ],
    activeProfile: 1,
    routingMode: 'blocked_only',
    networkMode: options.networkMode || 'auto',
    freeAccess: {
      enabled: true,
      reverse: false,
      disableFreeAccess: false,
      freeMethodsAllowed: true,
      services: [
        { tag: 'youtube', name: 'YouTube', enabled: true, selectedMethod: 'auto' },
        { tag: 'discord', name: 'Discord', enabled: true, selectedMethod: 'auto' },
        { tag: 'telegram', name: 'Telegram', enabled: true, selectedMethod: 'auto' },
        { tag: 'instagram', name: 'Instagram', enabled: false, selectedMethod: 'auto' },
        { tag: 'openai', name: 'AI services', enabled: true, requiresVpn: true, selectedMethod: 'vpn' },
      ],
    },
    hideRuTraffic: {
      enabled: false,
      proxyAddress: '',
    },
    templateContent: JSON.stringify({ log: { level: 'warn' }, outbounds: [] }, null, 2),
    logs: [
      '[INFO] dropo visual preflight started',
      '[INFO] free access ready',
      '[INFO] xhttp bridge available through Xray',
      '[21:06:43] VPN started successfully in Deep Windows mode',
      '[21:08:10] VPN stopped successfully',
    ],
    calls: [],
    events: {},
  };

  const record = (name, args) => {
    state.calls.push({ name, args: Array.from(args || []) });
  };
  const emit = (name, payload) => (state.events[name] || []).forEach((handler) => handler(payload));
  const ok = (extra = {}) => delay({ success: true, ...extra });
  const fail = (error) => delay({ success: false, error });

  const sampleWgConfig = {
    tag: 'work',
    name: 'Work network',
    private_key: 'private',
    public_key: 'public',
    preshared_key: 'psk',
    local_address: ['100.105.120.117/32'],
    allowed_ips: ['192.168.8.0/24'],
    endpoint: 'vpn.example.test:51820',
    dns: '192.168.8.17',
    mtu: 1280,
    persistent_keepalive: 25,
  };

  const trafficStats = () => ({
    success: true,
    current: {
      uploaded: 256 * 1024,
      downloaded: 1024 * 1024,
      uploadedStr: '256 KB',
      downloadedStr: '1 MB',
      durationStr: '2 min',
    },
    total: {
      uploadedStr: '3 MB',
      downloadedStr: '21 MB',
      durationStr: '34 min',
      sessions: 7,
    },
  });

  const methodForService = (tag, fallbackMethod, fallbackDelay) => {
    const service = state.freeAccess.services.find((item) => item.tag === tag);
    const selected = service?.selectedMethod || 'auto';
    const hasSubscription = state.subscription.hasSubscription;

    if (selected === 'direct') {
      return { method: 'Direct', outbound: 'direct', delay: fallbackDelay + 8, success: true };
    }
    if (selected === 'vpn') {
      return hasSubscription
        ? { method: 'VPN', outbound: 'auto-select', delay: Math.max(48, fallbackDelay + 20), success: true }
        : { method: 'VPN unavailable', outbound: 'missing-subscription', delay: 0, success: false };
    }
    if (selected && selected !== 'auto') {
      const label = selected.includes('zapret')
        ? 'Zapret winws desync'
        : selected.includes('spoof')
          ? 'SpoofDPI SOCKS5'
          : selected.includes('sni')
            ? 'ByeDPI SNI split'
            : 'ByeDPI auto';
      return { method: label, outbound: selected, delay: fallbackDelay, success: true };
    }
    if (state.freeAccess.disableFreeAccess) {
      return hasSubscription
        ? { method: 'VPN', outbound: 'auto-select', delay: Math.max(50, fallbackDelay + 18), success: true }
        : { method: 'Direct', outbound: 'direct', delay: fallbackDelay + 15, success: true };
    }
    return { method: fallbackMethod, outbound: fallbackMethod.includes('Zapret') ? 'zapret-winws-desync' : 'byedpi', delay: fallbackDelay, success: true };
  };

  const routeSummaryServices = () => {
    const hasSubscription = state.subscription.hasSubscription;
    const services = [
      { tag: 'telegram', name: 'Telegram', fallbackMethod: 'Zapret winws desync', fallbackDelay: 64 },
      { tag: 'youtube', name: 'YouTube', fallbackMethod: 'Zapret winws desync', fallbackDelay: 62 },
      { tag: 'discord', name: 'Discord', fallbackMethod: 'ByeDPI SNI split', fallbackDelay: 71 },
    ].map((service) => ({
      tag: service.tag,
      name: service.name,
      ...methodForService(service.tag, service.fallbackMethod, service.fallbackDelay),
    }));

    services.push({
      tag: 'openai',
      name: 'AI services',
      method: hasSubscription ? 'VPN' : 'Direct',
      outbound: hasSubscription ? 'auto-select' : 'direct',
      delay: hasSubscription ? 96 : 118,
      success: true,
    });

    return services;
  };

  const networkModeStatus = () => {
    const requested = state.networkMode || 'compat_tun';
    const active = options.networkModeActive || (options.deepEngineMissing ? 'compat_tun' : 'deep_windows');
    const fallback = Boolean(options.networkModeFallback ?? (active !== 'deep_windows'));
    const labels = {
      auto: 'Auto',
      deep_windows: 'Deep Windows',
      compat_tun: 'Compatibility TUN',
    };
    return {
      requested,
      active,
      fallback,
      fallbackReason: fallback ? 'Deep Windows engine is unavailable: missing winws.exe or WinDivert; active mode is Compatibility TUN' : '',
      label: labels[active] || active,
      description: active === 'compat_tun'
        ? 'Compatibility TUN uses sing-box TUN when the Windows engine is unavailable.'
        : 'Deep Windows uses zapret/winws + WinDivert for transparent Windows traffic handling.',
      driverReady: active === 'deep_windows',
      helperPath: 'bin/dropo-netd.exe',
    };
  };

  const methods = {
    GetStatus: () => delay({
      success: true,
      running: state.running,
      connecting: state.connecting,
      singboxExists: true,
      hasError: false,
      networkMode: networkModeStatus().active,
      requestedNetworkMode: networkModeStatus().requested,
      networkModeFallback: networkModeStatus().fallback,
      networkModeFallbackReason: networkModeStatus().fallbackReason,
      networkModeLabel: networkModeStatus().label,
      networkModeDescription: networkModeStatus().description,
    }, statusDelayMs),
    Toggle: () => {
      state.connecting = true;
      return new Promise((resolve) => {
        setTimeout(() => {
          state.running = !state.running;
          state.connecting = false;
          resolve({ success: true, running: state.running });
        }, connectionDelayMs);
      });
    },
    Start: () => {
      state.connecting = true;
      return new Promise((resolve) => {
        setTimeout(() => {
          state.running = true;
          state.connecting = false;
          resolve({ success: true, running: true });
        }, connectionDelayMs);
      });
    },
    Stop: () => {
      state.connecting = true;
      return new Promise((resolve) => {
        setTimeout(() => {
          state.running = false;
          state.connecting = false;
          resolve({ success: true, running: false });
        }, connectionDelayMs);
      });
    },
    GetCurrentSubscription: () => ok({ ...state.subscription }),
    TestVPNConnection: (url) => {
      if (!url || !/^https?:\/\/|^vless:\/\//.test(url)) return fail('Invalid subscription URL');
      return ok({
        count: 3,
        warning: '',
        proxies: [
          { type: 'vless', name: 'xhttp-test', server: 'cdn.dropo.test' },
          { type: 'trojan', name: 'fallback-test', server: 'fallback.dropo.test' },
          { type: 'ss', name: 'shadowsocks-test', server: 'ss.dropo.test' },
        ],
      });
    },
    SetVPNSubscription: (url) => {
      state.subscription = { hasSubscription: true, url, proxyCount: 3 };
      state.profiles.find((p) => p.id === state.activeProfile).subscription = true;
      state.profiles.find((p) => p.id === state.activeProfile).proxyCount = 3;
      return ok({ proxyCount: 3 });
    },
    RemoveVPNSubscription: () => {
      state.subscription = { hasSubscription: false, url: '', proxyCount: 0 };
      return ok();
    },
    TestAllProxiesDelay: () => ok({
      currentProxy: 'xhttp-test',
      proxies: [
        { name: 'xhttp-test', delay: 91 },
        { name: 'trojan-fallback', delay: 210 },
        { name: 'Telegram (VPN)', delay: 84, type: 'FreeAccess' },
        { name: 'YouTube (ByeDPI auto)', delay: 62, type: 'FreeAccess' },
        { name: 'wireguard-work', delay: -1, isInternal: true },
        { name: 'timeout-node', delay: 0 },
      ],
    }),
    GetBypassRouteSummary: () => {
      const catchAllUsesVPN = state.subscription.hasSubscription && ['except_russia', 'all_traffic'].includes(state.routingMode);
      return delay({
        success: true,
        running: state.running,
        mode: state.routingMode,
        foreignVpn: state.routingMode === 'except_russia',
        ruVpn: state.hideRuTraffic.enabled || state.routingMode === 'all_traffic',
        services: routeSummaryServices(),
        catchAll: {
          method: catchAllUsesVPN ? 'VPN' : 'Direct',
          outbound: catchAllUsesVPN ? 'auto-select' : 'direct',
          delay: catchAllUsesVPN ? 90 : 24,
        },
      }, routeSummaryDelayMs);
    },
    GetWireGuardList: () => ok({ configs: state.wireguards }),
    GetWireGuardConfig: (tag) => {
      const wg = state.wireguards.find((item) => item.tag === tag) || sampleWgConfig;
      return ok(wg);
    },
    ParseWireGuardConfigAPI: (config) => {
      if (!config || !config.includes('[Interface]') || !config.includes('[Peer]')) {
        return fail('Invalid WireGuard config');
      }
      return ok({ endpoint: 'vpn.example.test:51820' });
    },
    AddWireGuard: (tag, name, config) => {
      if (!tag || !config) return fail('Missing WireGuard fields');
      state.wireguards.push({
        ...sampleWgConfig,
        tag,
        name,
      });
      return ok();
    },
    UpdateWireGuard: (oldTag, tag, name) => {
      const wg = state.wireguards.find((item) => item.tag === oldTag);
      if (wg) {
        wg.tag = tag;
        wg.name = name;
      }
      return ok();
    },
    DeleteWireGuard: (tag) => {
      state.wireguards = state.wireguards.filter((item) => item.tag !== tag);
      return ok();
    },
    GetAppConfig: () => ok({ ...state.appConfig, networkMode: state.networkMode, networkModeStatus: networkModeStatus() }),
    SaveAppConfig: (autoStart, enableLogging, checkUpdates, notifications, autoUpdateSub, theme, language, logLevel, subUpdateInterval) => {
      Object.assign(state.appConfig, {
        autoStart,
        enableLogging,
        checkUpdates,
        notifications,
        autoUpdateSub,
        theme,
        language,
        logLevel,
        subUpdateInterval,
      });
      return ok();
    },
    GetVersion: () => delay('2.0.0'),
    GetAppVersion: () => ok({
      version: '2.0.0',
      fullVersion: '2.0.0+visual',
      singboxVersion: state.appConfig.singboxVersion,
      buildTime: state.appConfig.buildTime,
      buildHash: state.appConfig.buildHash,
      githubRepo: 'Droponevedimka/dropo',
      githubURL: 'https://github.com/Droponevedimka/dropo',
      telegramName: 't.me/droponevedimka555',
      telegramURL: 'https://t.me/droponevedimka555',
    }),
    GetRoutingMode: () => ok({ mode: state.routingMode }),
    SetRoutingMode: (mode) => {
      state.routingMode = mode;
      return ok({ mode });
    },
    GetNetworkMode: () => ok({
      status: networkModeStatus(),
      modes: [
        { value: 'auto', label: 'Auto', description: 'Auto selects Deep Windows when winws/WinDivert are available.' },
        { value: 'deep_windows', label: 'Deep Windows', description: 'zapret/winws + WinDivert engine.' },
        { value: 'compat_tun', label: 'Compatibility TUN', description: 'sing-box TUN fallback mode.' },
      ],
    }),
    SetNetworkMode: (mode) => {
      state.networkMode = mode;
      return ok({ status: networkModeStatus() });
    },
    GetFreeAccessConfig: () => ok({
      ...state.freeAccess,
      methodCache: {
        exists: true,
        fresh: true,
        running: false,
        updatedAt: new Date(Date.now() - 60_000).toISOString(),
        serviceCount: state.freeAccess.services.length,
        successCount: state.freeAccess.services.length,
        durationMs: 320,
      },
      methodOptions: [
        { value: 'auto', label: 'Автоматически' },
        { value: 'direct', label: 'Direct' },
        { value: 'vpn', label: 'VPN подписка' },
        { value: 'byedpi', label: 'ByeDPI auto' },
        { value: 'byedpi-sni', label: 'ByeDPI SNI split' },
        { value: 'spoofdpi-socks', label: 'SpoofDPI SOCKS5' },
        { value: 'zapret-winws-desync', label: 'Zapret winws desync' },
      ],
      services: state.freeAccess.services.map((svc) => ({ ...svc })),
    }),
    SetFreeAccessServiceMethod: (tag, method) => {
      const service = state.freeAccess.services.find((svc) => svc.tag === tag);
      if (service) service.selectedMethod = method || 'auto';
      return ok({ tag, method: method || 'auto' });
    },
    TestRouteMethods: () => {
      const services = [
        { tag: 'discord', name: 'Discord', methodLabel: 'ByeDPI SNI split', latencyMs: 71, success: true },
        { tag: 'youtube', name: 'YouTube', methodLabel: 'ByeDPI auto', latencyMs: 62, success: true },
        { tag: 'openai', name: 'AI services', methodLabel: 'VPN', latencyMs: 96, success: true },
      ];
      emit('route-probe-start', {
        serviceCount: services.length,
        freeMethodCount: 5,
        transparentMethodCount: 2,
        vpnCandidateCount: 1,
        services: services.map((svc) => ({ tag: svc.tag, name: svc.name, requiresVpn: svc.tag === 'openai' })),
      });
      emit('route-probe-log', { message: 'Manual route method test started' });
      services.forEach((svc) => emit('route-probe-service', svc));
      emit('route-probe-complete', { durationMs: 320, services });
      return ok({ durationMs: 320, services });
    },
    RefreshFreeAccessMethods: () => ok({
      durationMs: 320,
      successCount: 3,
      cache: {
        exists: true,
        fresh: true,
        running: false,
        updatedAt: new Date().toISOString(),
        serviceCount: 3,
        successCount: 3,
        durationMs: 320,
      },
      services: [
        { tag: 'discord', name: 'Discord', methodLabel: 'ByeDPI SNI split', latencyMs: 71, success: true },
        { tag: 'youtube', name: 'YouTube', methodLabel: 'ByeDPI auto', latencyMs: 62, success: true },
        { tag: 'openai', name: 'AI services', methodLabel: 'VPN', latencyMs: 96, success: true },
      ],
    }),
    RunClientQuickCheck: () => {
      const services = quickCheckServiceNames.map((name, index) => ({
        index,
        name,
        statusText: 'OK',
        success: true,
        normalTimeMs: 18 + (index % 9) * 17,
        proxyTimeMs: state.subscription.hasSubscription ? 80 + (index % 7) * 23 : 0,
      }));
      emit('client-check-start', { total: services.length, proxyUrl: 'http://127.0.0.1:2088' });
      return new Promise((resolve) => {
        services.forEach((service, index) => {
          setTimeout(() => {
            emit('client-check-progress', service);
            emit('client-check-service', service);
            if (index === services.length - 1) {
              const output = [
                'dropo client quick check',
                'Mixed proxy: http://127.0.0.1:2088',
                ...services.map((item) => `Testing ${item.name.padEnd(18)} ${item.statusText}`),
                '',
                'Summary',
                `  Duration: 1 s`,
                `  Failed: 0/${services.length}`,
                `  Normal failed: 0/${services.length}`,
                '  Blocked failed:0',
              ].join('\n');
              const payload = {
                success: true,
                durationMs: 1000,
                total: services.length,
                completed: services.length,
                failedCount: 0,
                proxyRescued: 0,
                output,
                services,
              };
              emit('client-check-done', payload);
              resolve({ success: true, ...payload });
            }
          }, 40 + index * 60);
        });
      });
    },
    SetDisableFreeAccess: (disabled) => {
      state.freeAccess.disableFreeAccess = disabled;
      state.freeAccess.enabled = !disabled;
      state.freeAccess.freeMethodsAllowed = !disabled;
      return ok();
    },
    SetFreeAccessEnabled: (enabled) => {
      state.freeAccess.enabled = enabled;
      state.freeAccess.disableFreeAccess = !enabled;
      state.freeAccess.freeMethodsAllowed = enabled;
      return ok();
    },
    SetFreeAccessReverse: (reverse) => {
      state.freeAccess.reverse = reverse;
      return ok();
    },
    ToggleFreeAccessService: (tag, enabled) => {
      const service = state.freeAccess.services.find((svc) => svc.tag === tag);
      if (service) service.enabled = enabled;
      return ok();
    },
    GetHideRuTraffic: () => ok({ ...state.hideRuTraffic }),
    SetHideRuTraffic: (enabled, proxyAddress) => {
      state.hideRuTraffic = { enabled, proxyAddress };
      return ok();
    },
    GetFiltersInfo: () => ok({ version: '2026.06.22', filter_count: 6, total_size_kb: 512, is_outdated: false }),
    UpdateFilters: () => ok({ success: false, started: false, error: 'Filters are updated at build time' }),
    ExportProfilesToFile: () => ok({ profiles_count: state.profiles.length }),
    ImportProfilesFromFile: () => ok({
      needs_confirmation: true,
      profiles_count: 2,
      wireguard_count: 1,
      has_template: true,
      profile_names: ['Imported A', 'Imported B'],
      file_data: '{"profiles":[]}',
    }),
    ConfirmImportProfiles: () => ok({ profiles_count: 2 }),
    GetTemplateContent: () => ok({ content: state.templateContent }),
    SaveTemplateContent: (content) => {
      state.templateContent = content;
      return ok();
    },
    ResetTemplate: () => {
      state.templateContent = JSON.stringify({ outbounds: [] }, null, 2);
      return ok();
    },
    UpdateTrafficFromClash: () => ok(),
    GetTrafficStats: () => delay(trafficStats()),
    ResetTrafficStats: () => ok(),
    GetLogs: () => ok({ logs: state.logs.slice() }),
    ClearLogs: () => {
      state.logs = [];
      return ok();
    },
    CheckForUpdates: () => ok({
      hasUpdate: true,
      currentVersion: '2.0.0',
      latestVersion: '2.0.1',
      fileSize: 44 * 1024 * 1024,
      publishedAt: '2026-06-22T12:00:00Z',
      releaseURL: 'https://example.test/dropo/releases/2.0.1',
      downloadURL: 'https://example.test/dropo.zip',
      releaseNotes: 'Visual preflight update',
    }),
    DownloadAndInstallUpdate: () => ok({ message: 'Downloaded' }),
    OpenConfigFolder: () => ok(),
    OpenLogs: () => ok(),
    SetWindowVisible: () => ok(),
    QuitApp: () => ok(),
    AddToLogBuffer: () => ok(),
    GetProfiles: () => ok({ profiles: state.profiles.map((profile) => ({ ...profile })), activeProfile: state.activeProfile }),
    SetActiveProfile: (id) => {
      state.activeProfile = id;
      return ok();
    },
    CreateProfile: (name) => {
      const nextId = Math.max(...state.profiles.map((p) => p.id)) + 1;
      state.profiles.push({ id: nextId, name, subscription: false, proxyCount: 0, wireguards: [] });
      return ok({ id: nextId });
    },
    UpdateProfile: (id, name) => {
      const profile = state.profiles.find((item) => item.id === id);
      if (profile) profile.name = name;
      return ok();
    },
    DeleteProfile: (id) => {
      state.profiles = state.profiles.filter((item) => item.id !== id);
      if (state.activeProfile === id) state.activeProfile = 1;
      return ok();
    },
  };

  const app = new Proxy(methods, {
    get(target, prop) {
      if (prop in target) {
        return (...args) => {
          record(prop, args);
          return target[prop](...args);
        };
      }
      return (...args) => {
        record(prop, args);
        return ok();
      };
    },
  });

  window.go = { main: { App: app } };
  window.runtime = {
    EventsOn(name, handler) {
      if (!state.events[name]) state.events[name] = [];
      state.events[name].push(handler);
      return () => {};
    },
    WindowStartDrag() {},
    WindowMinimise() {},
    WindowMinimize() {},
    Quit() {},
  };
  window.__dropoMock = state;
  window.confirm = () => true;

  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: {
      writeText: (text) => {
        state.lastClipboardText = text;
        return Promise.resolve();
      },
      readText: () => Promise.resolve(state.lastClipboardText || ''),
    },
  });
})();
