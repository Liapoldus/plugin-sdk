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

Библиотеки не зависят друг от друга. Их exact module path, REST routes, wire
format и transport profiles публикуются владельцами; не угадывайте import path
до назначения canonical remote Plugin SDK. Общие contracts не копируются в
plugin repos.

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

Для plugin-to-plugin взаимодействий Core публикует deny-by-default policy, но
не проксирует payload. Peer connections идут напрямую с отдельными identities и
trust roots. Carrier/security profile настраивается отдельно от
application-level method names и payloads; смена carrier не меняет plugin
handlers.

См. [создание plugin](../core/architecture/guide),
[границы SDK/protocol](../core/architecture/protocol),
[deployment](../core/architecture/plugin-deployment) и
[матрицу приёмки](../core/configuration/acceptance).
