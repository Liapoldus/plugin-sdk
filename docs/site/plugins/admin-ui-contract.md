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
буферизует весь artifact. Этот endpoint пока является целевым контрактом,
а не реализованной частью v1 runtime; см. SDK TODO и Server acceptance.
Это требуемое расширение Core API, указанное в
[API boundaries](/architecture/api-boundaries); endpoint не является
неофициальным direct-plugin URL.
