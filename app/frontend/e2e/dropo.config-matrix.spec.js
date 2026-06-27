const { expect, test } = require('@playwright/test');
const path = require('node:path');

const mockPath = path.join(__dirname, 'wails-mock.js');

const FULL_SERVICE_NAMES = [
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

async function boot(page, mockOptions = {}) {
  await page.setViewportSize({ width: 1366, height: 768 });
  await page.route('https://cdn.jsdelivr.net/**', (route) => {
    route.fulfill({
      contentType: 'application/javascript',
      body: 'window.jsQR = function(){ return null; };',
    });
  });
  await page.addInitScript((opts) => {
    window.__dropoMockOptions = opts;
    window.__dropoVisualErrors = [];
    window.addEventListener('error', (event) => {
      window.__dropoVisualErrors.push(event.message || String(event.error));
    });
    window.addEventListener('unhandledrejection', (event) => {
      window.__dropoVisualErrors.push(String(event.reason && event.reason.message ? event.reason.message : event.reason));
    });
  }, mockOptions);
  await page.addInitScript({ path: mockPath });
  await page.goto('/');
  await page.locator('#powerBtn').waitFor({ state: 'visible' });
  await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
}

async function expectDrawer(page, id) {
  await expect(page.locator(`#${id}`)).toHaveClass(/active/);
  await expect(page.locator(`#${id} .modal`).first()).toBeVisible();
}

async function closeModal(page, id) {
  await page.keyboard.press('Escape');
  await expect(page.locator(`#${id}`)).not.toHaveClass(/active/);
}

async function runServiceCheckAndExpectAllServices(page) {
  await page.locator('#serviceCheckBtn').click();
  await expect(page.locator('#serviceCheckModal')).toHaveClass(/active/);
  await expect(page.locator('#serviceCheckSummary')).toContainText('Проверка завершена', { timeout: 10_000 });

  const output = page.locator('#serviceCheckOutput');
  await expect(output).toContainText('Mixed proxy: http://127.0.0.1:2088');
  await expect(output).toContainText(`Failed: 0/${FULL_SERVICE_NAMES.length}`);
  for (const service of ['Yandex', 'Ozon', 'Gosuslugi', 'Discord', 'YouTube', 'Telegram', 'WhatsApp Web', 'ChatGPT', 'OpenAI API', 'Cursor API']) {
    await expect(output, `quick check should include ${service}`).toContainText(`Testing ${service}`);
  }
  const testedCount = await output.evaluate((el) => (el.textContent.match(/^Testing /gm) || []).length);
  expect(testedCount).toBe(FULL_SERVICE_NAMES.length);
  await closeModal(page, 'serviceCheckModal');
}

async function connectAndExpectRouteList(page, expected) {
  await page.locator('#powerBtn').click();
  await expect(page.locator('#powerBtn')).not.toHaveClass(/connecting/, { timeout: 8_000 });
  await expect(page.locator('#status')).toContainText('Подключено');
  await expect(page.locator('#serversPanel')).toBeVisible();
  await expect(page.locator('#serversList .route-service-item')).toHaveCount(3);

  for (const { service, method } of expected.visible) {
    const item = page.locator('#serversList .route-service-item', { hasText: service });
    await expect(item).toBeVisible();
    await expect(item).toContainText(method);
    await expect(item.locator('.route-service-latency')).toContainText('ms');
  }

  await page.locator('#showAllServersBtn').click();
  await expectDrawer(page, 'serversModal');
  for (const { service, method } of expected.all) {
    const item = page.locator('#serversModalList .route-service-item', { hasText: service });
    await expect(item).toBeVisible();
    await expect(item).toContainText(method);
    await expect(item.locator('.route-service-latency')).toContainText('ms');
  }
  await closeModal(page, 'serversModal');
}

async function disconnect(page) {
  await page.locator('#powerBtn').click();
  await expect(page.locator('#powerBtn')).not.toHaveClass(/connecting/, { timeout: 8_000 });
  await expect(page.locator('#status')).toContainText('Отключено');
  await expect(page.locator('#globalBusyOverlay')).not.toHaveClass(/active/);
}

async function chooseServiceMethod(page, serviceTag, optionText) {
  const item = page.locator(`.blocked-service-item[data-service-tag="${serviceTag}"]`);
  await item.locator('.blocked-method-trigger').click();
  await item.locator('.blocked-method-option', { hasText: optionText }).click();
  await expect(item.locator('.blocked-method-trigger')).toContainText(optionText);
}

async function openSettings(page) {
  await page.locator('#settingsBtn').click();
  await expectDrawer(page, 'settingsModal');
}

test.describe.configure({ mode: 'serial' });

test.describe('dropo settings and routing matrix', () => {
  test.afterEach(async ({ page }, testInfo) => {
    const errors = await page.evaluate(() => window.__dropoVisualErrors || []);
    expect(errors, `browser errors in ${testInfo.title}`).toEqual([]);
  });

  test('Deep Windows path works without subscription, then with subscription and every routing setting combination', async ({ page }) => {
    await boot(page);

    await expect(page.locator('#networkModePill')).toContainText('Deep Windows');
    await expect(page.locator('#serviceCheckBtn')).toBeVisible();
    await expect(page.locator('#testRoutesBtn')).toHaveCount(0);

    await runServiceCheckAndExpectAllServices(page);

    await connectAndExpectRouteList(page, {
      visible: [
        { service: 'Telegram', method: 'Zapret' },
        { service: 'YouTube', method: 'Zapret' },
        { service: 'Discord', method: 'ByeDPI' },
      ],
      all: [
        { service: 'Telegram', method: 'Zapret' },
        { service: 'YouTube', method: 'Zapret' },
        { service: 'Discord', method: 'ByeDPI' },
        { service: 'AI services', method: 'Direct' },
      ],
    });
    await disconnect(page);

    await openSettings(page);
    await expect(page.locator('#settingRoutingMode')).toHaveValue('blocked_only');
    await expect(page.locator('#settingNetworkMode')).toHaveValue('auto');
    await expect(page.locator('#settingDisableFreeAccess')).not.toBeChecked();
    await expect(page.locator('#settingHideRuTraffic')).not.toBeChecked();

    for (const mode of ['except_russia', 'all_traffic', 'blocked_only']) {
      await page.locator('#settingRoutingMode').selectOption(mode);
      await expect(page.locator('#settingRoutingMode')).toHaveValue(mode);
    }
    for (const mode of ['deep_windows', 'compat_tun', 'auto']) {
      await page.locator('#settingNetworkMode').selectOption(mode);
      await expect(page.locator('#settingNetworkMode')).toHaveValue(mode);
      await expect(page.locator('#networkModePill')).toContainText('Deep Windows');
    }

    await page.locator('#settingDisableFreeAccess + .slider').click();
    await expect(page.locator('#settingDisableFreeAccess')).toBeChecked();
    await page.locator('#settingDisableFreeAccess + .slider').click();
    await expect(page.locator('#settingDisableFreeAccess')).not.toBeChecked();

    await page.locator('#openBlockedServicesBtn').click();
    await expectDrawer(page, 'blockedServicesModal');
    await chooseServiceMethod(page, 'telegram', 'VPN');
    await chooseServiceMethod(page, 'youtube', 'Direct');
    await chooseServiceMethod(page, 'discord', 'Zapret');
    await closeModal(page, 'blockedServicesModal');

    await closeModal(page, 'settingsModal');

    await page.locator('#vpnBadge').click();
    await expectDrawer(page, 'vpnModal');
    await page.locator('#subscriptionInput').fill('https://subscription.example.test/sub');
    await page.locator('#testVpnBtn').click();
    await expect(page.locator('#saveVpnBtn')).toBeVisible();
    await page.locator('#saveVpnBtn').click();
    await expect(page.locator('#vpnBadge')).toContainText(/3|VPN|servers/i);
    await closeModal(page, 'vpnModal');

    await openSettings(page);
    await page.locator('#settingHideRuTraffic + .slider').click();
    await expect(page.locator('#settingHideRuTraffic')).toBeChecked();
    await expect(page.locator('#ruProxyAddressRow')).toBeVisible();
    await page.locator('#settingRuProxyAddress').fill('vless://00000000-0000-0000-0000-000000000000@example.test:443?security=tls#ru');
    await page.locator('#settingRuProxyAddress').blur();
    await page.locator('#refreshFreeMethodsBtn').click();
    await expect(page.locator('#freeMethodsCacheInfo')).toContainText(/\d+\/\d+/);

    await page.locator('#settingRoutingMode').selectOption('except_russia');
    await page.locator('#settingDisableFreeAccess + .slider').click();
    await expect(page.locator('#settingDisableFreeAccess')).toBeChecked();
    await closeModal(page, 'settingsModal');

    await connectAndExpectRouteList(page, {
      visible: [
        { service: 'Telegram', method: 'VPN' },
        { service: 'YouTube', method: 'Direct' },
        { service: 'Discord', method: 'Zapret' },
      ],
      all: [
        { service: 'Telegram', method: 'VPN' },
        { service: 'YouTube', method: 'Direct' },
        { service: 'Discord', method: 'Zapret' },
        { service: 'AI services', method: 'VPN' },
      ],
    });

    await runServiceCheckAndExpectAllServices(page);
    await disconnect(page);

    const calls = await page.evaluate(() => window.__dropoMock.calls.map((call) => ({ name: call.name, args: call.args })));
    expect(calls.some((call) => call.name === 'RunClientQuickCheck')).toBe(true);
    expect(calls.some((call) => call.name === 'Toggle')).toBe(true);
    expect(calls.some((call) => call.name === 'SetVPNSubscription')).toBe(true);
    expect(calls.some((call) => call.name === 'SetRoutingMode' && call.args[0] === 'except_russia')).toBe(true);
    expect(calls.some((call) => call.name === 'SetRoutingMode' && call.args[0] === 'all_traffic')).toBe(true);
    expect(calls.some((call) => call.name === 'SetNetworkMode' && call.args[0] === 'compat_tun')).toBe(true);
    expect(calls.some((call) => call.name === 'SetDisableFreeAccess' && call.args[0] === true)).toBe(true);
    expect(calls.some((call) => call.name === 'SetHideRuTraffic' && call.args[0] === true)).toBe(true);
    expect(calls.some((call) => call.name === 'SetFreeAccessServiceMethod' && call.args[0] === 'telegram' && call.args[1] === 'vpn')).toBe(true);
    expect(calls.some((call) => call.name === 'SetFreeAccessServiceMethod' && call.args[0] === 'youtube' && call.args[1] === 'direct')).toBe(true);
    expect(calls.some((call) => call.name === 'SetFreeAccessServiceMethod' && call.args[0] === 'discord' && String(call.args[1]).includes('zapret'))).toBe(true);
  });

  test('Compatibility TUN fallback remains usable when Deep Windows is unavailable', async ({ page }) => {
    await boot(page, { deepEngineMissing: true, networkMode: 'deep_windows' });

    await expect(page.locator('#networkModePill')).toContainText('Compatibility TUN');
    await expect(page.locator('#networkModePill')).toHaveClass(/fallback/);
    await expect(page.locator('#networkModeBanner')).toHaveClass(/show/);

    await runServiceCheckAndExpectAllServices(page);
    await connectAndExpectRouteList(page, {
      visible: [
        { service: 'Telegram', method: 'Zapret' },
        { service: 'YouTube', method: 'Zapret' },
        { service: 'Discord', method: 'ByeDPI' },
      ],
      all: [
        { service: 'Telegram', method: 'Zapret' },
        { service: 'YouTube', method: 'Zapret' },
        { service: 'Discord', method: 'ByeDPI' },
        { service: 'AI services', method: 'Direct' },
      ],
    });
    await disconnect(page);
  });
});
