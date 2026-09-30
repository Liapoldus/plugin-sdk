# TODO — Plugin SDK v1

## Документация

- [x] Общие Plugin SDK/lifecycle/Admin Surface Markdown и Mermaid исходники
  принадлежат этому repo в `docs/site/`; агрегатор собирает закреплённую
  ревизию, не поддерживая редактируемую копию.
- [ ] После изменения owner docs обновить pin в
  `liapoldus.github.io/docs-sources.json` и проверить единый сайт.

Нормативная цель: [Core target](https://liapoldus.github.io/core/architecture/target),
[v1 acceptance](https://liapoldus.github.io/core/configuration/acceptance) и
[Core↔plugin REST boundary](https://liapoldus.github.io/core/architecture/protocol).
Этот Go module — общий SDK для создания plugin services и единственный владелец
Core↔plugin REST lifecycle contracts.

`go.mod` использует временный local module path `liapoldus.local/plugin-sdk`.
Git remote задан как `https://github.com/Liapoldus/plugin-sdk.git`; canonical
Go module path остаётся временным до отдельного решения владельца. Не менять
imports автоматически.

Этот документ — reconciled status v1-среза: каждый исходный пункт помечен
`[x]` только вместе с исполняемым доказательством, `⚠️` — реализовано, но
исполняемой проверки нет, `[ ]` — открыто с указанной причиной.

## Целевая ответственность

SDK предоставляет общие plugin REST endpoints/clients и reusable API для
Manifest, opaque config schema, health/readiness/registration,
`Reload(generation)`, exact config pull, digest ACK, scoped secret redemption,
metrics, structured logging, typed safe errors и mutual TLS. SDK не знает
конкретные product fields, capabilities, providers, DB drivers или public
routes.

Ровно четыре слоя: `domain/`, `application/`, `infrastructure/`,
`presentation/`; `domain/` содержит только `models/` и `interfaces/`.
Зависимости направлены внутрь, `presentation` не импортирует `infrastructure`,
поэтому composition живёт в `main` плагина. SDK не импортирует Core,
`pluginprotocol`, Caddy либо другой plugin: production-код использует только
четыре слоя и standard library. Общий versioned HTTP contract — только
`infrastructure/assets/plugin-sdk/v1/http-contract.json`; paths/status/limits/
schema не копируются hardcode-строками по consumers.

## Зафиксировано

- [x] Четыре production layers; TypeScript gate направления импортов; domain
  разделён на `models/` и `interfaces/`.
- [x] Versioned REST HTTP contract; базовый flow
  `Reload → exact-generation pull → digest → plugin applier → ACK`.
- [x] Child-process HTTP conformance на exact bytes/digest, health/readiness,
  Manifest/schema, metrics, duplicate keys, oversized request и apply failure.
- [x] mTLS client/listener primitives с client certificate и pinned trust pool;
  fixture отклоняет anonymous peer.
- [x] JSON syntax/duplicate-key/size/generation path validation до plugin
  applier; SDK передаёт сохранённые config bytes без remarshal.

## P0 — lifecycle contract и Core consumer

- [x] Полный REST bootstrap/identity contract. Versioned asset регистрирует
  identity, registration, manifest, configSchema, health, ready, reload и
  metrics с уникальным method/path; обязательные поля документов, лимиты,
  статусы и коды объявлены и проверены. `models.Registration` валидирует
  `contractVersion`, `instanceId`, `replicaId`, `appliedGeneration`, `ready`.
  Evidence: `contract.test.ts` («registers every plugin endpoint once»,
  «names the required members of every document»), `reload-pull.test.ts`
  («serves the registration fields the contract requires»). Реализация
  Core-side identity — работа репозитория Core, здесь не выполняется.
- [x] Production-grade SDK client и server facade. `NewPluginClient`,
  `NewCoreConfigurationSource`, `NewCoreSecretBroker`, `NewMutualTLSServer`,
  `NewMutualTLSClient`: bounded reads по contract limit, per-operation
  deadlines, cancellation, request/response validation, `CloseIdleConnections`,
  safe `controlPlaneError` без cause/адреса/документа, типизированные outcome.
  `performControlCall` классифицирует transport только по context и никогда не
  парсит строку ошибки. Evidence: 176 исполняемых тестов, включая «refuses a
  descriptor past the contract request limit, at exactly one byte over».
- [x] Bootstrap сертификата для вручную запускаемого v1. Trust roots и
  identities принадлежат оператору: SDK не встраивает CA и не генерирует
  сертификаты. `LoadCredentials` валидирует trust pool, роль keypair,
  соответствие private key и минимальный размер ключа;
  `NewStaticCredentialsProvider` / `NewFileCredentialsProvider` обслуживают
  набор и ротацию; `NewRevocation` fail-closed на недоступном/истёкшем/
  неподписанном CRL; `ErrPeerCertificateExpired` проверяет окно validity.
  Revocation и single pinned peer identity вычисляются на handshake.
  Evidence: `security.test.ts` («requires a client certificate and pins the
  peer identity», «refuses a client that presents no certificate», «refuses a
  peer whose certificate the revocation list names», «refuses a handshake
  below the TLS floor», «refuses a bearer token in place of a client
  certificate»), `reload-pull.test.ts` («does not answer a caller that does not
  trust the control-plane authority»). Разрыв доказательства ротации вынесен в
  P2.1.
- [x] Exact-generation pull API. `PullExact` требует bare HTTPS origin, строит
  единственный pull-path с percent-encoded generation, не перечисляет и не
  подставляет поколение, требует все зарегистрированные response headers и на
  этапе конструирования, и на ответе, отображает только `active`/`previous`,
  и отдаёт байты без remarshal. Descriptor сверяется с объявлением и с
  самими байтами. Evidence: «names the pull path the contract declares and
  percent-encodes the generation», «answers a mutual-TLS caller the exact
  bytes, under every header the contract names», «publishes only the two
  durable slots», «refuses a generation Core publishes as the previous slot»,
  «refuses bytes whose digest is not the digest Core announced», «applies a
  pull response at exactly the contract document limit, byte for byte»,
  «refuses a generation that leaves the generation namespace».
- [x] ACK только после успешного plugin-owned validation и атомарного apply.
  Политика `Lifecycle.Reload` (синтаксис → idempotent repeat → conflict →
  pull → desired-state → digest/schema → applier); предыдущая конфигурация
  остаётся рабочей при отказе; readiness не подтверждает отказанное поколение,
  а публикует его в `pendingGeneration`. Evidence: «applies the announced
  generation and only then acknowledges it», «acknowledges a byte-for-byte
  repeat of the active descriptor without pulling», «refuses a descriptor that
  contradicts the active generation under the same name», «refuses a document
  the plugin's own applier rejects, leaving the last one active», «fences the
  replica with the generation it refused, without claiming it», «keeps the
  applied generation usable through every pull refusal», «answers every outcome
  the contract names for reload» (15 outcomes, у каждого свой status и код).
- [x] Generic scoped secret retrieval. `SecretManager.IssueGrant`, `Redeem`,
  `SecretProvider`; grant ограничен instance/replica, применённым generation;
  внутри `ConfigurationApplier.Apply` разрешён только обрабатываемый candidate
  generation, с возвратом к active generation после apply. Это позволяет
  атомарно построить candidate repository/runtime с DSN или TLS secret до ACK,
  не разрешая произвольные generation. Grant ограничен
  purpose, погашается один раз, второе погашение отклоняется локально без
  второго вызова Core; значение отдаётся с явным `Destroy`; SDK не
  интерпретирует reference и не пишет secret bytes. Evidence: 18 тестов
  `secrets.test.ts`, включая «redeems a grant exactly once and reports the
  second attempt as spent», «never puts a handle, a value, a reference or a
  purpose in any answer», «refuses a grant before Core has reloaded the
  replica», «returns the secret bytes to the plugin as a count and a digest»,
  «restores the ordinary path once the fault is cleared».
- [x] Cancellation/deadline/disconnect и невозможность replay. Отдельный
  contract-deadline на каждую операцию, классификация только по context
  (`cancelled`, `deadlineExceeded`), сериализация apply, отсутствие внутреннего
  retry/replay, one-use учёт grant-ов. Evidence: «reports cancelled when Core
  answers a pull Core cancels», «reports deadlineExceeded», «reports
  coreUnavailable», «never applies a generation a refused request announced»,
  «keeps the applied generation usable through every pull refusal».

## P1 — plugin author experience и observability

- [x] Простой consumer API регистрации. `presentation.HandlerConfiguration` +
  `NewHandlerSet` принимают contract и по одному значению на порт
  (`Lifecycle`, `Readiness`, `Registration`, `Metadata`, `Metrics`);
  `application.NewLifecycle` требует source, applier, identity и observer.
  Автор пишет `ConfigurationApplier`, `PluginMetadata` и, при необходимости,
  `Clock`/`Logger`; generic HTTP/mTLS boilerplate не пишется. `Handler()`
  отдаёт multiplexer и сам не слушает.
- [x] Registry startup порядок и конфликтующие routes/handlers до listen.
  `NewHandlerSet` валидирует contract и требует все порты до bind, а
  presentation строит routes только из asset, поэтому дубликат маршрута
  невозможен по построению; metrics exposition с несовпадающим media type
  отказывает сборку. Evidence: «registers exactly the endpoints the contract
  names, with no others», «publishes no process control, no plugin-side
  rollback and no test control route», «refuses a read of the reload endpoint,
  which accepts only a write», «answers every path outside the contract with
  the transport not-found refusal».
- [x] Prometheus scrape contract. `NewObserverPrometheusCollector` рендерит
  exposition в зарегистрированный media type с детерминированными именами и
  help-строками; `NewRecorder` держит `AllowedKinds` и `MaximumKindLength` и
  не принимает document, secret value или grant handle как label. Evidence:
  «serves Prometheus text under the media type the contract names», «names
  every metric and every help line the contract publishes», «reports readiness
  and every bounded outcome it has recorded», «counts a refusal the
  presentation layer answers before any pull as no pull failure», plus
  source-of-truth gate на metric names в production Go.
- [x] JSON stdout/stderr structured logger. `NewJSONLogger` +
  `application.NewLoggingObserver` с contract `RedactedPlaceholder`,
  `MaximumFields`, `MaximumKeyLength`, `MaximumValueLength`; redaction
  применяется до сериализации. Evidence: «publishes one bounded JSON
  lifecycle event per outcome and no secret material», «records exactly one
  event per lifecycle outcome it reports», «keeps a log line inside the bounds
  the contract publishes», «never repeats a product setting, a document or an
  address in a refusal».
- [x] Public-safe errors. `application.OutcomeError` и `OutcomeOf` дают
  типизированную классификацию; внутренний cause не сериализуется клиенту;
  наружу уходят outcome и зарегистрированный contract-ом code. Evidence:
  «publishes only the outcome and the code the contract defines», «names a
  transport refusal with a code the contract publishes», «gives every
  transport refusal a status and a unique snake_case code».

## P2 — conformance и публикационная готовность

- [x] Child-process tests: покрыты manual startup (отдельный fixture-процесс),
  mTLS client certificate required, anonymous peer, revoked peer с тем же
  authority/name/usage, bearer вместо сертификата, недоверенный authority,
  TLS floor, plaintext control URL, repeated/stale/conflicting Reload, exact
  pull со всеми headers, malformed/duplicate-key/oversized JSON, digest
  mismatch, schema version mismatch, apply failure, cancellation, deadline,
  coreUnavailable, metrics и readiness, secret grant one-use/expired/denied/
  unknown/notPermitted/spent. Пять сценариев, которые раньше оставались только
  на чтение contract, теперь исполняются против живого дочернего процесса:
  (1) trusted, но wrong-identity peer — `test_tls.go` выпускает второй сертификат
  под тем же authority, с тем же CN и usage, но другим URI-префиксом replica,
  и `security.test.ts` («refuses a peer that presents a different replica
  identity») требует, чтобы отказ пришёл от самого listener; (2) истёкший peer
  certificate — «refuses a peer whose certificate has expired»; (3) ротация
  credentials на диске — `credentials-rotation.test.ts` пишет новый material во
  временный каталог, проверяет, что listener обслуживает новый serial на
  проводе, не понижая поверхность до plaintext/anonymous, и отказывает после
  drain; (4) reconnect и close race — `connection-recovery.test.ts` роняет
  соединения под нагрузкой, требует, чтобы ни один результат не был применён,
  поверхность продолжила обслуживать, и ни одно соединение не осталось
  открытым; (5) graceful shutdown — `graceful-shutdown.test.ts` требует, чтобы
  запрос in flight получил обещанный ответ, после чего listener остановился.

  Отдельно проверена нетавтологичность mTLS-проверок. Verdict в
  `testMutualTLSRefusesCredential` читается по тому, появился ли HTTP-ответ
  вообще, а не по его status-коду: handshake-gate работает внутри handshake и
  не может отказать после того, как появилось что отвечать, а readiness до
  подтверждения поколения отвечает 503, и чтение status-кода объявляло бы любой
  предъявленный credential отвергнутым. Проверка подменялась на подстановку
  живого Core credential в каждый из трёх probe'ов — revoked, wrong-identity и
  expired — и каждый раз соответствующий тест падал, подтверждая, что флаги
  означают отказ listener'а, а не константу.
- [x] Локальные test CA/test credentials живут только в fixture. Production
  packages не содержат insecure listener, self-signed bootstrap CA или
  certificate-generation shortcut; plaintext mirror и harness control listener
  существуют только в `tests/fixtures/reload-runtime/`. Evidence: gate
  направления импортов, `source-of-truth.test.ts` («keeps every product string
  the tests own out of the production tree», «imports only the four layers and
  the standard library from production code»), «serves the replica's own
  surface on a separate plaintext test mirror only».
- [ ] Завершить сквозную интеграцию SDK с Core, Server и forms-db. Core уже
  содержит REST composition/per-replica clients; `core make check` и Core
  `go vet ./...` прошли 2026-09-30. Это не закрывает consumer migration:
  текущий `go test ./server/... ./forms-db/...` падает из-за оставшихся импортов
  удалённых `pluginprotocol/pluginv1` и `pluginprotocol/presentation/sdk`; Server
  дополнительно содержит REST adapter, который не компилируется с публичным SDK
  API (`ServerConfiguration`, route/error mapping и `NewLifecycle` signature).
  Не оставлять permanent gRPC/REST dual mode. Тестовый дубль маршрута удалён
  в том же coordinated change: `legacyReload` и
  `testRouteLegacyReload` не имели ни одного consumer во всём workspace — ни
  один тест в SDK, `core` или `liapoldus.github.io` к ним не обращался, — и
  больше не существуют; зарегистрирован единственный путь `control.reload`.
  Требуемый результат — реально собираемые consumers и Core→SDK→Server/forms-db
  child-process smoke, а не только SDK и Core unit/contract gates.
- [ ] Проверить macOS и Linux вручную запускаемые plugins. Исполняемый suite
  прогнан на локальной macOS-машине; Linux-доказательств нет, поэтому заявлять
  обе платформы нельзя. Заявление о platform coverage появится только вместе с
  исполняемым прогоном. В v1 binaries устанавливает и запускает оператор: SDK
  не управляет process lifecycle и не обращается к container API.
- [ ] После решения владельца заменить временный module path и мигрировать Core
  и plugin imports согласованно. Git repository/remote уже созданы; релиз,
  license и CI остаются отдельными owner decisions.

## Отложено до v2: embedding и in-process adapter

- [ ] Сохранить REST+mTLS adapter для plugin processes и добавить явно
  выбираемый in-process adapter с теми же lifecycle models и observable
  semantics; не вводить fallback.
- [ ] Реализовать scoped in-memory `ConfigSource`, доступный только plugin
  instance, чей immutable host binding его создал; не читать Core SQLite из
  adapter и не выдавать staging generation.
- [ ] Обеспечить Reload/pull/apply/ACK, digest/schema validation, grants,
  cancellation и безопасные errors одинаково через оба adapter-а.
- [ ] Закрепить, что in-process предназначен только доверенным статически
  скомпонованным Go plugins: process isolation и Core↔plugin mTLS отсутствуют,
  независимое обновление требует пересборки host binary.
- [ ] Общий TypeScript conformance corpus пройти на REST child-process и
  in-process adapters; добавить host-process smoke для listener absence,
  instance scope, panic containment и graceful shutdown.

## Definition of Done

Проверка 2026-09-30 на локальной macOS. `make check` проходит целиком: `npx vitest run`
— 10 файлов, 176 тестов (layers 3, contract 20, source-of-truth 14, security 16,
secrets 18, reload-lifecycle 25, reload-pull 73, credentials-rotation 3,
connection-recovery 3, graceful-shutdown 1), затем `go build ./...` и
`go vet ./...` без ошибок; `staticcheck ./...` не находит замечаний и
`gofmt -l .` пуст. Исполняемый прогон — локальная macOS-машина.

SDK остаётся product-agnostic, не зависит от `pluginprotocol` и не содержит
второй lifecycle-модели: production-дерево проверено на отсутствие
plugin-side rollback, ровно один `Reload` use case, ровно один reload handler и
ноль product-specific строк.

Открытые gates, которые не дают объявить SDK production-ready: P2.3
(интеграция с Core, `plugins/server` и `plugins/forms-db` по их owner tasks),
P2.4 (Linux-прогон) и P2.5 (решение владельца по module path). P2.1
закрыт: пять сценариев исполняются против живого процесса, и нетавтологичность
каждой mTLS-проверки подтверждена подстановкой живого credential. Секретов в fixtures, логах, errors, metrics и ACK нет;
временные credentials фикстуры — test-only и не являются credentials
эксплуатируемого развёртывания.
