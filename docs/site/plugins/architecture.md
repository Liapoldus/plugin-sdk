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
вперёд: готовые replicas обслуживают новый `active`, отставшие остаются
fenced/degraded, а трафик допускается только к replicas с нужным generation.
Core выполняет reconciliation один раз при старте; работающий Core может только
наблюдать readiness и не повторяет `Reload` в фоне. После ручного восстановления
plugin оператор проверяет его health и перезапускает Core, чтобы запустить
startup reconciliation. Rollback — Core Management API operation: Core меняет
`active`/`previous` и вызывает обычный REST
`Reload(generation)`; отдельной plugin rollback command нет. Неответившие
replicas остаются fenced до ACK. См. [state machine Core](../core/architecture/control-plane).

Общий Plugin SDK предоставляет инфраструктурные REST structures и явный
`infrastructure.InProcessReplica` для trusted Go composition, а также
metrics/logging/error handling и lifecycle helpers. Он не выбирает plugin
settings и не содержит их бизнес-валидации. Плагин не получает application
config через env, argv или локальный application-config file; он загружает
immutable JSON через Core REST и держит active settings в памяти. Secret bytes
выдаются отдельно ограниченным REST grant и не попадают в settings, логи или
публичные ошибки.

Сетевую защиту Core↔plugin REST реализует Plugin SDK: его listener/client
выполняют TLS/mTLS handshake, проверку цепочки, replica identity и настроенной
revocation policy. Plugin передаёт SDK bootstrap security configuration и
credential source, но не собирает собственный TLS stack и не обрабатывает
сертификаты в product handlers. В production lifecycle контракт требует
per-replica mTLS; Bearer credential дополняет, но не заменяет TLS identity.
Plaintext не входит в production REST contract v1. Отдельный loopback-only
development profile реализован как явный вызов `infrastructure.NewLoopbackHealthServer`;
по умолчанию SDK его не создаёт. Listener принимает только TCP и только literal
loopback IP (`127.0.0.0/8` либо `::1`); hostname, wildcard и remote bind отвергаются
до прослушивания. SDK сам создаёт handler и публикует на нём только
`GET /_liapoldus/v1/health`. Любой иной path или method получает общий `404` без
identity/configuration metadata. `/ready`, identity, Manifest, schema, Reload,
metrics, admin actions и artifact stream остаются только на mTLS listener.
Config pull и secret grants остаются только на mTLS Core endpoints и их clients;
SDK отвергает plaintext Core URL. Ошибка TLS никогда не включает этот профиль
автоматически. Реализация и точный allow-list закреплены отдельным versioned
owner contract [`loopback-plaintext-profile.json`](../../../infrastructure/assets/plugin-sdk/v2/loopback-plaintext-profile.json);
полный mTLS lifecycle контракт v1 не изменён. Выбор profile остаётся явным
вызовом Plugin SDK API, а не обязанностью product plugin реализовывать TLS.

## Размещение сервисов

В v1 оператор вручную устанавливает и запускает Core и каждый plugin standalone
binary. Core регистрирует и обслуживает fixed REST endpoint каждой replica, но
не управляет процессами и контейнерами. Docker/Compose, Swarm, Kubernetes как
внешние способы размещения входят в v2; доставку releases и установку
workloads выполняет оператор. SDK не становится process manager;
будущая модель
описана в [deployment design](../core/architecture/plugin-deployment).

Ни один балансируемый endpoint не заменяет identity и ACK отдельной replica.
Подробности сетевой identity, deployment и retries задаёт
[deployment contract](../core/architecture/plugin-deployment).

## Общие библиотеки и границы

Каноническая ответственность и актуальные v1 conformance/release gates приведены в
[документе Plugin SDK и protocol](../core/architecture/protocol). `pluginprotocol`
регистрирует только произвольные application-level names, которые определяют
сами participating plugins; физический carrier/security profile настраивается
независимо от этих names. Remote workload transport требует аутентифицированной
защищённой связи. Core REST имеет отдельные trust roots и identities.

Health/readiness не становится `Ready`, пока конкретная replica не подтвердила
актуальное поколение plugin configuration. Peer-policy generations входят в
v2 peer-directory contract и сохраняются в v3. Неизвестный результат plugin Call
не повторяется автоматически; оборванный Stream закрывается. Публичная матрица
runtime-доказательств — [Core acceptance](../core/configuration/acceptance).

## Встраивание Core и монолитная композиция в v3

В v3 SDK предоставляет два явно выбираемых adapter-а одного lifecycle
contract: REST+mTLS для отдельных процессов и in-process interface для
статически скомпонованных доверенных Go plugins в процессе Core. In-process
сохраняет `Reload` metadata, scoped exact-generation config source, digest
validation, ACK и общую классификацию ошибок; он не передаёт raw settings в
`Reload` и не открывает HTTP listener.

Встроенные plugins составляют одну границу доверия и отказа с Core. SDK не
обеспечивает process isolation или Core↔plugin mTLS внутри процесса; panic containment не
защищает от исчерпания ресурсов или аварии процесса. Поэтому in-process режим
допустим только для доверенных compile-time modules. REST остаётся режимом для
отдельных/удалённых процессов, без автоматического fallback. Каноническое
решение и conformance gates описаны в
[Core protocol architecture](https://liapoldus.github.io/core/architecture/protocol).

## Саморегистрация и peer-directory в v2

Отдельная replica регистрируется у Core по REST+mTLS и периодически продлевает
lease (TTL 30 секунд, renew каждые 10 секунд). Её сертификат связывает SAN или
SPIFFE URI с `instanceId` и `replicaId`; уникальный incarnation отличает новый
процесс от умершего. Регистрация сообщает REST/peer endpoints, placement group,
SemVer, release digest, диапазоны совместимости config/state/peer contract и
подтверждённый generation. Endpoint и placement неизменны до новой incarnation.
SDK не утверждает происхождение бинарника: проверка поставки остаётся за
оператором. После потери Core replica регистрируется заново; SDK не маскирует
потерю lease как готовность.

`advertisedContracts` и `acceptedContracts` — общий plugin-owned реестр
совместимости, а не только peer API. Идентификаторы непрозрачны для SDK/Core;
plugin использует разные namespaced ID для settings schema, durable state и
peer API. Для двух разных release digest смешанная когорта допустима, только
если каждая сторона принимает все версии контрактов, объявленные другой,
диапазонами SemVer `[minimumVersion, maximumVersionExclusive)`. Одинаковый
release digest совместим без дополнительных claims. Для разных digest пустой
набор claims означает «совместимость не доказана» и закрывает смешивание.
Digest каждого объявленного контракта фиксирует точный contract artifact, но
решение о совместимости принимает диапазон; SDK/Core не интерпретируют поля
конкретного plugin.

По защищённому long-poll SDK получает versioned peer-directory: разрешённые
Core caller→target rules, размещение, endpoints, rollout cohorts и веса. Generic
resolver выбирает target replica по весу и необязательному стабильному routing
key и возвращает endpoint с названным transport. Он не импортирует
`pluginprotocol` и не выполняет вызов сам. Для `same-placement` socket-only
правила отсутствие локального target означает bounded unavailable, без
автоматического перехода на удалённый transport. Core не видит peer payload.

Точный REST wire-контракт находится в
[`peer-directory-poll.json`](../../../infrastructure/assets/plugin-sdk/v2/peer-directory-poll.json).
Первый `GET /internal/v2/plugin-peer-directory?waitMs=0` без `If-None-Match`
возвращает текущий снимок. Последующие запросы передают сильный ETag в
`If-None-Match` и могут ждать изменения не более 20 секунд; deadline запроса —
25 секунд. Изменение возвращает `200` с новым снимком и ETag, отсутствие
изменения — пустой `304` с тем же ETag. Отмена контекста отменяет ожидание на
Core; SDK не делает автоматических повторов. Каждый ответ проверяется по
ограничению 1 MiB, схеме и identity URI SAN клиентского сертификата. SDK
предоставляет `PeerDirectoryClient`, но не реализует Core endpoint, durable
registry или авторизацию caller→target.

Registration несёт неизменяемые contract claims release; SDK проверяет их
форму и предоставляет общую проверку взаимной совместимости. Core использует
результат до promotion конфигурации. Полный release rollout gate и traffic
cohort assignment остаются частью Core rollout implementation. SDK `Reload`/ACK
остаётся единственным lifecycle для отдельного процесса: Core
может разрешить прежний `previous` во время config canary, но каждая replica
применяет только одно поколение. Никаких продуктовых полей, правил Caddy или
форм в общий SDK не добавляется.

### Peer-directory: точный контракт и выбор replica

Каноническая JSON Schema: [`peer-directory.schema.json`](../../../infrastructure/assets/plugin-sdk/v2/peer-directory.schema.json).
Её версия `liapoldus.plugin-sdk.peer-directory.v1` независима от версии REST
lifecycle SDK. Все перечисленные поля обязательны, кроме явно помеченных ниже;
неизвестные поля запрещены. `links`, `requiredPeerContracts`, `replicas` и
`peerContracts` передаются как массивы даже при отсутствии элементов. Размер
сырого JSON ограничен 1 MiB, directory TTL — 30 секунд, максимум 256 links,
512 replicas на link и 64 contract descriptors на link/replica.

```json
{
  "contractVersion": "liapoldus.plugin-sdk.peer-directory.v1",
  "generation": "directory-42",
  "issuedAt": "2026-10-06T10:00:00Z",
  "expiresAt": "2026-10-06T10:00:30Z",
  "caller": {
    "instanceId": "server",
    "replicaId": "server-1",
    "incarnationId": "server-inc-1",
    "placementId": "node-a"
  },
  "links": [{
    "linkId": "forms-remote",
    "targetInstanceId": "forms-db",
    "placementRule": "remote",
    "carrier": "quic",
    "securityProfile": "mtls",
    "requiredPeerContracts": [{
      "contractId": "org.example.forms-api",
      "minimumVersion": "1.2.0",
      "maximumVersionExclusive": "2.0.0"
    }],
    "replicas": [{
      "identity": {
        "instanceId": "forms-db",
        "replicaId": "forms-2",
        "incarnationId": "inc-2",
        "placementId": "node-b"
      },
      "endpoint": "forms-2.internal:9443",
      "eligibility": "ready",
      "eligibleUntil": "2026-10-06T10:00:25Z",
      "weight": 3,
      "peerContracts": [{
        "contractId": "org.example.forms-api",
        "version": "1.4.0",
        "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
      }]
    }]
  }]
}
```

`generation` — непрозрачный идентификатор снимка; `issuedAt`/`expiresAt` и
`eligibleUntil` — RFC 3339 timestamps. Для неготовой replica `endpoint` обязан
быть пустой строкой и `eligibleUntil` — `null`; допустимы состояния
`not-ready`, `draining`, `lease-expired`, `fenced` и `incompatible` наряду с
`ready`. `weight` обязателен в диапазоне 1..100. `securityProfile` пока имеет
единственное значение `mtls`, поэтому downgrade в directory не кодируется.
Пустой `links: []` — допустимый fail-closed снимок: он означает, что caller не
разрешён ни один peer link. Запрос любого `linkId` в таком снимке завершается
`ErrPeerLinkNotFound`; отсутствие разрешения не подменяется ошибкой формата и
не приводит к поиску другого link или transport.

`placementRule=same-placement` допускает только `unix` либо
`windows-named-pipe`; готовая replica обязана иметь тот же `placementId`, что
caller. `remote` допускает только `tcp` либо `quic`, и готовая replica обязана
находиться в другом placement. Локальный endpoint — абсолютный нормализованный
socket path или именованный канал Windows; remote endpoint — `host:port`.
Указанный carrier является обязательным выбором, а не предпочтением: SDK не
переключается на иной carrier или link при отсутствии подходящей replica.

`contractId` — opaque identifier без product-specific семантики SDK. Диапазон
версии — ASCII SemVer 2.0.0 (до 128 байт) с полуоткрытыми границами
`[minimumVersion, maximumVersionExclusive)`; `minimumVersion` должна быть меньше
верхней границы. Replica подходит, только если объявляет каждый требуемый
`contractId` с версией внутри соответствующего диапазона. SHA-256 — digest
неизменяемого contract document; для одинаковой пары contract ID/version
directory не может объявлять разные digests. Core включает link/candidate в
directory только для caller, которому разрешён этот target; `ready` не отменяет
повторную проверку lease и contract ranges в SDK.

Публичный pure API SDK: `models.ParsePeerDirectory(raw)` для ограниченного
строгого JSON parsing и `application.ResolvePeer(directory, request)` для
выбора endpoint. Запрос содержит `linkId`, явное время `now` и
`selectionOrdinal`, если `stableRoutingKey` отсутствует. При наличии обоих
полей приоритет у stable key; ключ ограничен 256 UTF-8 байтами.
JSON request DTO имеет поля `linkId: string`, `now: RFC3339 string`,
`stableRoutingKey?: string` и `selectionOrdinal?: uint64`; пустой или
отсутствующий ключ требует ordinal. Успех возвращает DTO `ResolvedPeer` с
полями `directoryGeneration`, `linkId`, `targetInstanceId`, `replicaId`,
`incarnationId`, `placementId`, `carrier`, `securityProfile`, `endpoint` и
`eligibleUntil` (RFC 3339). Это структуры SDK API, не сетевой endpoint и не
новый Core wire API.
Resolver принимает только интервал `issuedAt <= now < expiresAt`, выбирает
ровно названный link и отбрасывает не-`ready`, просроченные и несовместимые
replicas. Без кандидатов возвращается `ErrNoEligiblePeer`; другие link/carrier
не пробуются. Без stable key replicas сортируются по `(replicaId,
incarnationId)` и `selectionOrdinal % sum(weights)` выбирает взвешенный слот.
Со stable key SDK вычисляет SHA-256 score для каждого virtual ticket
`0..weight-1` из length-prefixed UTF-8 значений key/link/target/replica/
incarnation плюс uint16 ticket; выбирается максимальный score, при равенстве —
лексикографически меньшая identity. Это weighted rendezvous routing, не
round-robin state и не сетевой retry. SDK возвращает поколение, идентичность,
carrier, endpoint и срок eligibility, но сам соединение не открывает.

### Replica registration и lease

Канонический контракт DTO и размеров: [`replica-lifecycle.json`](../../../infrastructure/assets/plugin-sdk/v2/replica-lifecycle.json);
JSON Schema тел запросов/ответов: [`replica-lifecycle.schema.json`](../../../infrastructure/assets/plugin-sdk/v2/replica-lifecycle.schema.json).
Все три endpoint-а доступны только по HTTPS с обязательным взаимным TLS;
Bearer-only и plaintext варианты отсутствуют. Core обязан проверить SAN URI
клиентского сертификата по шаблону
`spiffe://liapoldus/plugin/{instanceId}/{replicaId}/{incarnationId}` и сравнить
его с `identity` в теле. Common Name не является идентификатором. SDK проверяет
ту же связь локально до отправки запроса.

Плагин создаёт `ReplicaLifecycleClient` через
`infrastructure.NewReplicaLifecycleClient(contract, coreURL, mutualTLS, identity)`
и вызывает `Register`, `Renew` и `Deregister`. `Register` передаёт release
SemVer+SHA-256, неизменяемые REST/peer endpoints, placement, advertised
contract versions и принимаемые полуоткрытые SemVer ranges. `Renew` обновляет
только applied generation и readiness. `Deregister` снимает конкретную
incarnation. Неизвестные JSON-поля отклоняются; размеры тел и число endpoint-ов
ограничены contract asset. Вызовы имеют отдельный deadline, не повторяются
автоматически и не replay-ятся после неопределённого сетевого результата.

Lease живёт 30 секунд и продлевается каждые 10 секунд. Время Core из ACK —
авторитетное; SDK проверяет, что expiry позже server time и не превышает TTL.
После истечения lease replica fenced и не eligible. Renew истёкшей incarnation
возвращает `409 lease_expired`; reconnect выполняется как новая регистрация с
новым incarnation и сертификатом. В рамках одной incarnation Core запрещает
менять endpoint, placement, release и compatibility metadata; такие изменения
требуют нового incarnation. Повторная активная регистрация с тем же identity и
неизменяемыми полями продлевает lease. Deregister для неизвестной/уже снятой
incarnation возвращает `404 replica_not_registered`; снятая replica не
восстанавливается renew-ом.

Поверхность SDK проверена реальным дочерним процессом: fixture создаёт CA и
сертификаты, обращается к mTLS Core stub, проверяет register/renew/expiry,
reconnect, body/certificate identity spoofing, неизменяемые поля, ошибочные
SemVer/ranges и deregister. Это подтверждает SDK-клиент и DTO; Core production
handler, долговременное lease-хранилище и доставка peer-directory остаются
интеграционными обязанностями Core и этим тестом не считаются реализованными.
