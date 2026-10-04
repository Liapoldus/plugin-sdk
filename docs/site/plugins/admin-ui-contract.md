# Plugin Admin UI contract

Плагин публикует versioned declarative Admin Surface; Constructor не вшивает
его UI, а Core не принимает plugin-owned HTTP handler. Полная жизненная
модель, fixed internal API, cache, security и ownership находятся на
[Plugin Admin Pages](/plugins/admin-pages).

Поддерживаемые базовые fields: `string`, `number`, `boolean`, `select`,
`multiselect`, `secret`, `file`, `directory`, `duration`, `size`, `code`,
`keyValue`, `array`, `object`. `secret` — reference/write-only field, его
значение никогда не возвращается UI.

Общие technical endpoint/JSON shapes для declarative Admin Surface принадлежат
отдельному Plugin SDK contract; конкретное содержимое page/actions остаётся
plugin-owned. Не размещать Admin Surface в `pluginprotocol`, который отвечает
только за generic plugin-to-plugin communication.
Для action с бинарным artifact Core передаёт metadata и один ограниченный
поток через отдельный mTLS Plugin SDK REST endpoint. SDK владеет framing,
ограничением входящих байтов, cancellation/backpressure и receipt envelope;
плагин владеет schema metadata, форматом архива, проверкой содержимого и
durable operation. Core не импортирует peer-only `pluginprotocol` и не
буферизует весь artifact. SDK endpoint реализован и проверен на настоящем
Core→SDK→Server child-process пути: первая публикация, receipt и status action
долговечной operation, выдача опубликованного сайта и повторный запуск Server.
Ограничения и формат endpoint принадлежат versioned SDK contract. Это
расширение Core API описано в
[API boundaries](/architecture/api-boundaries); endpoint не является
неофициальным direct-plugin URL.
