import 'package:dropo/main.dart';
import 'package:flutter/material.dart';
import 'package:flutter_test/flutter_test.dart';

void main() {
  testWidgets('dropo Flutter shell keeps the compact map dashboard controls', (
    tester,
  ) async {
    tester.view.physicalSize = const Size(1280, 860);
    tester.view.devicePixelRatio = 1;
    addTearDown(tester.view.resetPhysicalSize);
    addTearDown(tester.view.resetDevicePixelRatio);

    await tester.pumpWidget(const DropoApp());
    await tester.pump();

    expect(find.text('Dr'), findsOneWidget);
    expect(find.text('opo'), findsOneWidget);
    expect(find.byIcon(Icons.menu), findsOneWidget);
    expect(find.byIcon(Icons.public), findsOneWidget);
    expect(find.byIcon(Icons.settings), findsOneWidget);

    await tester.tap(find.byIcon(Icons.menu));
    await tester.pump(const Duration(milliseconds: 240));

    expect(find.text('Подключение'), findsOneWidget);
    expect(find.text('Профили'), findsWidgets);
    expect(find.text('Настройки'), findsOneWidget);
    expect(find.text('Статистика'), findsOneWidget);
    expect(find.text('Логи'), findsOneWidget);
    expect(find.text('Выход'), findsOneWidget);
    expect(find.text('vdev'), findsOneWidget);
    expect(find.textContaining('Компоненты готовы:'), findsNothing);
    expect(find.byType(CircularProgressIndicator), findsWidgets);
  });

  testWidgets(
    'Android shell uses bottom navigation and requires subscription before VPN start',
    (tester) async {
      debugMobileShellOverride = true;
      tester.view.physicalSize = const Size(390, 844);
      tester.view.devicePixelRatio = 1;
      addTearDown(() => debugMobileShellOverride = null);
      addTearDown(tester.view.resetPhysicalSize);
      addTearDown(tester.view.resetDevicePixelRatio);

      final bridge = MockCoreBridge();
      await tester.pumpWidget(MaterialApp(home: DropoHomePage(bridge: bridge)));
      await tester.pump();
      await tester.pump(const Duration(seconds: 1));

      expect(find.text('Главная'), findsOneWidget);
      expect(find.text('Настройки'), findsOneWidget);
      expect(find.text('Еще'), findsOneWidget);
      expect(find.text('Рабочие сети'), findsOneWidget);
      expect(find.byIcon(Icons.menu), findsNothing);

      await tester.tap(
        find
            .ancestor(
              of: find.text('Настройки'),
              matching: find.byType(GestureDetector),
            )
            .last,
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 600));

      expect(find.text('Сервисы и маршруты'), findsOneWidget);
      expect(find.text('Через VPN'), findsWidgets);
      expect(find.text('Напрямую'), findsWidgets);

      await tester.tap(
        find
            .ancestor(
              of: find.text('Еще'),
              matching: find.byType(GestureDetector),
            )
            .last,
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 600));

      expect(find.text('Разделы'), findsOneWidget);
      expect(find.text('Профили'), findsOneWidget);
      expect(find.text('Статистика'), findsOneWidget);
      expect(find.text('Логи'), findsOneWidget);
      expect(find.text('Выход'), findsOneWidget);

      await tester.tap(find.text('Логи').last);
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 600));

      expect(find.text('Android core, VpnService и sing-box.'), findsOneWidget);
      expect(find.text('Копировать всё'), findsOneWidget);
      expect(find.text('Открыть папку с логами'), findsNothing);

      await tester.tap(
        find
            .ancestor(
              of: find.text('Главная'),
              matching: find.byType(GestureDetector),
            )
            .last,
      );
      await tester.pump();
      await tester.pump(const Duration(milliseconds: 600));

      await tester.tap(find.byIcon(Icons.power_settings_new));
      await tester.pump();

      expect(
        find.text('Добавьте VPN-подписку для запуска на Android.'),
        findsOneWidget,
      );
      expect(find.text('Добавить'), findsOneWidget);
      expect((await bridge.status()).connected, isFalse);

      await tester.pump(const Duration(seconds: 5));
    },
  );

  testWidgets('autostart prompt: ОК returns true (enable autostart)', (
    tester,
  ) async {
    bool? decision;
    await tester.pumpWidget(
      MaterialApp(
        home: autoStartPromptDialogForTest(
          onDecision: (value) => decision = value,
        ),
      ),
    );
    await tester.pump();

    expect(find.text('Автозапуск'), findsOneWidget);
    expect(find.text('ОК'), findsOneWidget);
    expect(find.text('Нет, не надо'), findsOneWidget);

    await tester.tap(find.text('ОК'));
    expect(decision, isTrue);
  });

  testWidgets('autostart prompt: «Нет, не надо» returns false (decline)', (
    tester,
  ) async {
    bool? decision;
    await tester.pumpWidget(
      MaterialApp(
        home: autoStartPromptDialogForTest(
          onDecision: (value) => decision = value,
        ),
      ),
    );
    await tester.pump();

    await tester.tap(find.text('Нет, не надо'));
    expect(decision, isFalse);
  });

  group('AppConfig.autoStartPrompted controls the first-run prompt', () {
    test('missing field is treated as already answered (no prompt)', () {
      expect(AppConfig.fromJson(const {}).autoStartPrompted, isTrue);
    });

    test('explicit false requests the prompt', () {
      expect(
        AppConfig.fromJson(const {
          'autoStartPrompted': false,
        }).autoStartPrompted,
        isFalse,
      );
    });

    test('explicit true suppresses the prompt', () {
      expect(
        AppConfig.fromJson(const {'autoStartPrompted': true}).autoStartPrompted,
        isTrue,
      );
    });

    test('copyWith carries autoStartPrompted through', () {
      final resolved = AppConfig.defaults.copyWith(
        autoStart: false,
        autoStartPrompted: true,
      );
      expect(resolved.autoStart, isFalse);
      expect(resolved.autoStartPrompted, isTrue);
    });
  });

  testWidgets(
    'Dropo Space first-run notice has no checkbox and closes after 8 seconds',
    (tester) async {
      bool? openDropoSpace;
      final info = AndroidCompatibilityInfo.fromJson(const {
        'supported': true,
        'manufacturer': 'Google',
        'brand': 'google',
        'model': 'Pixel',
        'device': 'pixel',
        'sdk': 35,
        'dropoSpaceSupported': true,
        'dropoSpaceReady': false,
        'dropoSpacePaused': false,
        'dropoSpaceCanCreate': true,
        'privateSpaceSupported': true,
        'promptDismissed': false,
        'searchUrl': '',
        'riskApps': [
          {
            'packageName': 'ru.rostel',
            'name': 'Госуслуги',
            'installed': true,
            'inDropoSpace': false,
          },
        ],
      });

      await tester.pumpWidget(
        MaterialApp(
          home: androidCompatibilityNoticeDialogForTest(
            info: info,
            onDecision: (value) => openDropoSpace = value,
          ),
        ),
      );
      await tester.pump();

      expect(find.text('Больше не показывать'), findsNothing);
      expect(find.byType(CheckboxListTile), findsNothing);
      expect(find.byType(LinearProgressIndicator), findsOneWidget);
      expect(find.text('Окно закроется через 8 сек.'), findsOneWidget);

      await tester.pump(const Duration(seconds: 7));
      expect(openDropoSpace, isNull);
      await tester.pump(const Duration(milliseconds: 1100));
      expect(openDropoSpace, isFalse);
    },
  );

  testWidgets('Dropo Space first-run notice can open the section immediately', (
    tester,
  ) async {
    bool? openDropoSpace;
    final info = AndroidCompatibilityInfo.fromJson(const {
      'supported': true,
      'sdk': 35,
      'dropoSpaceSupported': true,
      'dropoSpaceCanCreate': true,
      'promptDismissed': false,
      'riskApps': [
        {'packageName': 'ru.rostel', 'name': 'Госуслуги', 'installed': true},
      ],
    });

    await tester.pumpWidget(
      MaterialApp(
        home: androidCompatibilityNoticeDialogForTest(
          info: info,
          onDecision: (value) => openDropoSpace = value,
        ),
      ),
    );
    await tester.pump();
    await tester.tap(find.text('В Dropo Space'));

    expect(openDropoSpace, isTrue);
    await tester.pump(const Duration(seconds: 8));
    expect(openDropoSpace, isTrue);
  });

  test('UpdateInfo keeps Android GitHub APK asset details', () {
    final info = UpdateInfo.fromJson(const {
      'success': true,
      'hasUpdate': true,
      'currentVersion': '2.1.6',
      'latestVersion': '2.1.7',
      'releaseURL':
          'https://github.com/Droponevedimka/dropo/releases/tag/v2.1.7',
      'downloadURL':
          'https://github.com/Droponevedimka/dropo/releases/download/v2.1.7/dropo-Android-arm64.apk',
      'assetName': 'dropo-Android-arm64.apk',
      'fileSize': 58242990,
      'platform': 'android',
      'selfUpdate': false,
    });

    expect(info.hasUpdate, isTrue);
    expect(info.downloadUrl, contains('dropo-Android-arm64.apk'));
    expect(info.assetName, 'dropo-Android-arm64.apk');
    expect(info.fileSize, 58242990);
    expect(info.platform, 'android');
    expect(info.selfUpdate, isFalse);
  });

  test('MockCoreBridge exposes an autonomous Android UI backend', () async {
    final bridge = MockCoreBridge();

    await bridge.ensureStarted();
    expect((await bridge.status()).connected, isFalse);

    final connected = await bridge.setConnected(true);
    expect(connected['success'], isTrue);
    expect((await bridge.status()).connected, isTrue);
    expect(await bridge.logs(), contains('Mock VPN session is connected'));

    final events = await bridge.events(since: 0);
    expect(events.map((event) => event.name), contains('vpn-status-changed'));
  });
}
