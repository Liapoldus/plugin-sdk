# Административные страницы Plugin

Plugin может объявить собственные административные страницы для совместимого
клиента управления. Это extension control plane, а не расширение public data plane:
plugin не получает browser bundle, public route, raw Core credentials или
право зарегистрировать произвольный HTTP handler.

## Цель

Страница позволяет оператору конфигурировать instance и выполнять его
предметные administrative actions. Например, forms-db показывает настройки
storage, фильтруемый список submissions и контролируемое удаление записи.
Core остаётся единственной точкой internal API, authorization, audit,
лимитов и redaction; клиент управления отображает декларативный UI.

<img src="/diagrams/plugin-admin-page-flow.svg" alt="Клиент управления получает declarative schema через Core и вызывает plugin capabilities через namespaced API" />

## Три независимых артефакта

| Артефакт | Автор | Хранение | Что содержит |
| --- | --- | --- | --- |
| Instance settings | оператор/клиент управления | Core SQLite как durable source of truth; plugin pulls versioned JSON после REST `Reload(generation)` | DB connection refs, feature settings; валидируются plugin-owned schema, active runtime держится plugin в памяти |
| Admin surface | plugin release | versioned plugin contract | page/section/field/table/action metadata |
| Page data/action result | plugin через Core | transient response + audit | typed query/action payload, никогда не executable UI |

Instance settings не являются UI schema, а UI schema не является конфигурацией
Core. Установка plugin instance не создаёт page сама по себе: Core
сначала получает и валидирует `admin.surface.get`, затем клиент показывает
только страницы, которые capability объявляет для данного healthy instance.

## Декларативная модель страницы

```text
AdminSurface
├── version (plugin protocol surface version)
├── plugin / manifestVersion / surfaceDigest (Core metadata)
├── requiredCapabilities[]
└── pages[]
    ├── id, title, icon, required capability, permissions[]
    └── sections[]
        ├── form: typed fields, validation and option sources
        ├── table: columns, query capability/input schema, cursor policy
        ├── detail: read-only structured result
        ├── metrics / log: bounded observation projection
        └── actions[]: id, capability, input schema, row binding, confirmation, danger flag
```

Поддерживаемые типы полей: `string`, `number`, `boolean`, `select`,
`multiselect`, `secret`, `file`, `directory`, `duration`, `size`, `code`,
`keyValue`, `array`, `object`. Клиент должен отклонять неизвестные section,
field or action type rather than interpret it. Labels/descriptions are plain
text; HTML, CSS, JavaScript/module URL, browser route and arbitrary endpoint
fields are forbidden by schema.

Action объявляет `inputSchema` — ограниченную JSON Schema Draft 2020-12 для
object-input (`type`, `properties`, `required`, `additionalProperties:false`,
`minLength`/`maxLength`, `minimum`/`maximum` и `enum`; без `pattern` (чтобы
не исполнять недоверенные регулярные выражения в browser), `$ref`, executable
extensions и remote schema).
Клиент строит форму только из этой schema и
валидирует её перед запросом; Core повторно валидирует до dispatch. Surface
без `inputSchema` можно показать, но action остаётся disabled с диагностикой;
Клиент не изобретает payload. Для действия по выбранной строке
необязательный `rowInput` явно сопоставляет input key с column key
(`{"recordId":"id"}`); скрытое угадывание имён полей запрещено.

У `select`/`multiselect` есть ровно один источник: статический `options` или
`optionsSource` с объявленным Core capability, typed `inputSchema`,
`valueField` и `labelField`. Динамические options запрашиваются через тот же
fixed page `query` endpoint в режиме
`{"mode":"options","field":"site","input":{...}}`; Core проверяет
объявленный источник и передаёт plugin только typed operation/field/input.
Обычная таблица использует `{"mode":"data","input":{...},"cursor":"…",
"limit":50}` и `section.inputSchema`. Оба режима возвращают typed JSON;
options response имеет форму `{items:[{value,label}],nextCursor?}`. Core
сверяет поле/источник с активной Surface и `requiredCapabilities`, валидирует
вложенный input schema, ограничивает результат 200 options и не принимает из
браузера capability или endpoint. Отсутствующие/некорректные options делают
поле недоступным; клиент не подменяет его произвольным текстовым вводом.

ID страницы стабилен, задаётся в нижнем регистре и локален для instance. Он
становится частью namespaced API path, но не public Core route. Релиз plugin
может совместимо добавить page/field/action; удаление или смена типа поля
требует новой версии Surface и уведомления о migration.

## Пространство имён Core API

Только Core предоставляет указанные ниже внутренние endpoints. Клиент
никогда не подключается к процессу plugin напрямую.

| Endpoint | Capability dispatch | Semantics |
| --- | --- | --- |
| `GET /api/plugins/{instance}/admin/surface` | `admin.surface.get` | returns cached, schema-validated protocol surface + Core metadata; `ETag` is the quoted `surfaceDigest` |
| `POST /api/plugins/{instance}/admin/pages/{page}/query` | page-declared data or option capability | requires `If-Match: "<surfaceDigest>"`; validates mode-specific input schema and cursor/page limits; returns typed data/options only |
| `POST /api/plugins/{instance}/admin/pages/{page}/actions/{action}` | action capability | requires `If-Match` and `Idempotency-Key`; validates action `inputSchema`; dangerous action uses the confirmation handshake below |
| `GET /api/plugins/{instance}/admin/pages/{page}/health` | `health` projection | bounded status, no raw logs/secrets |

For synchronous JSON actions, `Idempotency-Key` is correlation/audit metadata,
not a deduplication key. Every separately accepted request dispatches the action
again even if its key and input match an earlier request. Plugin SDK transports
the value but stores no action response cache and promises no exactly-once
effects; product CAS and domain rules determine the result of a repeated action.
Durable artifact actions are different: their accepted operation has
operation-level deduplication, as described above.

До выдачи данных страницы или выполнения action backend Controller проверяет
все перечисленные в `permissions` идентификаторы по правам аутентифицированного
пользователя на каждом запросе; скрытие элементов только в UI не является
авторизацией. Это Controller-side authorization, а не утверждение от browser.
Все запросы к Core независимо проходят management authorization
(`platform-admin` service credential, а для web Controller — также mTLS) и
проверку instance scope и объявленной capability. Строки `permissions` сами по
себе не выдают полномочий Core и не заменяют его проверки. Core проверяет
`{instance,page,action}` по активному Surface, передаёт только объявленные
входные поля, добавляет actor/request ID и scoped grant handles, применяет лимиты
deadline/concurrency/payload, выполняет redaction, пишет audit и отображает
typed plugin errors в Problem Details.

`query` доступен только для чтения, имеет два schema-bound режима (`data` и
`options`) и использует cursor для данных. Все query/action
передают digest текущей Surface в стандартном quoted entity-tag формате
`If-Match: "<surfaceDigest>"`; устаревший digest возвращает
`409 plugin_surface_changed` до dispatch.

Обычный action без бинарного входа принимает `application/json`. Action может
объявить не более одного `artifactInput` с MIME allow-list и byte limit; такой
action принимает один `multipart/form-data` запрос с JSON part `metadata` и
единственным бинарным part с фиксированным именем `artifact`. Неизвестные или
повторные parts, metadata в неверной форме, MIME вне allow-list и превышение
byte limit отклоняются до dispatch. `metadata` валидируется по action
`inputSchema`, а для multipart эта schema описывает только metadata, не архив.
Core считает фактически полученные bytes всего request body (включая
chunked framing body и multipart boundaries); объявленный action byte limit
применяется отдельно к бинарному part. Каждый artifact action обязан задать
ограничение metadata size и допустимый multipart overhead.

Core потоково читает part, считает фактические байты независимо от
`Content-Length`, применяет deadline/cancel/backpressure, не помещает весь
artifact в память или SQLite и передаёт его plugin через общий Plugin SDK REST
artifact endpoint с привязкой к уже авторизованным `(instance,page,action,
surfaceDigest,idempotencyKey)`. Stream не создаёт новый публичный endpoint и не
позволяет клиенту выбирать capability. Запрос не dispatch-ится, если headers,
metadata, action schema или idempotency preconditions не прошли проверку.

После принятия artifact stream Core отвечает `202` объектом
`{"operationId":"…","state":"pending"}`; результат доступен через общий
`GET /api/operations/{operationId}`. Повтор с тем же idempotency key возвращает ту же operation, а несовпадающий
payload с уже использованным key даёт conflict. Artifact bytes не сохраняются
в журнале операции; durable staging и cleanup принадлежат plugin action и
описаны её product contract.

`action` с признаком `dangerous` выполняется в два запроса к тому же fixed
endpoint. Первый запрос содержит тот же `If-Match`, `Idempotency-Key` и input,
но не содержит `X-Admin-Confirmation`: Core ничего не dispatch-ит и
возвращает `428 confirmation_required` с одноразовым непрозрачным
`confirmationToken` и `expiresAt`. Клиент показывает confirmation UI с
объявленным текстом. Только после явного подтверждения он повторяет неизменный
input с теми же `If-Match` и `Idempotency-Key`, добавляя
`X-Admin-Confirmation: <confirmationToken>`. Core исполняет действие только
если токен не истёк и он привязан к `(actor, instance, page, action,
surfaceDigest, input digest, idempotency key)`; срок — пять минут. Изменение
input требует нового idempotency key и нового handshake. Токен одноразовый,
не хранится в persistent browser storage и не попадает в logs/audit.

Plugin никогда не получает raw client access token, secret value,
management bearer key или database path.

## Жизненный цикл и кэш

1. Оператор вручную запускает plugin process. Core подключается к заранее
   объявленному endpoint и проверяет manifest, health/readiness и активную
   generation; Core не управляет process lifecycle.
2. Core запрашивает `admin.surface.get`, сверяет versioned contract и
   сохраняет `(instance, manifest version, surface digest)`.
3. Клиент читает Surface через Core и отображает разрешённые страницы.
4. Config apply, restart, смена manifest version или unhealthy state сбрасывают
   cache; клиент скрывает страницы до получения healthy valid Surface.
5. Каждый query/action проверяет current surface digest из `If-Match`;
   устаревший UI получает `409 plugin_surface_changed`, прекращает отправку,
   перезагружает schema и сохраняет только соответствующие schema non-secret
   draft values.

Некорректный Surface — ошибка protocol plugin, а не частично отображённый UI.
Core помечает административный Surface недоступным, но не останавливает
несвязанные public capabilities, если их собственный health contract успешен.

## Security invariants

- Plugin cannot add an arbitrary Core API endpoint, listener or frontend
  code; all paths and operation kinds are fixed by Core.
- Клиент отображает данные своими компонентами; он не исполняет plugin
  output, trust HTML, or give plugin a DOM handle.
- `secret` field is write-only. Query response can state `configured: true`,
  never return its value or a secret reference without permission.
- Dangerous-action confirmation is a two-request handshake; a `428` challenge
  never dispatches plugin code, and the one-time token is bound to the exact
  action input and idempotency key.
- Table data is subject to surface-declared columns, cursor limit and Core
  redaction. Export/download is a distinct declared action with audit.
- Configuration write remains the generic Plugin SDK REST `Reload` + exact
  config-pull lifecycle through Core; an admin page cannot mutate bootstrap
  `core.yaml` or traffic configuration outside its instance settings.

## Владение контрактом

Общие Admin UI structures и технические endpoints принадлежат Plugin SDK.
Product page declarations/actions принадлежат конкретному plugin.
Core implementation владеет endpoint authorization и dispatch. Клиент
управления владеет поведением UI. Эта страница — каноническая архитектура;
protocol schema и code обязаны точно ей соответствовать.
