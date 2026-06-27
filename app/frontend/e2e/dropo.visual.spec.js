const { expect, test } = require('@playwright/test');
const path = require('node:path');

const mockPath = path.join(__dirname, 'wails-mock.js');

const VIEWPORTS = [
  { name: 'desktop', width: 1366, height: 768 },
  { name: 'tablet', width: 1024, height: 700 },
  { name: 'mobile', width: 390, height: 844 },
];

async function boot(page, viewport = VIEWPORTS[0], options = {}) {
  await page.setViewportSize({ width: viewport.width, height: viewport.height });
  await page.route('https://cdn.jsdelivr.net/**', (route) => {
    route.fulfill({
      contentType: 'application/javascript',
      body: 'window.jsQR = function(){ return null; };',
    });
  });
  await page.addInitScript((opts) => {
    window.__dropoMockOptions = opts.mockOptions || {};
    if (opts.busyTimeoutMs) window.__dropoBusyTimeoutMs = opts.busyTimeoutMs;
    if (opts.initCallTimeoutMs) window.__dropoInitCallTimeoutMs = opts.initCallTimeoutMs;
  }, options);
  await page.addInitScript({ path: mockPath });
  await page.goto('/');
  await page.locator('#powerBtn').waitFor({ state: 'visible' });
  await expect(page.locator('h1')).toHaveCount(0);
  await expect(page.locator('#profileLogo .brand-dr')).toHaveText('Dr');
  await expect(page.locator('#profileLogo .brand-opo')).toHaveText('opo');
  await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
}

async function expectNoPageBreaks(page) {
  const result = await page.evaluate(() => {
    const doc = document.documentElement;
    const visibleControls = Array.from(document.querySelectorAll('button, .badge, input, select, textarea'))
      .filter((el) => {
        const overlay = el.closest('.modal-overlay');
        if (overlay && !overlay.classList.contains('active')) return false;
        const style = getComputedStyle(el);
        const rect = el.getBoundingClientRect();
        return style.visibility !== 'hidden' && style.display !== 'none' && rect.width > 0 && rect.height > 0;
      })
      .map((el) => {
        const rect = el.getBoundingClientRect();
        return {
          tag: el.tagName,
          id: el.id,
          className: String(el.className || ''),
          left: rect.left,
          right: rect.right,
          top: rect.top,
          bottom: rect.bottom,
          width: rect.width,
          height: rect.height,
        };
      });

    const outside = visibleControls.filter((item) =>
      item.right < -2 ||
      item.left > window.innerWidth + 2 ||
      item.width < 1 ||
      item.height < 1
    );

    return {
      horizontalOverflow: doc.scrollWidth > doc.clientWidth + 1,
      outside,
    };
  });

  expect(result.horizontalOverflow, 'page must not create horizontal overflow').toBe(false);
  expect(result.outside, 'visible controls must stay in the viewport').toEqual([]);
}

async function expectDrawer(page, id) {
  const overlay = page.locator(`#${id}`);
  const modal = overlay.locator('.modal').first();
  await expect(overlay).toHaveClass(/active/);
  await expect(modal).toBeVisible();
  await page.waitForTimeout(380);

  const box = await modal.boundingBox();
  const viewport = page.viewportSize();
  expect(box, `${id} drawer must have a bounding box`).toBeTruthy();
  expect(box.x, `${id} drawer starts from the left edge`).toBeLessThanOrEqual(1);
  expect(box.width, `${id} drawer width is capped at 90vw`).toBeLessThanOrEqual(viewport.width * 0.9 + 3);
  expect(box.height, `${id} drawer fills the viewport height`).toBeGreaterThanOrEqual(viewport.height - 2);

  const radius = await modal.evaluate((el) => ({
    topLeft: getComputedStyle(el).borderTopLeftRadius,
    topRight: getComputedStyle(el).borderTopRightRadius,
  }));
  expect(radius.topLeft, `${id} drawer should not look like a centered modal`).toBe('0px');
  expect(parseFloat(radius.topRight), `${id} drawer has a right-side radius`).toBeGreaterThan(0);
}

async function closeDrawer(page, id) {
  await page.keyboard.press('Escape');
  await expect(page.locator(`#${id}`)).not.toHaveClass(/active/);
}

async function saveScreenshot(page, testInfo, name) {
  await page.screenshot({
    path: testInfo.outputPath(`${name}.png`),
    fullPage: true,
  });
}

test.describe('dropo visual preflight', () => {
  test.afterEach(async ({ page }, testInfo) => {
    const errors = await page.evaluate(() => window.__dropoVisualErrors || []);
    expect(errors, `browser errors in ${testInfo.title}`).toEqual([]);
  });

  test.beforeEach(async ({ page }) => {
    await page.addInitScript(() => {
      window.__dropoVisualErrors = [];
      window.addEventListener('error', (event) => {
        window.__dropoVisualErrors.push(event.message || String(event.error));
      });
      window.addEventListener('unhandledrejection', (event) => {
        window.__dropoVisualErrors.push(String(event.reason && event.reason.message ? event.reason.message : event.reason));
      });
    });
  });

  for (const viewport of VIEWPORTS) {
    test(`landing layout is stable on ${viewport.name}`, async ({ page }, testInfo) => {
      await boot(page, viewport);
      await expect(page.locator('#status')).toBeVisible();
      await expect(page.locator('#vpnBadge')).toBeVisible();
      await expect(page.locator('#wgBadge')).toBeVisible();
      await expect(page.locator('#serviceCheckBtn')).toBeVisible();
      await expect(page.locator('#exitAppBtn')).toBeVisible();
      await expect(page.locator('.footer')).toBeVisible();
      await expectNoPageBreaks(page);
      await saveScreenshot(page, testInfo, `landing-${viewport.name}`);
    });
  }

  test('all main sections open as left drawers and close with Escape', async ({ page }, testInfo) => {
    await boot(page);

    const sections = [
      { trigger: '#vpnBadge', id: 'vpnModal', shot: 'drawer-vpn' },
      { trigger: '#wgBadge', id: 'wgListModal', shot: 'drawer-wireguard' },
      { trigger: '#settingsBtn', id: 'settingsModal', shot: 'drawer-settings' },
      { trigger: 'button[onclick="openStats()"]', id: 'statsModal', shot: 'drawer-stats' },
      { trigger: 'button[onclick="openLogsModal()"]', id: 'logsModal', shot: 'drawer-logs' },
      { trigger: 'button[onclick="openAbout()"]', id: 'aboutModal', shot: 'drawer-about' },
      { trigger: '#profileLogo', id: 'profilesModal', shot: 'drawer-profiles' },
    ];

    for (const section of sections) {
      await page.locator(section.trigger).click({ force: section.trigger === '#profileLogo' });
      await expectDrawer(page, section.id);
      await expectNoPageBreaks(page);
      await saveScreenshot(page, testInfo, section.shot);
      await closeDrawer(page, section.id);
    }
  });

  test('startup loader cannot permanently block main action clicks', async ({ page }) => {
    await boot(page, VIEWPORTS[0], {
      mockOptions: { delayMs: 350 },
      busyTimeoutMs: 150,
      initCallTimeoutMs: 80,
    });

    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);

    await page.locator('button[onclick="openStats()"]').click();
    await expectDrawer(page, 'statsModal');
    await closeDrawer(page, 'statsModal');

    await page.locator('#settingsBtn').click();
    await expectDrawer(page, 'settingsModal');
    await closeDrawer(page, 'settingsModal');

    await page.locator('#profileLogo').click({ force: true });
    await expectDrawer(page, 'profilesModal');
  });

  test('slow status refreshes are serialized instead of stacking', async ({ page }) => {
    await boot(page, VIEWPORTS[0], {
      mockOptions: { statusDelayMs: 500 },
    });
    await page.waitForTimeout(1200);

    await page.evaluate(() => {
      window.__dropoMock.calls = [];
      window.safeUpdateStatus('manual slow status', 100);
      window.safeUpdateStatus('manual duplicate status', 100);
      window.safeUpdateStatus('manual duplicate status 2', 100);
    });

    await page.waitForTimeout(160);
    let calls = await page.evaluate(() => window.__dropoMock.calls.filter((call) => call.name === 'GetStatus').length);
    expect(calls).toBe(1);

    await page.waitForTimeout(450);
    await page.evaluate(() => window.safeUpdateStatus('manual status after settle', 100));
    await page.waitForTimeout(30);
    calls = await page.evaluate(() => window.__dropoMock.calls.filter((call) => call.name === 'GetStatus').length);
    expect(calls).toBeGreaterThanOrEqual(2);
    expect(calls).toBeLessThanOrEqual(3);
  });

  test('power button becomes an inline connection loader with helpful status', async ({ page }) => {
    await boot(page, VIEWPORTS[0], {
      mockOptions: { connectionDelayMs: 6000 },
    });

    await page.locator('#powerBtn').click();
    await expect(page.locator('#powerBtn')).toHaveClass(/connecting/);
    await expect(page.locator('#powerBtn')).toHaveAttribute('aria-busy', 'true');
    await expect(page.locator('#connectionHint')).toHaveClass(/visible/);
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', { id: 'backend-connect', active: true, message: 'Р—Р°РїСѓСЃРєР°РµРј Xray bridge...' });
    });
    await expect(page.locator('#connectionHintMessage')).toContainText('Xray bridge');
    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', { id: 'backend-connect', active: false });
    });

    await expect(page.locator('#powerBtn')).not.toHaveClass(/connecting/, { timeout: 9000 });
    await expect(page.locator('#connectionHint')).not.toHaveClass(/visible/);
    await expect(page.locator('#status')).toContainText('Подключено');
  });

  test('disconnect shows cleanup progress and does not leave loader stuck', async ({ page }) => {
    await boot(page, VIEWPORTS[0], {
      mockOptions: { connectionDelayMs: 3000 },
    });

    await page.evaluate(async () => {
      window.__dropoMock.running = true;
      await window.updateStatus();
    });
    await expect(page.locator('#status')).toContainText('Подключено');

    await page.locator('#powerBtn').click();
    await expect(page.locator('#powerBtn')).toHaveClass(/connecting/);
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
    await expect(page.locator('#connectionHint')).toHaveClass(/visible/);
    await expect(page.locator('#connectionHintTitle')).toContainText('Отключаем VPN');
    await expect(page.locator('#connectionHintMessage')).toContainText('фоновые');

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', {
        id: 'backend-disconnect',
        active: true,
        message: 'Cleanup: checking and closing remaining dropo background processes...',
      });
    });
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
    await expect(page.locator('#connectionHintMessage')).toContainText('remaining dropo background processes');

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', {
        id: 'backend-disconnect',
        active: true,
        message: 'VPN РѕС‚РєР»СЋС‡РµРЅ',
      });
    });
    await expect(page.locator('#connectionHintMessage')).not.toContainText('VPN РѕС‚РєР»СЋС‡РµРЅ');
    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', { id: 'backend-disconnect', active: false });
    });

    await expect(page.locator('#powerBtn')).not.toHaveClass(/connecting/, { timeout: 10000 });
    await expect(page.locator('#connectionHint')).not.toHaveClass(/visible/);
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
    await expect(page.locator('#status')).toContainText('Отключено');
  });

  test('about and footer use one version source and public project links', async ({ page }) => {
    await boot(page);

    await expect(page.locator('#versionBadge')).toHaveText('v2.0.0+visual');
    await page.locator('button[onclick="openAbout()"]').click();
    await expectDrawer(page, 'aboutModal');
    await expect(page.locator('#aboutVersion')).toHaveText('v2.0.0+visual');
    await expect(page.locator('#aboutSingboxVersion')).toHaveText('v1.13.0-alpha.27');
    await expect(page.locator('#aboutGithubLink')).toHaveText('Droponevedimka/dropo');
    await expect(page.locator('#aboutGithubLink')).toHaveAttribute('href', 'https://github.com/Droponevedimka/dropo');
    await expect(page.locator('#aboutTelegramLink')).toHaveText('t.me/droponevedimka555');
    await expect(page.locator('#aboutTelegramLink')).toHaveAttribute('href', 'https://t.me/droponevedimka555');
    await expect(page.locator('#aboutModal')).not.toContainText('dr.oponevedimkga@gmail.com');
  });

  test('network mode shows fallback banner and bottom indicator', async ({ page }) => {
    await boot(page, VIEWPORTS[0], {
      mockOptions: { networkMode: 'deep_windows', networkModeActive: 'compat_tun', networkModeFallback: true },
    });

    await expect(page.locator('#networkModePill')).toContainText('Compatibility TUN');
    await expect(page.locator('#networkModePill')).toHaveClass(/fallback/);
    await expect(page.locator('#networkModeBanner')).toHaveClass(/show/);
    await expect(page.locator('#networkModeBannerText')).toContainText('Compatibility TUN');

    await page.locator('#networkModeBanner .close').click();
    await expect(page.locator('#networkModeBanner')).not.toHaveClass(/show/);
  });

  test('duplicate VPN status events show one notification', async ({ page }) => {
    await boot(page);
    await page.evaluate(() => document.querySelectorAll('.toast').forEach((toast) => toast.remove()));

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('vpn-status-changed', true);
      emit('vpn-status-changed', true);
    });

    await expect(page.locator('.toast')).toHaveCount(1);
    await expect(page.locator('.toast .message')).toContainText('VPN');
  });

  test('settings autosave every switch and parameter without a save button', async ({ page }) => {
    await boot(page);
    await page.locator('#settingsBtn').click();
    await expectDrawer(page, 'settingsModal');

    const settingsButtons = await page.locator('#settingsModal button').allTextContents();
    expect(settingsButtons.join(' ')).not.toMatch(/Save|РЎРѕС…СЂР°РЅРёС‚СЊ/i);

    for (const selector of [
      '#settingAutoStart',
      '#settingNotifications',
      '#settingEnableLogging',
      '#settingAutoUpdateSub',
      '#settingCheckUpdates',
    ]) {
      await page.locator(`${selector} + .slider`).click();
    }

    await page.locator('#logLevelSelect').selectOption('debug');
    await page.locator('#settingTheme').selectOption('light');
    await page.locator('#settingLanguage').selectOption('en');
    await page.locator('#settingRoutingMode').selectOption('except_russia');
    await page.locator('#settingNetworkMode').selectOption('deep_windows');
    await page.locator('#settingDisableFreeAccess + .slider').click();
    await page.locator('#settingHideRuTraffic + .slider').click();
    await page.locator('#settingRuProxyAddress').fill('socks://127.0.0.1:1080');
    await page.locator('#settingRuProxyAddress').blur();
    await page.locator('#refreshFreeMethodsBtn').click();
    await expect(page.locator('#routeProbeModal')).not.toHaveClass(/active/);
    await expect(page.locator('#freeMethodsCacheInfo')).toContainText(/\d+\/\d+/);

    await page.locator('#openBlockedServicesBtn').click();
    await expectDrawer(page, 'blockedServicesModal');
    await page.locator('.blocked-service-item[data-service-tag="telegram"] .blocked-method-trigger').click();
    await page.locator('.blocked-service-item[data-service-tag="telegram"] .blocked-method-option', { hasText: 'Direct' }).click();
    await closeDrawer(page, 'blockedServicesModal');

    await expect(page.locator('.toast').last()).toBeVisible();
    await page.waitForTimeout(450);

    const calls = await page.evaluate(() => window.__dropoMock.calls.map((call) => call.name));
    expect(calls).toContain('SaveAppConfig');
    expect(calls).toContain('SetRoutingMode');
    expect(calls).toContain('SetNetworkMode');
    expect(calls).toContain('SetDisableFreeAccess');
    expect(calls).toContain('RefreshFreeAccessMethods');
    expect(calls).not.toContain('SetFreeAccessEnabled');
    expect(calls).not.toContain('SetFreeAccessReverse');
    expect(calls).not.toContain('ToggleFreeAccessService');
    expect(calls).toContain('SetFreeAccessServiceMethod');
    expect(calls).toContain('SetHideRuTraffic');
  });

  test('connected home shows route ping list without legacy Test button', async ({ page }) => {
    await boot(page);

    await expect(page.locator('#testRoutesBtn')).toHaveCount(0);

    await page.locator('#powerBtn').click();
    await page.waitForTimeout(2300);
    await expect(page.locator('#routeIndicator')).not.toHaveClass(/visible/);
    await expect(page.locator('#serversPanel')).toBeVisible();
    await expect(page.locator('#serversList .route-service-item')).toHaveCount(3);
    await expect(page.locator('#serversList')).toContainText('Telegram');
    await expect(page.locator('#serversList')).toContainText('Zapret');
    await expect(page.locator('#serversList')).toContainText('ByeDPI');
    await expect(page.locator('#serversList .route-service-latency').first()).toContainText('ms');
    await expect(page.locator('#serversList .route-service-method').first()).toContainText(/\S/);
    await page.locator('#showAllServersBtn').click();
    await expectDrawer(page, 'serversModal');
    await expect(page.locator('#serversModalList .route-service-item')).toHaveCount(4);

    const calls = await page.evaluate(() => window.__dropoMock.calls.map((call) => call.name));
    expect(calls).not.toContain('TestRouteMethods');
  });

  test('background route probe does not open test modal automatically', async ({ page }) => {
    await boot(page);
    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('route-probe-start', {
        serviceCount: 1,
        services: [{ tag: 'discord', name: 'Discord' }],
        freeMethodCount: 1,
        transparentMethodCount: 0,
        vpnCandidateCount: 0,
      });
      emit('route-probe-service', {
        tag: 'discord',
        name: 'Discord',
        methodLabel: 'ByeDPI SNI split',
        latencyMs: 71,
        success: true,
      });
      emit('route-probe-complete', {
        durationMs: 71,
        services: [{ tag: 'discord', name: 'Discord', methodLabel: 'ByeDPI SNI split', latencyMs: 71, success: true }],
      });
    });

    await expect(page.locator('#routeProbeModal')).not.toHaveClass(/active/);
  });

  test('startup route probe keeps loader until service strategies are resolved', async ({ page }) => {
    await boot(page);

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('route-probe-start', {
        reason: 'startup required',
        serviceCount: 2,
        services: [
          { tag: 'discord', name: 'Discord' },
          { tag: 'youtube', name: 'YouTube' },
        ],
        freeMethodCount: 3,
        transparentMethodCount: 1,
        vpnCandidateCount: 1,
      });
    });

    await expect(page.locator('#globalBusyOverlay')).toHaveClass(/active/);
    await expect(page.locator('#globalBusyDetails')).toHaveClass(/active/);
    await expect(page.locator('#globalBusyDetails')).toContainText('Discord');
    await expect(page.locator('#globalBusyDetails')).toContainText('YouTube');
    await expect(page.locator('#globalBusyMessage')).toContainText('0/2');
    await expect(page.locator('#routeProbeModal')).not.toHaveClass(/active/);

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('route-probe-candidate', {
        serviceTag: 'discord',
        serviceName: 'Discord',
        methodTag: 'byedpi-fast',
        methodLabel: 'ByeDPI fast',
        methodKind: 'proxy',
        latencyMs: 42,
        success: true,
      });
      emit('route-probe-service', {
        tag: 'discord',
        name: 'Discord',
        methodTag: 'byedpi-fast',
        methodLabel: 'ByeDPI fast',
        methodKind: 'proxy',
        latencyMs: 42,
        success: true,
      });
    });

    await expect(page.locator('#globalBusyMessage')).toContainText('Discord');
    await expect(page.locator('#globalBusyMessage')).toContainText('ByeDPI fast');
    await expect(page.locator('#globalBusyDetails')).toContainText('42 ms');

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('route-probe-service', {
        tag: 'youtube',
        name: 'YouTube',
        error: 'not available',
        success: false,
      });
      emit('route-probe-complete', {
        reason: 'startup required',
        durationMs: 333,
        services: [
          { tag: 'discord', name: 'Discord', methodTag: 'byedpi-fast', methodLabel: 'ByeDPI fast', methodKind: 'proxy', latencyMs: 42, success: true },
          { tag: 'youtube', name: 'YouTube', error: 'not available', success: false },
        ],
      });
    });

    await expect(page.locator('#globalBusyMessage')).toContainText('1/2');
    await expect(page.locator('#globalBusyDetails')).toContainText('not available');
    await expect(page.locator('#routeProbeModal')).not.toHaveClass(/active/);
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/, { timeout: 7000 });
  });

  test('service availability button runs bundled quick check and shows output', async ({ page }) => {
    await boot(page);

    await page.locator('#serviceCheckBtn').click();
    await expect(page.locator('#serviceCheckModal')).toHaveClass(/active/);
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
    await expect(page.locator('#serviceCheckOutput')).toContainText('dropo client quick check');
    await expect(page.locator('#serviceCheckOutput')).toContainText('Mixed proxy: http://127.0.0.1:2088');
    await expect(page.locator('#serviceCheckOutput')).toContainText('Testing Discord');
    await expect(page.locator('#serviceCheckSummary')).toContainText('Проверка завершена');
    await expect(page.locator('#serviceCheckRunBtn')).toBeEnabled();

    const calls = await page.evaluate(() => window.__dropoMock.calls.map((call) => call.name));
    expect(calls).toContain('RunClientQuickCheck');
  });

  test('toast stays bottom-right above open drawers', async ({ page }, testInfo) => {
    await boot(page);
    await page.locator('#settingsBtn').click();
    await expectDrawer(page, 'settingsModal');
    await page.evaluate(() => window.showToast('error', 'Visual preflight toast'));
    await expect(page.locator('.toast').last()).toBeVisible();
    await page.waitForTimeout(450);
    await saveScreenshot(page, testInfo, 'toast-over-drawer');

    const geometry = await page.evaluate(() => {
      const toast = document.querySelector('.toast');
      const overlay = document.querySelector('#settingsModal');
      const toastRect = toast.getBoundingClientRect();
      return {
        toastRightGap: window.innerWidth - toastRect.right,
        toastBottomGap: window.innerHeight - toastRect.bottom,
        toastZ: Number.isNaN(Number(getComputedStyle(toast).zIndex))
          ? Number(getComputedStyle(toast.parentElement).zIndex)
          : Number(getComputedStyle(toast).zIndex),
        overlayZ: Number(getComputedStyle(overlay).zIndex),
      };
    });

    expect(geometry.toastRightGap).toBeGreaterThanOrEqual(0);
    expect(geometry.toastRightGap).toBeLessThanOrEqual(24);
    expect(geometry.toastBottomGap).toBeGreaterThanOrEqual(0);
    expect(geometry.toastBottomGap).toBeLessThanOrEqual(24);
    expect(geometry.toastZ).toBeGreaterThan(geometry.overlayZ);
  });

  test('global busy loader covers active processes above drawers and toasts', async ({ page }, testInfo) => {
    await boot(page);
    await page.locator('#settingsBtn').click();
    await expectDrawer(page, 'settingsModal');
    await page.evaluate(() => window.showToast('info', 'Background process toast'));
    await expect(page.locator('.toast').last()).toBeVisible();

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', { id: 'visual-busy', active: true, message: 'РџРѕРґР±РёСЂР°РµРј Р»СѓС‡С€РёР№ РјР°СЂС€СЂСѓС‚ РґР»СЏ СЃРµСЂРІРёСЃРѕРІ...' });
    });

    await expect(page.locator('#globalBusyOverlay')).toHaveClass(/active/);
    await expect(page.locator('#globalBusyMessage')).toContainText('РџРѕРґР±РёСЂР°РµРј Р»СѓС‡С€РёР№ РјР°СЂС€СЂСѓС‚');
    await saveScreenshot(page, testInfo, 'global-busy-overlay');

    const geometry = await page.evaluate(() => {
      const busy = document.querySelector('#globalBusyOverlay');
      const drawer = document.querySelector('#settingsModal');
      const toastContainer = document.querySelector('#toastContainer');
      return {
        busyZ: Number(getComputedStyle(busy).zIndex),
        drawerZ: Number(getComputedStyle(drawer).zIndex),
        toastZ: Number(getComputedStyle(toastContainer).zIndex),
      };
    });

    expect(geometry.busyZ).toBeGreaterThan(geometry.drawerZ);
    expect(geometry.busyZ).toBeGreaterThan(geometry.toastZ);

    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('app-busy', { id: 'visual-busy', active: false });
    });
    await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
  });

  test('route probe modal shows checks, results and closes by overlay click', async ({ page }, testInfo) => {
    await boot(page);
    await page.evaluate(() => window.openRouteProbeModal());
    await page.evaluate(() => {
      const emit = (name, payload) => (window.__dropoMock.events[name] || []).forEach((handler) => handler(payload));
      emit('route-probe-start', {
        serviceCount: 2,
        freeMethodCount: 5,
        transparentMethodCount: 2,
        vpnCandidateCount: 1,
        services: [
          { tag: 'discord', name: 'Discord' },
          { tag: 'telegram', name: 'Telegram' },
        ],
      });
      emit('route-probe-log', { message: 'Route check started' });
      emit('route-probe-candidate', {
        serviceName: 'Discord',
        methodLabel: 'ByeDPI SNI split',
        latencyMs: 123,
        success: true,
      });
      emit('route-probe-service', {
        tag: 'discord',
        name: 'Discord',
        methodLabel: 'ByeDPI SNI split',
        latencyMs: 123,
        success: true,
      });
      emit('route-probe-service', {
        tag: 'telegram',
        name: 'Telegram',
        error: 'no available candidates',
        success: false,
      });
      emit('route-probe-complete', {
        durationMs: 456,
        services: [
          { tag: 'discord', name: 'Discord', methodLabel: 'ByeDPI SNI split', latencyMs: 123, success: true },
          { tag: 'telegram', name: 'Telegram', error: 'no available candidates', success: false },
        ],
      });
    });

    await expect(page.locator('#routeProbeModal')).toHaveClass(/active/);
    await expect(page.locator('#routeProbeResults')).toContainText('Discord');
    await expect(page.locator('#routeProbeResults')).toContainText('Telegram');
    await expect(page.locator('#routeProbeProgressText')).toContainText('Окно останется открытым');
    await expectNoPageBreaks(page);
    await page.waitForTimeout(320);
    await saveScreenshot(page, testInfo, 'route-probe-modal');
    await page.locator('#routeProbeModal').click({ position: { x: 5, y: 5 } });
    await expect(page.locator('#routeProbeModal')).not.toHaveClass(/active/);
  });

  test('subscription, WireGuard and profile CRUD flows are clickable', async ({ page }) => {
    await boot(page);

    await page.locator('#vpnBadge').click();
    await page.locator('#subscriptionInput').fill('bad-url');
    await page.locator('#testVpnBtn').click();
    await expect(page.locator('#vpnModalStatus')).toContainText(/Invalid|РќРµРІРµСЂ|РѕС€РёР±|Invalid/i);
    await page.locator('#subscriptionInput').fill('https://subscription.example.test/sub');
    await page.locator('#testVpnBtn').click();
    await expect(page.locator('#saveVpnBtn')).toBeVisible();
    await page.locator('#saveVpnBtn').click();
    await expect(page.locator('#vpnBadge')).toContainText(/VPN|СЃРµСЂРІРµСЂ|servers/i);
    await page.keyboard.press('Escape');

    await page.locator('#wgBadge').click();
    await expectDrawer(page, 'wgListModal');
    await page.locator('button[onclick="openAddWgModal()"]').click();
    await expectDrawer(page, 'wgEditModal');
    await page.locator('#wgTag').fill('work');
    await page.locator('#wgName').fill('Work network');
    await page.locator('#wgConfig').fill(`[Interface]
PrivateKey = test
Address = 100.105.120.117/32

[Peer]
PublicKey = test
AllowedIPs = 192.168.8.0/24
Endpoint = vpn.example.test:51820`);
    await page.locator('#parseWgBtn').click();
    await expect(page.locator('#saveWgBtn')).toBeVisible();
    await page.locator('#saveWgBtn').click();
    await expect(page.locator('#wgBadge')).toContainText('1');
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');

    await page.locator('#profileLogo').click({ force: true });
    await expectDrawer(page, 'profilesModal');
    await page.locator('.add-profile-btn').click();
    await expectDrawer(page, 'profileEditModal');
    await page.locator('#profileNameInput').fill('QA profile');
    await page.locator('#saveProfileBtn').click();
    await expect(page.locator('#profilesModal')).toHaveClass(/active/);
    await expect(page.locator('#profilesList')).toContainText('QA profile');
    await closeDrawer(page, 'profilesModal');

    await page.locator('#exitAppBtn').click();
    await expect(page.locator('#globalBusyOverlay')).toHaveClass(/active/);
    await expect(page.locator('#globalBusyTitle')).toContainText('Завершаем dropo');
    await expect(page.locator('#globalBusyDetails')).toContainText('WinDivert');
    await expect(page.locator('#powerBtn')).not.toHaveClass(/connecting/);
    const calls = await page.evaluate(() => window.__dropoMock.calls.map((call) => call.name));
    expect(calls).toContain('QuitApp');
  });

  test('template, logs, stats, update and server panels are reachable', async ({ page }, testInfo) => {
    await boot(page);

    await page.locator('#settingsBtn').click();
    await page.locator('button[onclick="openTemplateEditor()"]').click();
    await expectDrawer(page, 'templateModal');
    await expect(page.locator('#templateEditor')).toHaveValue(/outbounds/);
    await page.locator('#templateEditor').fill('{"outbounds":[]}');
    await page.locator('button[onclick="saveTemplate()"]').click();
    await expect(page.locator('#templateModalStatus')).toContainText(/Template|РЎРѕС…СЂР°РЅ/i);
    await page.keyboard.press('Escape');
    await page.keyboard.press('Escape');

    await page.locator('button[onclick="openStats()"]').click();
    await expectDrawer(page, 'statsModal');
    await page.locator('button[onclick="resetStats()"]').click();
    await page.locator('#confirmOk').click();
    await expect(page.locator('.toast').last()).toBeVisible();
    await page.keyboard.press('Escape');

    await page.locator('button[onclick="openLogsModal()"]').click();
    await expectDrawer(page, 'logsModal');
    await expect(page.locator('#logsContainer .log-entry.success', { hasText: 'VPN started successfully' })).toBeVisible();
    await expect(page.locator('#logsContainer .log-entry.success', { hasText: 'VPN stopped successfully' })).toBeVisible();
    await expect(page.locator('button[onclick="clearLogs()"]')).toHaveCount(0);
    await expect(page.locator('#logsContainer')).toBeVisible();
    await page.keyboard.press('Escape');

    await page.evaluate(() => window.checkForUpdates());
    await expect(page.locator('#updateBanner')).toHaveClass(/show/);
    await page.locator('#updateBanner .btn').click();
    await expectDrawer(page, 'updateModal');
    await saveScreenshot(page, testInfo, 'drawer-update');
    await page.keyboard.press('Escape');

    await page.locator('#powerBtn').click();
    await page.waitForTimeout(2300);
    await expect(page.locator('#serversPanel')).toBeVisible();
    await page.locator('#showAllServersBtn').click();
    await expectDrawer(page, 'serversModal');
    await expect(page.locator('#serversModalList .route-service-item')).toHaveCount(4);
  });
});
