# Liapoldus Plugin SDK

Отдельный четырёхслойный Go-модуль общего REST lifecycle для plugin processes.
Текущий локальный SDK worktree проходит `make check` (14 файлов / 190 тестов),
Go build/vet и child-process conformance. На 2026-10-04 Core, Server и forms-db
также прошли локальные интеграционные gates с SDK; Core→Server→forms-db проверен
на macOS и Linux/OrbStack, включая PostgreSQL, MySQL и MariaDB. Это не означает,
что текущий WIP опубликован или проверен hosted CI. Оставшиеся внешние release
gates перечислены в [TODO.md](https://github.com/Liapoldus/plugin-sdk/blob/main/TODO.md).

## Единственный источник contract

Нормативный contract — `infrastructure/assets/plugin-sdk/v1/http-contract.json`,
встроенный в бинарник. Production-код читает из него маршруты, методы,
media types, статусы, коды, лимиты, deadlines, обязательные поля документов,
список generation-состояний, TLS floor, trust domain и полный словарь
outcomes. Второго источника нет: `tests/integration/source-of-truth.test.ts`
падает, если значение contract-строки появится в production Go, если asset
разветвится или если в нём появится продуктовый документ.

Поэтому ниже не приводятся literal-пути, лимиты и коды ответов: они живут в
asset. Здесь описано только то, что меняет поведение потребителя.

## Breaking change в Core

SDK реализует согласованный target contract, и Core обязан перейти на него
одним coordinated breaking change. SDK не содержит совместимых alias-ов,
fallback-ов на старые endpoints и двух lifecycle-моделей: старые
экспортируемые конструкторы и error sentinels не сохранены.

Core обязан:

- **Pull exact generation.** На exact-generation pull публиковать все
  зарегистрированные contract-ом response headers, а не часть: `generation`,
  `sha256`, `schemaVersion`, `generationState`. Зарегистрированные значения
  берутся из asset. `generationState` публикует ровно два durable slot-а:
  `active` и `previous`. Любой другой slot, любое перечисление поколений и
  подстановка чужого generation не поддерживаются.
- **Reload acknowledgement.** Возвращать типизированный ACK с пятью
  обязательными полями: `generation`, `sha256`, `schemaVersion`, `applied`,
  `outcome`. Ответ без `schemaVersion`/`outcome` SDK не примет.
- **Readiness.** Публиковать readiness с семью полями: `ready`, `generation`,
  `sha256`, `schemaVersion`, `pendingGeneration`, `instanceId`, `replicaId`.
  `generation` — только реально применённое поколение; отказанное поколение
  публикуется в `pendingGeneration` и никогда не выдаётся за активное.
- **Registration.** Публиковать registration с полями, которые регистрирует
  contract (`contractVersion`, `instanceId`, `replicaId`,
  `appliedGeneration`, `ready`).

`Rollback` остаётся операцией Core Management API. Plugin-side rollback
endpoint не существует, и его появление ломает suite: production-дерево
проверяется на отсутствие любого plugin-side rollback.

## Lifecycle

`Reload` — это уведомление, а не передача конфигурации: descriptor несёт
только `generation`, `sha256`, `schemaVersion`. Plugin сам тянет ровно это
поколение и применяет документ.

Политика `Lifecycle.Reload` по порядку: отклонить descriptor, нарушающий
синтаксис; ответить на побайтовый повтор активного descriptor-а без повторного
pull; отклонить descriptor, противоречащий активному поколению, без pull;
pull-нуть точное поколение; отклонить поколение, которое Core больше не
желает; проверить generation, digest и schema version и по объявлению, и по
самим байтам; и только затем вызвать plugin-owned applier.

SDK никогда не повторяет pull или apply по собственной инициативе, не
применяет документ с непроверенным digest и не заменяет ранее активную
конфигурацию из-за отказа. Повторный `Reload` того же descriptor-а
идемпотентен (`alreadyActive`); расхождение под тем же именем — отдельный
outcome `generationConflict`. Все 15 contract-outcomes имеют каждый свой
status и уникальный код, и suite проверяет, что outcome не отвечает чужим
кодом.

## Product configuration остаётся непрозрачным

Документ — opaque JSON object. SDK сохраняет байты, которые вернул Core, без
remarshal и без нормализации, отклоняет duplicate JSON object keys, проверяет
JSON syntax, размер, generation, digest и schema version, и только после этого
вызывает plugin-owned `ConfigurationApplier`. Плагин сам валидирует документ по
своей версионированной JSON Schema и атомарно активирует его; при ошибке
предыдущая конфигурация остаётся полностью рабочей, отменённый context
прерывает apply без активации. SDK не интерпретирует ни одного product field,
не знает capabilities, providers, DB drivers и public routes. Product settings,
Manifest и schemas остаются в соответствующих plugin repos.

## Secrets

`SecretManager` выдаёт и погашает scoped grant-ы для operational secrets.
Во время вызова plugin-owned applier grant автоматически ограничен именно
обрабатываемым candidate generation; вне этого вызова — уже активным
поколением. Произвольное generation задать нельзя. Grant ограничен
instance/replica, поколением и purpose, и
погашается ровно один раз; второе погашение отклоняется локально, без второго
вызова к Core. SDK не интерпретирует secret references и не пишет secret bytes,
handles, references или purpose ни в один ответ, лог, error или metric.
Погашенное значение отдаётся владельцу с явным `Destroy`.

## Transport и credentials

Control plane — только mutual TLS: HTTPS-only, обязательный client
certificate, pinned single peer identity, TLS floor из contract и отказ от
HTTP redirect. Анонимный клиент, bearer token вместо сертификата и plaintext
control URL отклоняются на handshake или на этапе конструирования. Bearer-only
и plaintext downgrade-путей нет.

Credentials принадлежат оператору: SDK не встраивает CA, не генерирует
сертификаты и не подставляет заглушку. `LoadCredentials` валидирует материал
(непустой trust pool, соответствие роли, private key против public key,
минимальный размер ключа); `NewStaticCredentialsProvider` обслуживает один
набор, `NewFileCredentialsProvider` перечитывает файлы при изменении размера
или mtime, поэтому ротация на диске применяется без рестарта процесса.
Неудачная ротация sticky: та же ошибка повторяется, пока вход не изменится, и
ранее валидный материал остаётся в силе. `NewRevocation` индексирует CRL,
подписанные доверенным authority, и уважает окно ThisUpdate/NextUpdate;
недоступный, непарсящий, неподписанный или истёкший набор даёт ошибку, а не
`false`, поэтому handshake fail-closed. `RevocationFailClosed` — zero value и
единственная posture, которую описывает contract; `RevocationPermitAbsent`
существует как явный более слабый opt-in флаг и не используется по умолчанию.
Revocation и peer identity вычисляются один раз на handshake — единственной
точке, где предъявляется peer certificate; окно на уже принятое соединение
ограничивает contract idle deadline.

## Слои и границы

Ровно четыре production слоя: `domain/`, `application/`,
`infrastructure/`, `presentation/`. `domain/` содержит только `models/` и
`interfaces/`. Зависимости направлены внутрь: domain не импортирует другие
слои, application и infrastructure импортируют domain, presentation —
domain и application. `presentation` не импортирует `infrastructure`, поэтому
composition живёт вне четырёх слоёв — в собственном `main` плагина.
`tests/layers.test.ts` падает на нарушении любого из этих правил, на
появлении пятого слоя или product-specific package.

SDK не зависит от Core, `pluginprotocol`, Caddy, любого product plugin или
стороннего модуля: production-код импортирует только четыре слоя и
standard library. `pluginprotocol` остаётся независимой библиотекой только
для generic plugin↔plugin взаимодействия и этот модуль её не импортирует.

## Что делает автор плагина

Автор реализует только plugin-owned порты и собирает их в своём `main`:

- `interfaces.ConfigurationApplier` — валидация и атомарное применение
  документа.
- `interfaces.PluginMetadata` — `Manifest` и версионированная settings JSON
  Schema, публикуемые байт-в-байт.
- `interfaces.Logger`, `interfaces.LifecycleObserver`, `interfaces.Clock` и
  observability, если автор хочет собственную телеметрию.
- `application.NewSecretManager` там, где плагину нужны operational secrets.

SDK даёт готовые адаптеры: `infrastructure.LoadHTTPContract`,
`NewLifecycle`, `NewCoreConfigurationSource`, `NewPluginClient`,
`NewCoreSecretBroker`, `NewMutualTLSClient`, `NewMutualTLSServer`,
`NewJSONLogger`, `NewObserverPrometheusCollector`, `NewRecorder`,
`NewLoggingObserver`, `NewRevocation` и `presentation.NewHandlerSet`.
`NewHandlerSet` валидирует contract и требует значение на каждый порт до того,
как что-либо слушается, отказываясь строить неконформную поверхность;
`Handler()` отдаёт multiplexer и никогда сам не слушает — transport и lifecycle
процесса принадлежат composition root. Author не пишет generic HTTP/mTLS boilerplate и
не может случайно добавить endpoint, которого нет в versioned contract.

Рабочий пример composition — `tests/fixtures/reload-runtime/main.go`. Он
test-only: его plaintext mirror и harness control listener никогда не
попадают в production packages.

## Observability

`NewObserverPrometheusCollector` рендерит exposition в media type, который
регистрирует contract, с детерминированными именами, help-строками и
bounded labels; `NewRecorder` считает ровно один bounded lifecycle event на
outcome, держит readiness и pull-failure сигналы в шаге с применённым
поколением и никогда не принимает document, secret value или grant handle как
label value. `NewJSONLogger` пишет newline-delimited JSON в operator-owned
sink, ограничивая поля, длины ключа и значения и подставляя redaction
placeholder до сериализации; config body, cookies, `Authorization`, private
key, DSN, grant handle и transport secrets не пишутся. `application.OutcomeOf`
даёт типизированный outcome из ошибки, а `OutcomeError` не сериализует
внутреннюю причину клиенту: наружу уходят только outcome и
зарегистрированный contract-ом code.

## Canonical module path

Canonical import path, утверждённый владельцем: `github.com/Liapoldus/plugin-sdk`.
`go.mod`, SDK imports, Core и Server consumer теперь используют этот путь.
Не выпускать модуль, пока forms-db и общие integration gates не пройдены.

Нормативная архитектура и migration plan находятся в
[документации Core](/core/architecture/target).

## Проверка

`make check` запускает TypeScript conformance, `go build ./...` и
`go vet ./...`. TypeScript conformance поднимает реальные mTLS child processes,
поэтому для него нужны Go toolchain и Node. На 2026-10-04 текущий локальный
набор включает 14 файлов и 190 тестов; тот же suite прошёл в Ubuntu 24.04 ARM64
VM под OrbStack. Сквозная интеграция дополнительно проверяется в Core и
продуктовых plugins; локальный PASS не заменяет hosted CI на согласованных
опубликованных revisions и release provenance.
