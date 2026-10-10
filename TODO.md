# TODO — Plugin SDK v2

## Повторная проверка — 2026-10-04

Проверка до релиза: `make check` прошёл (14 файлов / 190 тестов),
`GOWORK=off go build ./...`, `GOWORK=off go vet ./...` и
`git diff --check` прошли. Актуальное опубликованное состояние приведено в
дополнении от 2026-10-05 ниже.

Дополнительная Linux-проверка 2026-10-04: в Ubuntu 24.04.5 ARM64 VM под
OrbStack повторно прошёл `make check` (14 файлов / 190 тестов), включая Go
build/vet. Это Linux VM runtime evidence; hosted CI и опубликованные module
versions на дату проверки ещё не были подтверждены. Отдельный bare-metal host
не требуется для v1 Linux runtime gate.

## Документация

- [x] Общие Plugin SDK/lifecycle/Admin Surface Markdown и Mermaid исходники
  принадлежат этому repo в `docs/site/`; агрегатор собирает закреплённую
  ревизию, не поддерживая редактируемую копию.
- [x] После изменения owner docs pin обновлён в
  `liapoldus.github.io/docs-sources.json`, единый сайт собран и развёрнут.

## Актуальная проверка — 2026-10-02

Дополнение 2026-10-03: повторное объявление уже активного поколения после
отказанного candidate очищает `pendingGeneration`; целевой
`tests/integration/reload-lifecycle.test.ts` прошёл 26/26. Полный SDK gate
после этой правки прошёл: `make check` 14 файлов / 190 тестов,
`go build ./...` и `go vet ./...`.

`make check` прошёл: 14 Vitest files / 189 tests, включая artifact streaming и
Admin Action suites; Go build и vet прошли;
`GOWORK=off go vet ./...` и `git diff --check` также PASS. Настоящий
Core→Server/forms-db child-process smoke пройден. Ранее приведённые в этом
файле ошибки consumer-компиляции и незакрытый Core integration gate относятся
к устаревшему состоянию и не являются текущими TODO. На момент этой проверки
оставались Linux runtime, hosted CI, опубликованные module versions и общий
release gate; актуальная Linux-проверка 2026-10-04 зафиксирована выше, а все
внешние release gates закрыты дополнением 2026-10-05.

Нормативная цель: [Core target](https://liapoldus.github.io/core/architecture/target),
[v1 acceptance](https://liapoldus.github.io/core/configuration/acceptance) и
[Core↔plugin REST boundary](https://liapoldus.github.io/core/architecture/protocol).
Этот Go module — общий SDK для создания plugin services и единственный владелец
Core↔plugin REST lifecycle contracts.

Владелец утвердил canonical module path `github.com/Liapoldus/plugin-sdk/v2`
2026-09-30. `go.mod`, SDK imports, Core, Server и forms-db consumers переведены на него.
Не выпускать SDK до сквозных gates. Git remote:
`https://github.com/Liapoldus/plugin-sdk.git`.

Этот документ — reconciled status v2-среза: каждый исходный пункт помечен
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
`infrastructure/assets/plugin-sdk/v2/http-contract.json`; paths/status/limits/
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
  парсит строку ошибки. Контекст HTTP-вызова остаётся активным до закрытия
  response body; отдельный regression test воспроизводил обрыв между headers
  и чтением schema. Evidence: 178 исполняемых тестов, включая «refuses a
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
  а публикует его в `pendingGeneration` и отвечает `notReady` до успешного
  повторного Reload/идемпотентного объявления active поколения. Evidence:
  «applies the announced
  generation and only then acknowledges it», «acknowledges a byte-for-byte
  repeat of the active descriptor without pulling», «refuses a descriptor that
  contradicts the active generation under the same name», «refuses a document
  the plugin's own applier rejects, leaving the last one active», «fences the
  replica with the generation it refused, without claiming it», «keeps the
  applied generation usable through every pull refusal», «answers every outcome
  the contract names for reload» (15 outcomes, у каждого свой status и код).
  После rollback idempotent reannouncement активного descriptor очищает
  `pendingGeneration`; это закреплено отдельным TypeScript regression test.
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
- [x] Добавить отдельный mTLS REST streaming endpoint для bounded artifact
  transfer Core→plugin. SDK передаёт metadata и один artifact с
  cancellation/backpressure, digest/byte limits и typed receipt; product
  archive/site validation принадлежит Server plugin. Проверено SDK
  `artifact-stream.test.ts` и реальным Core→SDK→Server child-process publish:
  multipart без `If-Match` для первой публикации, receipt `202`, durable
  operation, serving опубликованного сайта и сохранность после Server restart.
- [x] Опубликовать и реализовать общий SDK REST контракт Admin Surface
  discovery/action delivery, включая mTLS identity, bounds и typed errors.
  Product pages/actions/schema принадлежат plugins; удалённый
  `pluginprotocol/contracts/admin-ui` не восстанавливался. Проверено SDK
  `admin-surface-actions.test.ts` и Core→Server forwarding suites. Для
  синхронных JSON Actions SDK передаёт `Idempotency-Key` как opaque correlation
  metadata и не кеширует результат; повторное выполнение, CAS и domain effects
  принадлежат Core/plugin контрактам. Artifact actions используют отдельную
  durable operation idempotency модель.

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
- [x] Завершить сквозную интеграцию SDK с Core, Server и forms-db. На 2026-10-02
  Core, Server и forms-db suites проходят; Core→Server/forms-db child-process
  smoke проверяет настоящий SDK REST/mTLS, exact pull/ACK, Server traffic,
  rollback, artifact publish и persistence после рестартов. Повторный Linux
  runtime suite и прямой production E2E прошли в Ubuntu guest под OrbStack;
  на момент записи hosted CI оставался отдельным gate; он прошёл 2026-10-05.
  Удалённый `pluginprotocol/pluginv1` lifecycle не восстановлен; `control.reload`
  остаётся единственным plugin reload route.
- [x] Проверены вручную запускаемые plugins на macOS и в Linux VM runtime:
  Core→Server/forms-db child-process E2E прошёл на обеих средах, включая Linux
  guest под OrbStack. В v1 binaries
  устанавливает и запускает оператор; SDK не управляет process lifecycle и не
  обращается к container API.
- [x] Перевести `go.mod`, SDK, Core и Server imports на утверждённый
  `github.com/Liapoldus/plugin-sdk/v2`; локальные SDK/Core builds проходят.
- [x] Подключить forms-db к SDK: `go build ./...`, `go vet ./...` и plugin
  TypeScript suite прошли 2026-10-01; child-process SDK REST/mTLS и прямой
  Server→forms-db peer/HTTP маршрут подтверждены тестом с двумя процессами.
- [x] License metadata: root `LICENSE` declares MIT; the module has no external
  Go module requirements, so there is no separate dependency-license inventory
  for this SDK module.
- [x] Hosted CI на согласованной опубликованной ревизии и release provenance:
  macOS/Ubuntu tag CI прошли; SDK `v1.0.1` и VitePress pins опубликованы.

## V2 — регистрация replicas и rollout (вне v1 gates)

- [x] Owner contract и SDK-клиент REST registration/renew/deregister с per-replica mTLS,
  SAN/SPIFFE binding, immutable incarnation/endpoints/placement, SemVer,
  release digest и generic opaque contract claims. Pairwise release compatibility
  требует взаимного принятия всех advertised versions для разных digest и
  закрывается при отсутствии evidence. Lease 30 s, renew 10 s; после
  истечения fenced до новой регистрации. DTO/schema: `infrastructure/assets/plugin-sdk/v2/replica-lifecycle{,.schema}.json`;
  API и semantics: `docs/site/plugins/architecture.md`. Gate run by TS child-process
  fixture covers registration, expiry, reconnect, spoofing, immutable metadata,
  same/mixed-release compatibility, `replica_not_registered` для неизвестной и
  повторно снятой incarnation, локальный отказ по `maximumContractsPerList` и
  `maximumPeerEndpoints`, а также cancellation: отменённый register/renew/deregister
  падает без replay и не оставляет следа на стороне Core.
  Evidence: `tests/integration/replica-lifecycle.test.ts`.
- [x] Versioned peer-directory v1 DTO/schema, строгая bounded JSON validation
  и deterministic resolver: placement/carrier rules, lease/contract eligibility,
  ordinal weighting и stable-key weighted rendezvous без transport fallback.
  Bounds проверены через реальный дочерний процесс: >1 MiB документ, >256 links,
  >512 replicas,   >64 contracts, weight вне 1..100, TTL >30 s, routing key >256 B,
  дубликаты linkId и конфликтующие digests.
  Контракт и compatibility semantics: `docs/site/plugins/architecture.md`;
  schema: `infrastructure/assets/plugin-sdk/v2/peer-directory.schema.json`.
  Evidence: TS integration tests under `tests/integration/peer-directory.test.ts`.
- [x] Зафиксировать owner wire contract и SDK client для защищённого
  peer-directory long-poll: initial snapshot, strong ETag/`If-None-Match`,
  `200` при смене, пустой `304` без изменений, wait до 20 s, request deadline
  25 s, cancellation без automatic retry, лимит ответа 1 MiB и binding caller
  к URI SAN клиентского сертификата. Источник:
  `infrastructure/assets/plugin-sdk/v2/peer-directory-poll.json`; client:
  `infrastructure.PeerDirectoryClient`; TS child-process conformance:
  `tests/integration/replica-lifecycle.test.ts` и
  `tests/integration/peer-directory-poll.test.ts`.
- [ ] Cross-repository Core↔SDK process gate: Core закреплён на опубликованном
  Plugin SDK `v1.1.0`, содержащем используемые registration/peer-directory APIs;
  независимый `GOWORK=off GOFLAGS=-p=1 go build ./...` в Core прошёл 2026-10-09.
  Тем самым прежний dependency/build blocker закрыт, но conformance не завершён.
  Открыты сквозные проверки Core authority per caller, stale incarnation,
  expiry/reconnect, restart recovery, revoked identity и отсутствия carrier
  fallback на реальных child processes и закреплённых revisions. SDK
  предоставляет client contract, но не реализует Core endpoint, не делает peer
  Call и не зависит от `pluginprotocol`.
- [x] Добавить generic pairwise release-cohort compatibility поверх opaque
  `advertisedContracts`/`acceptedContracts`, не связывая SDK с продуктом.
  `models.ReleaseCohortCompatible` проверяется child-process fixture для
  same digest, взаимных диапазонов, одностороннего acceptance и отсутствующего
  evidence. Core activation/rollout orchestration и partial ACK/drain остаются
  владельческой работой Core, не SDK.

## V2 — явно настраиваемые transport security profiles

- [x] Opt-in plaintext profile для SDK REST ограничен development loopback TCP.
  Default выключен: отдельный listener создаётся только явным вызовом SDK API;
  API не принимает произвольный handler и сам обслуживает только generic health.
  Bind требует literal loopback IP и SDK отклоняет hostname, wildcard и remote
  IP, а `Serve` повторно проверяет фактически bound listener. Любые другие path
  и method завершаются общим `404`. Production mTLS HTTP contract v1 не менялся;
  config pull/secret-grant clients и endpoints остаются на mTLS, TLS failure не
  включает plaintext fallback. Versioned profile:
  `infrastructure/assets/plugin-sdk/v2/loopback-plaintext-profile.json`;
  child-process conformance: `tests/integration/loopback-plaintext.test.ts`.
  Документация: `docs/site/plugins/architecture.md` и `development.md`.
  Local evidence: `make check` passed (19 Vitest files / 227 tests,
  `go build ./...`, `go vet ./...`); targeted config-pull gate
  `npx vitest run tests/integration/reload-pull.test.ts` passed (74 tests).
  `git diff --check` passed. Это локальная проверка, не hosted CI/release gate.

## V3 — embedding и in-process adapter

- [x] Сохранить REST+mTLS adapter для plugin processes и добавить явно
  выбираемый in-process adapter с теми же lifecycle models и observable
  semantics; не вводить fallback. REST child-process suite и
  `infrastructure/in_process_lifecycle_test.go` подтверждают одинаковые
  applied/duplicate/stale outcomes.
- [x] Реализовать scoped in-memory `ConfigSource`, доступный только plugin
  instance, чей immutable host binding его создал; не читать Core SQLite из
  adapter и не выдавать staging generation. Проверки exact active/previous,
  scope, atomic publish, invalid digest и cancellation находятся в
  `infrastructure/in_process_config_source_test.go`.
- [x] Обеспечить Reload/pull/apply/ACK, digest/schema validation, grants,
  cancellation и безопасные errors одинаково через оба adapter-а; lifecycle
  conformance покрывает exact digest, duplicate idempotency и stale refusal.
- [x] Закрепить, что in-process предназначен только доверенным статически
  скомпонованным Go plugins: process isolation и Core↔plugin mTLS отсутствуют,
  независимое обновление требует пересборки host binary.
- [ ] Общий TypeScript conformance corpus пройти на REST child-process и
  in-process adapters; добавить host-process smoke для listener absence,
  instance scope, panic containment и graceful shutdown.

## Definition of Done

Проверка 2026-10-02 на macOS: `make check` — 14 Vitest files / 189 tests,
`go build ./...` и `go vet ./...`; отдельный `GOWORK=off go vet ./...` тоже
проходит. Актуальный набор 14 файлов / 190 тестов и Linux VM runtime повторно
проверены 2026-10-04; hosted CI на дату этой записи оставался открытым и прошёл
позже, 2026-10-05.

SDK остаётся product-agnostic, не зависит от `pluginprotocol` и не содержит
второй lifecycle-модели. Пройденные lifecycle/mTLS проверки подтверждают отказ
для anonymous, wrong-identity, revoked и expired credentials; секреты не
возвращаются в fixtures, логах, errors, metrics и ACK.

SDK `v1.0.0` и согласованный patch tag `v1.0.1` опубликованы; macOS/Ubuntu
hosted CI прошли для tag. VitePress pin и GitHub Pages публикация обновлены.
Core→SDK→Server→forms-db cross-repository integration прошла в hosted CI.
Linux VM runtime проверен в OrbStack; in-process adapter теперь имеет native
Go conformance для source/lifecycle semantics. Production-ready статус всей
v3 экосистемы всё ещё требует общего REST/in-process corpus, Core host-process
smoke и hosted release evidence.
