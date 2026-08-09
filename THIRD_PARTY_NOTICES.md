# Third-party notices and research references

Этот файл разделяет компоненты, которые поставляются с dropo, и проекты,
использованные только для изучения общих сетевых техник. Наличие ссылки в
исследовательском разделе не означает включение исходников или runtime проекта.

## Компоненты Windows release

| Компонент | Назначение | Лицензия / источник |
| --- | --- | --- |
| WinDivert 2.2.2 | Перехват и reinjection пакетов в собственном Windows engine | [Официальный сайт и документация](https://reqrypt.org/windivert.html), LGPL-3.0-only / GPL-2.0-only; текст лицензии включается в `licenses/WinDivert-LICENSE.txt` |
| sing-box | TUN, VPN-протоколы и маршрутизация | [SagerNet/sing-box](https://github.com/SagerNet/sing-box); текст лицензии включается в release |
| WireGuard for Windows / Wintun | Рабочие и пользовательские WireGuard-туннели | [WireGuard/wireguard-windows](https://github.com/WireGuard/wireguard-windows); текст лицензии включается в release |
| Xray-core | Поддержка отдельных VLESS transport-вариантов | [XTLS/Xray-core](https://github.com/XTLS/Xray-core); текст лицензии включается в release |
| tg-ws-proxy | Локальный MTProto-over-WebSocket transport для Telegram | Локально закреплённая версия 1.7.3, MIT; текст лицензии включается в `licenses/tg-ws-proxy-LICENSE.txt` |
| Flutter | Пользовательский интерфейс | [flutter/flutter](https://github.com/flutter/flutter), BSD-3-Clause |
| Re-filter lists | Вложенный каталог заблокированных доменов и IP-сетей, а также локально скомпилированные sing-box rule-set | [1andrevich/Re-filter-lists](https://github.com/1andrevich/Re-filter-lists), MIT; точный release и SHA-256 записываются в `bin/filters/version.json`, лицензия включается в `licenses/Re-filter-lists-LICENSE.txt` |

Точные версии и хеши закреплены в `version.json`, file-level
`runtime-manifest.json` и `dropo-sbom.spdx.json`, создаваемых сборкой.

## Исследовательские источники

| Проект | Что изучается | Статус в release |
| --- | --- | --- |
| [bol-van/zapret2](https://github.com/bol-van/zapret2) | Узкая kernel-side фильтрация, классификация STUN/Discord/WireGuard, порядок и ограничения desync-техник | Процесс, Lua, Cygwin, бинарники и исходники не поставляются. Собственная реализация написана на Go с типизированными bounded actions. Upstream MIT license учитывается при любом будущем переносе существенного кода. |
| [Flowseal/zapret-discord-youtube](https://github.com/Flowseal/zapret-discord-youtube) | Актуальная ALT12-комбинация активных Discord/STUN decoy-пакетов и список прямых исключений Steam | Не поставляется и не вызывается; использована только как проверочная конфигурация для собственной типизированной реализации |
| [bol-van/zapret](https://github.com/bol-van/zapret) | Исторические описания split/overlap/fake-подходов и blockcheck | Не поставляется и не вызывается |
| [hufrea/byedpi](https://github.com/hufrea/byedpi) | Сравнение proxy-based обхода с прозрачным packet engine | Не входит в Windows release |

Последние исследованные ревизии на 2026-08-09:

- zapret2: `032651deeb2117a32c67fdf5cec115d5e52a63dd`;
- Flowseal zapret-discord-youtube: `47da17f80ad36a8424cdd25658153fdebd7eb938` (обновление стратегий от 2026-08-09 проверено; обработка неизвестного или слабо классифицированного игрового payload намеренно не перенесена в fail-safe packet path dropo).

Обновление upstream само по себе не обновляет dropo: сначала выполняются
license/security review, перевод идеи в типизированную модель, unit/fixture/
Windows VM тесты и release audit.
