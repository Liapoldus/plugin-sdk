# Архитектура plugin runtime

Плагин — отдельный процесс или удалённый service с собственным продуктовым
поведением и контрактами. Core остаётся единым control plane: хранит desired
settings в SQLite, вызывает plugin REST lifecycle и контролирует generation
readiness. Он не содержит конкретных plugin capabilities и не проксирует
plugin-to-plugin payload.

У плагина есть две независимые интеграционные плоскости:

1. **Plugin SDK REST** — общий lifecycle и control API, независимый от
   продуктовых endpoint-ов: bootstrap/identity, Manifest и settings schema,
   health/readiness, `Reload`, config pull и metrics. Процессом управляет
   оператор средствами ОС; SDK не предоставляет endpoint остановки.
2. **`pluginprotocol`** — опциональная библиотека прямого plugin-to-plugin
   обмена с регистрируемыми самими плагинами методами и handlers. Она не знает
   lifecycle Core, product method names, settings schemas или plugin products.

Библиотеки полностью независимы друг от друга. Плагин, которому не нужен peer
network, не обязан подключать `pluginprotocol`; его Go-модуль и REST facade
Plugin SDK публикуются отдельно. Контракты capabilities, payloads, settings и
Admin Surface принадлежат только конкретному plugin и не копируются в общий SDK,
`pluginprotocol` или Core.

## Lifecycle и поколения

При старте Core подключается к каждой вручную зарегистрированной и запущенной
replica, проверяет plugin REST identity/Manifest/schema и сверяет applied
generation. Для новой конфигурации Core валидирует candidate до транзакции,
одновременно сохраняет новый `active` и прежний active как `previous`, затем
отправляет `Reload(generation)` по REST. Плагин сам запрашивает точный generation
у Core, проверяет и применяет конфигурацию в памяти, после чего ACK-ает
generation и digest.

Для instance поддерживаются ровно два durable слота `active`, `previous`.
После validation одна SQLite-транзакция сохраняет candidate как `active`, бывший
`active` как `previous` и удаляет прежний `previous`. Частичный rollout идёт только
вперёд: готовые replicas обслуживают новый `active`, отставшие fenced/degraded и
повторно получают Reload; трафик допускается только к replicas с нужным
generation. Rollback — Core Management API operation: Core меняет
`active`/`previous` и вызывает обычный REST
`Reload(generation)`; отдельной plugin rollback command нет. Неответившие
replicas остаются fenced до ACK. См. [state machine Core](../core/architecture/control-plane).

Общий Plugin SDK предоставляет только инфраструктурные REST structures,
metrics/logging/error handling и lifecycle helpers. Он не выбирает plugin
settings и не содержит их бизнес-валидации. Плагин не получает application
config через env, argv или локальный application-config file; он загружает
immutable JSON через Core REST и держит active settings в памяти. Secret bytes
выдаются отдельно ограниченным REST grant и не попадают в settings, логи или
публичные ошибки.

## Размещение сервисов

В v1 оператор вручную устанавливает и запускает Core и каждый plugin standalone
binary. Core регистрирует и обслуживает fixed REST endpoint каждой replica, но
не управляет процессами и контейнерами. Docker/Compose, Swarm, Kubernetes,
установка releases и process supervision относятся к v2; будущая модель
описана в [deployment design](../core/architecture/plugin-deployment).

Ни один балансируемый endpoint не заменяет identity и ACK отдельной replica.
Подробности сетевой identity, deployment и retries задаёт
[deployment contract](../core/architecture/plugin-deployment).

## Общие библиотеки и границы

Каноническая ответственность и ещё не закрытые migration gates приведены в
[документе Plugin SDK и protocol](../core/architecture/protocol). `pluginprotocol`
регистрирует только произвольные application-level names, которые определяют
сами participating plugins; физический carrier/security profile настраивается
независимо от этих names. Remote workload transport требует аутентифицированной
защищённой связи. Core REST имеет отдельные trust roots и identities.

Health/readiness не становится `Ready`, пока конкретная replica не подтвердила
актуальные config и peer-policy generations. Неизвестный результат plugin Call
не повторяется автоматически; оборванный Stream закрывается. Публичная матрица
runtime-доказательств — [Core acceptance](../core/configuration/acceptance).
