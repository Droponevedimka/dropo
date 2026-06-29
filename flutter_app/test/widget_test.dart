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
}
