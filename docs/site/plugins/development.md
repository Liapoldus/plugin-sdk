# Разработка plugins

Плагин реализует собственное продуктовое поведение и подключает общую
инфраструктуру через два независимых Go-модуля:

- Plugin SDK — обязательный единый REST lifecycle/control surface: bootstrap,
  Manifest/settings schema, health/readiness, `Reload(generation)`, exact config
  pull, metrics, logging и безопасные ошибки. Rollback — операция Core
  Management API; plugin получает новое active-поколение через `Reload`.
- `pluginprotocol` — опциональная generic peer network для plugin-to-plugin
  вызовов. Plugin регистрирует собственные методы/handlers; library не знает
  product names, payload schema или lifecycle Core.

Библиотеки не зависят друг от друга. Canonical import path Plugin SDK —
`github.com/Liapoldus/plugin-sdk/v2`; точные REST routes принадлежат SDK, а wire
format и transport profiles — `pluginprotocol`. Общие contracts не копируются
в plugin repos.

TLS/mTLS — ответственность используемой библиотеки, не самого plugin:
Plugin SDK защищает Core↔plugin REST, а `pluginprotocol` — peer-соединения.
Плагин передаёт каждой библиотеке её security configuration/credentials через
публичный API и использует готовые client/listener/call abstractions; он не
реализует TLS, проверку сертификатов или revocation самостоятельно. Trust roots
для двух независимых каналов остаются раздельными. В peer-протоколе явный
plaintext допустим только для TCP loopback в development; remote и production
требуют mTLS. SDK REST production contract v1 требует mTLS. Разработчик может
явно выбрать opt-in loopback-only plaintext profile вызовом
`infrastructure.NewLoopbackHealthServer`; по умолчанию такой listener не
создаётся. SDK принимает только literal loopback TCP bind и сам ограничивает
маршрут `GET /_liapoldus/v1/health`. `/ready`, identity, Manifest, config schema,
Reload, metrics, admin actions и artifacts остаются mTLS-only. Exact config pull
и secret-grant clients/endpoints всегда используют mTLS; config bytes и secret
values никогда не выдаются через plaintext. TLS error не включает plaintext
fallback. Канонические значения profile находятся в
[`loopback-plaintext-profile.json`](../../../infrastructure/assets/plugin-sdk/v2/loopback-plaintext-profile.json).
Plugin только явно вызывает API SDK; собственную обработку TLS, сертификатов
или plaintext transport он не реализует.

Каждый plugin владеет собственным Manifest, settings schema, capabilities,
ошибками, Admin Surface и product data. Settings сохраняются Core в SQLite и
получаются plugin-ом только pull-запросом точной immutable generation после
`Reload`. Management API принимает непосредственно plugin-owned JSON object в
body, без общей `{config: ...}` обёртки. Core сохраняет исходные JSON bytes,
проверяет синтаксис, дубликаты ключей и schema, не декодируя продуктовые поля и
не пересериализуя документ. Передавать application settings через environment,
argv или application config files запрещено. Secret values извлекаются отдельно
через scoped Core REST grants и не попадают в response, log, trace, error или
audit.

Для plugin-to-plugin взаимодействий вызывающий plugin применяет собственную
authorization policy через generic authorizer `pluginprotocol`; Core не
публикует и не хранит такую policy и не проксирует payload. Peer connections
идут напрямую с отдельными identities и trust roots. Carrier/security profile настраивается отдельно от
application-level method names и payloads; смена carrier не меняет plugin
handlers.

См. [создание plugin](../core/architecture/guide),
[границы SDK/protocol](../core/architecture/protocol),
[deployment](../core/architecture/plugin-deployment) и
[матрицу приёмки](../core/configuration/acceptance).
