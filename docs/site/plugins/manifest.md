# Manifest и capabilities

Подробный Manifest, settings schema и invocation-mode contract принадлежат
подключённому plugin; общий REST lifecycle shape задаёт Plugin SDK. Manifest
объявляет идентичность/release и capability→modes; Core проверяет объявленную
schema и поддерживаемый mode, но не содержит специальных условий для
конкретных plugins. `pluginprotocol` Manifest не переносит и lifecycle не
обслуживает.

Instance metadata и desired settings принадлежат Core. Settings JSON
передаётся Management API непосредственно как plugin-owned JSON object, без
общей обёртки. Core сохраняет точные исходные UTF-8 bytes и их SHA-256 в
versioned SQLite generation, отклоняет duplicate keys и проверяет schema без
декодирования и пересериализации продуктовых полей. Core вызывает REST
`Reload(generation)`, после чего plugin сам pull-ит эту immutable revision и
подтверждает совпадающий digest до readiness. Plugin не читает application
environment/config files.

Routing и capabilities связываются через traffic JSON settings Server plugin,
а не native Caddyfile или Core route DSL. Server plugin строит свою runtime
конфигурацию и напрямую вызывает разрешённые plugin instances.

Wire schema и examples не копируются в эту документацию; каноническая модель
описана в [целевой архитектуре Core](../core/architecture/target),
[plugin deployment](../core/architecture/plugin-deployment) и
[plugin Admin Pages](admin-pages).
