# Dropo DPI censor lab

Лаборатория воспроизводит блокировки уровня провайдера за пределами целевой
сети. Она нужна для регрессионной проверки собственного Windows Traffic
Orchestrator и не входит в пользовательскую поставку.

Схема проверки:

```text
[ Windows VM: Dropo + один WinDivert ] -> [ Linux censor ] -> Internet
```

## Модель блокировок

`censor/apply.py` преобразует объединённый профиль в правила FORWARD:

| Поле профиля | Модель | Действие |
|---|---|---|
| `tls_rst[]` | чтение TLS SNI и активный reset | `REJECT --reject-with tcp-reset` |
| `tls_drop[]` | silent drop по SNI | `DROP` |
| `ip_drop[]` | блокировка IP/CIDR | `DROP` |
| `throttle[]` | ограничение скорости | `MARK` + `tc htb` |

## Быстрый self-test

```bash
cd testlab
python tools/build_profile.py
docker compose up -d --build censor
docker compose run --rm tester
docker compose down
```

Ожидается, что контрольный host доступен, TLS-цели из профиля получают RST,
а IP-block цели завершаются timeout. Для iptables-варианта нужен host kernel с
`xt_string`.

## Stateful censor

Наивный поиск подстроки можно обойти простым TCP split, поэтому
`censor/stateful.py` сначала восстанавливает TCP stream и только затем читает
SNI. Это отделяет реальную устойчивость стратегии от ложного успеха в тестовой
среде.

```bash
docker compose -f docker-compose.yml -f docker-compose.stateful.yml up -d --build censor
docker compose -f docker-compose.yml -f docker-compose.stateful.yml run --rm tester
docker run --rm --entrypoint python3 testlab-censor-stateful /stateful.py --selftest
```

Параметры модели:

| Действие Dropo | Параметр | Проверяемое свойство |
|---|---|---|
| TCP split | — | stream reassembly не должен давать ложный pass |
| sequence overlap | `REASSEMBLE_POLICY=first\|last` | политика разрешения перекрытий |
| fake с некорректным checksum | `VALIDATE_CHECKSUM=0\|1` | отбрасывание повреждённых сегментов |
| low-TTL fake | `MIN_TTL=0..255` | достижимость синтетического пакета до DPI |
| QUIC Initial action | `QUIC=1` | извлечение и сопоставление QUIC SNI |

Для проверки конкретной сети значения задаются по клиентскому fingerprint.
Успех кандидата засчитывается только если одна стратегия проходит весь набор
обязательных TCP, UDP и web-целей одновременно.

## Проверка Windows-приложения

1. Запустить censor и определить его gateway address.
2. Направить Windows VM через этот gateway.
3. Запустить собранный Dropo EXE с включённым Defender.
4. Через диагностический API запустить `SelectTrafficStrategy` для набора целей.
5. Проверить, что в процессе работает один Dropo-owned WinDivert handle, план
   меняется без перезапуска драйвера, а обычный HTTPS остаётся неизменным.
6. Одновременно проверить Discord web/API, signalling, STUN, voice/video и Go
   Live, затем отказ local strategy и переход на следующий VPN-источник.

## Обновление fingerprint-базы

Клиент создаёт JSON через **Настройки → Снять отпечаток**. Добавление:

```bash
python tools/add_fingerprint.py path/to/dpi-fingerprint.json --isp mts --country RU
```

Файл копируется в `fingerprints/`, после чего профиль перестраивается.
Агрегация сохраняет наиболее строгий наблюдавшийся verdict для сервиса.

## Структура

```text
testlab/
  services.json                  service SNI/IP fixtures
  fingerprints/                 client reports and schema
  profiles/censor-profile.json  generated censor policy
  tools/build_profile.py        merge fingerprints
  tools/add_fingerprint.py      import one report
  censor/                       stateless/stateful censor
  tester/                       integration self-tests
  docker-compose.yml
```

Исследовательские источники и их лицензии перечислены только в
[`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md).
