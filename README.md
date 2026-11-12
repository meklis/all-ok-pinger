# Golang ICMP/TCP pinger

> Отличия от версии 0.3 и инструкция по переходу — [docs/CHANGES-1.0.0.md](docs/CHANGES-1.0.0.md).

Список хостов (`api.host_list_url`) загружается в фоне раз в `api.host_list_refresh_interval` (5m) и синхронизируется
с внутренним хранилищем (добавление / обновление атрибутов / удаление).

Цикл раз в `sleep_interval` (30s; если цикл длится дольше — следующий начинается сразу):
ICMP-проверка → TCP-проверка падающих → репорт (`api.report_url`) по хостам, чей статус расходится с известным серверу.

## Принцип проверки

* Инспекция: каждому хосту отправляется `icmp.packets` (4) пакетов по кругу (1-й всем хостам, затем 2-й…),
  в случайном порядке, с общим темпом `icmp.packets_per_second` (15000). Хост ответил, если пришёл хотя бы один ответ.
* Хост, подтвердивший свой статус (up и ответил, down и не ответил), выбывает из цикла.
  Остальные проверяются повторно через `icmp.inspection_interval` (3s), максимум `icmp.inspections` (10) инспекций:
  * up → down — `icmp.failed_for_down` (10) провальных инспекций подряд, затем TCP-проверка (`tcp.ports`), если включена;
  * down → up — `icmp.success_for_up` (2) успешных инспекций подряд.
* Статусы: `1`–`100` — up, процент полученных пакетов за цикл (`100` — ответил на все пакеты, даже если проверялся одну инспекцию),
  `0` — down, `999` — не отвечает по ICMP, но доступен по TCP. Репорт уходит при любом изменении статуса, в том числе процента.
* Хост с неизвестным статусом проверяется как up: `100`, или `0` после `failed_for_down` провальных инспекций.
* Известный статус берётся из хранилища (последний отправленный, если репорты включены, иначе — с прошлого прохода),
  если его нет — из списка хостов.

## Список хостов

```json
{"data": [
  {"ip": "10.0.0.1", "status": 100, "target_group": "core", "_id": 15, "_name": "sw-1", "_location": "Kyiv"},
  {"ip": "10.0.0.2", "status": null}
]}
```

* Поля с префиксом `_` — дополнительные атрибуты. Передаются в репорт/API как есть и добавляются в метрики
  лейблами без префикса (`_location` → `location`).
* `status: null` — статус берётся с прошлого прохода (если репорты включены — последний успешно отправленный).
  Если прошлого прохода нет — хост попадает в репорт в любом случае.
* `target_group` — необязательный; если его нет, в репорт он не попадает.
* `api.ignore_host_list_statuses: true` — статусы из списка игнорируются (как `status: null`).

## Репорт

POST `api.report_url?ident=<ident>` — массив хостов с изменившимся статусом:

```json
{
  "ip": "10.0.0.1", "status": 100, "target_group": "core",
  "icmp": true, "tcp": null,
  "latency":    {"min": 1.2, "max": 3.4, "avg": 2.1},
  "latency_1m": {"min": 1.1, "max": 4.0, "avg": 2.2},
  "latency_5m": {"min": 1.0, "max": 5.2, "avg": 2.3},
  "loss": {"transmitted": 15, "received": 15, "loss": 0},
  "_id": 15, "_name": "sw-1", "_location": "Kyiv"
}
```

* задержки — в мс; `latency` — за текущий проход, `null`, если ответов не было;
* `tcp` — `null`, если TCP-проверка выключена или не выполнялась (хост отвечает по ICMP);
* `status` — `1`–`100` — up (процент полученных пакетов за цикл), `0` — down, `999` — доступен только по TCP.

В репорт попадают хосты, чей статус у пингера расходится с тем, что известно серверу. Если таких нет, запрос не уходит.
Расхождение возникает в трёх случаях:
1. статус изменился в этом цикле;
2. прошлый репорт не дошёл (отправленным статус считается только после HTTP 200) — хост уходит в следующем цикле;
3. в списке хостов, загруженном после последней отправки, у хоста другой статус (например, в списке `0`, а хост работает).
   `status: null` в списке означает, что сервер статус не сообщает, — такие хосты расхождения не дают.

Что известно серверу — более свежий из двух статусов: последний отправленный или статус из списка хостов.
Хосты, ждущие отправки, видны в API (`report_pending: true`), на дашборде (фильтр «Ждут репорта»)
и в метрике `icmp_pinger_sys_report_pending_hosts`.

`api.report_enabled: false` (или пустой `report_url`) — репорты не отправляются.

## Реалтайм-состояния

Статус фиксируется (и уходит в репорт) после всех инспекций цикла. Внутри цикла после каждой инспекции видно:
* `falling` — хост был up и не ответил в `realtime.falling_after_inspections` (3) инспекциях;
* `rising` — хост был down и ответил в `realtime.rising_after_inspections` (1) инспекциях.

В API: поле `state` (`up|down|unknown|falling|rising`) и `pending` (`failed_inspections`, `success_inspections`,
`inspection`, `inspections`, `since`); фильтр `GET /api/v1/hosts?state=falling,rising`; прогресс цикла в `/api/v1/summary`.

## Redis

`redis.enabled: true` — события и репорты публикуются в redis асинхронно через pub/sub (`PUBLISH <канал> <json>`).
* `events_key` — канал событий: `status_changed`, `falling`, `rising`, `falling_cancelled`, `rising_cancelled`, `host_added`, `host_removed`.
  ```json
  {"ident":"PINGER","type":"falling","ip":"10.0.0.1","time":"...","status":100,"prev_status":100,"state":"falling",
   "pending":{"state":"falling","failed_inspections":2,...},"host":{...объект репорта...}}
  ```
* `reports_key` — канал репортов по изменившимся хостам за цикл: `{"type":"report","ident":..,"time":..,"count":..,"items":[...]}`.

## Веб-сервер (`web.listen`, по умолчанию `:2112`)

| Путь | Описание |
|---|---|
| `/` | дашборд: сводка, хосты в переходе, хосты с группировкой по атрибутам, лента событий, настройки |
| `GET /api/v1/hosts` | все хосты; фильтры `status=up\|down\|unknown`, `target_group=`, `ip=a,b`, `q=` |
| `GET /api/v1/hosts/{ip}` | текущее состояние одного хоста |
| `GET /api/v1/summary` | сводка и информация о последнем цикле |
| `GET /api/v1/ws` | websocket: события в реальном времени (см. ниже) |
| `GET /api/v1/config` | конфигурация (без секретов) |
| `/metrics` | метрики Prometheus (`prometheus.enabled`) |

Ответы `/api/v1/hosts` и `/api/v1/summary` сжимаются gzip (55к хостов: 42 МБ → 2,9 МБ).

### Websocket `/api/v1/ws`

Одно JSON-сообщение на кадр:
* события хостов — в том же формате, что в redis (`status_changed`, `falling`, `rising`, `falling_cancelled`,
  `rising_cancelled`, `host_added`, `host_removed`);
* `{"type":"inspection","progress":{...},"pending":[{"ip","status","state","failed_inspections","success_inspections","inspection","inspections","since"}]}` —
  после каждой инспекции: все хосты, которые сейчас падают / поднимаются;
* `{"type":"cycle","cycle":{...}}` — цикл завершён, статусы зафиксированы;
* `{"type":"ping"}` — раз в 30 с.

Фильтры: `?types=falling,rising,cycle` (типы сообщений), `?ip=10.0.0.1,10.0.0.2` (события хостов).
Медленный клиент (не успевает читать) отключается — после переподключения нужно перечитать `/api/v1/hosts`.

Дашборд загружает хосты при подключении и после каждого цикла, остальное применяет из websocket.
Группировка, сортировка, развёрнутые группы и фильтры сохраняются в localStorage браузера.

## Метрики Prometheus

* `prometheus.host_metrics`: `none` | `basic` (status, up, icmp, loss за проход, счётчики пакетов) | `detailed` (+ loss по окнам, tcp, cicles).
* `prometheus.latency_metrics`: `none` | `avg` (`icmp_pinger_peer_latency_sec` — среднее за последний проход) | `all` (+ `icmp_pinger_peer_latency_ms{window,stat}`).
* `prometheus.attribute_labels`: `info` — атрибуты только в `icmp_pinger_peer_info{host,...} 1`
  (`icmp_pinger_peer_up * on(host) group_left(name,location) icmp_pinger_peer_info`) | `all` — на каждой метрике хоста.

Системные метрики:

| Метрика | Описание |
|---|---|
| `icmp_pinger_sys_last_cicle_sec` | длительность последнего цикла |
| `icmp_pinger_sys_cicles_sec`, `icmp_pinger_sys_cicles_count` | суммарное время и количество циклов (counter) |
| `icmp_pinger_sys_last_cycle_timestamp_seconds` | время окончания последнего цикла — алерт на зависание: `time() - ... > 300` |
| `icmp_pinger_sys_inspection_duration_sec{inspection}` | длительность инспекции (отправка + ожидание ответов) в текущем / последнем цикле |
| `icmp_pinger_sys_inspection_hosts{inspection}` | сколько хостов проверялось в инспекции |
| `icmp_pinger_sys_inspections_count` | количество инспекций |
| `icmp_pinger_sys_host_list_last_success_timestamp_seconds` | последняя успешная загрузка списка хостов — алерт: `time() - ... > 900` |
| `icmp_pinger_sys_host_list_load_duration_sec`, `icmp_pinger_sys_host_list_hosts` | длительность последней загрузки и количество хостов в списке |
| `icmp_pinger_sys_host_list_errors_count` | ошибки загрузки списка |
| `icmp_pinger_sys_reports_count{result}`, `icmp_pinger_sys_reported_hosts_count` | репорты |
| `icmp_pinger_sys_report_pending_hosts` | хостов, ждущих отправки в репорте |
| `icmp_pinger_sys_report_last_success_timestamp_seconds` | время последнего успешного репорта |
| `icmp_pinger_sys_redis_messages_count{result}` | сообщения в redis (sent, error, dropped) |

Метрики инспекций сбрасываются в начале цикла: во время цикла видны уже выполненные инспекции текущего цикла.

## Производительность (замеры на 55к хостов)

* Сеть держит ~20–30к пакетов/с без роста потерь; при 50к/с потери ~12%, при 95к/с — ~55%.
* Настройки по умолчанию (4 пакета, 15к пакетов/с, ожидание ответов 2 с, 10 инспекций с паузой 3 с): первая инспекция ~17 с, цикл ~60 с.
* Число отправляющих сокетов вычисляется из `packets_per_second` (~8к пакетов/с на сокет),
  буфер приёма ICMP-сокета — 32 МБ (`SO_RCVBUFFORCE`).

## Логи

logrus: `log.level` (`debug|info|warn|error`), `log.format` (`text|json`).

## Переменные окружения

`PINGER_IDENT`, `SLEEP_INTERVAL`, `LOG_LEVEL`, `LOG_FORMAT`,
`API_HOST_LIST_URL`, `API_HOST_LIST_REFRESH_INTERVAL`, `API_IGNORE_HOST_LIST_STATUSES`,
`API_REPORTS_URL`, `API_REPORTS_ENABLED`, `API_TIMEOUT`,
`ICMP_INSPECTIONS`, `ICMP_FAILED_FOR_DOWN`, `ICMP_SUCCESS_FOR_UP`, `ICMP_PACKETS`, `ICMP_PACKETS_PER_SECOND`,
`ICMP_RESPONSE_TIMEOUT`, `ICMP_INSPECTION_INTERVAL`, `ICMP_IPV6`,
`TCP_ENABLED`, `TCP_PORTS` (`[80,22]`), `TCP_WORKERS`, `TCP_TIMEOUT`,
`REALTIME_FALLING_AFTER_INSPECTIONS`, `REALTIME_RISING_AFTER_INSPECTIONS`, `WEB_LISTEN`, `WEB_PPROF`,
`PROMETHEUS_ENABLED`, `PROMETHEUS_HOST_METRICS`, `PROMETHEUS_LATENCY_METRICS`, `PROMETHEUS_ATTRIBUTE_LABELS`,
`REDIS_ENABLED`, `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_DB`, `REDIS_EVENTS_KEY`, `REDIS_REPORTS_KEY`, `REDIS_QUEUE_SIZE`.
