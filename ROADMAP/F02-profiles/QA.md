# F02 QA

## Планируемые сценарии

- Разобрать валидный профиль и defaults с каждым типом поля.
- Отклонить неизвестную версию, некорректный тип, отсутствующее обязательное поле, недопустимый порт/диапазон и путь, который нельзя безопасно смонтировать.
- Убедиться, что неизвестное поле не превращает `off` в `direct` и не убирает обязательный proxy alias.
- Проверить precedence defaults → профиль и профильный override DNS.
- Без `--profile` создать пользовательский `default.unic` при первом запуске и сохранить его при следующих запусках; с явным профилем не читать `default.unic`.
- Без `--profile` проверить выбор `<app-id>.unic`, fallback на `default.unic`, отсутствие поиска app-specific профиля без `--app-id`, и приоритет явного `--profile`.
- Декодировать значения `env`, проверить их передачу bwrap, отказ на неверном имени, NUL в значении и зарезервированных ключах.
- Проверить профильный argv `command`, его использование при отсутствии CLI-команды, приоритет команды после `--`, и ошибку при пустых командах в обоих источниках.

## Выполнение

Проверено:

- `gofmt -w internal/profile/strict_unic.go internal/profile/decode_test.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — первый запуск остановился на vet-диагностике dynamic format в `strictUNICParser.expect`; после замены на `"%s", message` повторный запуск прошёл. Добавлены проверки round-trip вложенного профиля, отказа неизвестного поля/отсутствующего mount source и merge system → user → profile. Другие пакеты пока сообщают `[no test files]`.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; GVM вывел предупреждение о недоступном stat cache в read-only `/home/user/.gvm/.cache`.
- `git diff --check` — без замечаний.
- Проверены исходники go-unic v1.0.2: неизвестные поля struct пропускаются, повторные одиночные поля дают ошибку, повторные slice fields собираются decoder в slice.

Эти тесты проверяют decode/merge, но не runtime поведение filesystem mounts, сети или proxy.

Изменение F02-T03:

- Реализованы необязательный `--profile` и создание `$XDG_CONFIG_HOME/isobox/default.unic` с режимом `0600` и сетью `off`; повторное создание использует `O_EXCL`, сохраняя уже существующий файл.
- Проверки F02-T03 пока не запускались; runtime-приёмка не выполнялась.
- Для F02-T04 добавлены поле `env` и fail-closed валидация; `gofmt`, `go build ./...` и `git diff --check` прошли. GVM сообщил о read-only stat cache, сборка завершилась с кодом 0. Unit и runtime проверки ещё не запускались.
- Для F02-T05 добавлено профильное поле `path`, fail-closed валидация и расширение `PATH` перед встроенным sandbox списком. `gofmt`, `go build ./...` и `git diff --check` прошли; GVM сообщил о read-only stat cache, сборка завершилась с кодом 0. Unit и runtime проверки не запускались.
- Для F02-T06 выбор профиля без `--profile` учитывает `<app-id>.unic` рядом с `default.unic`; при отсутствии app-specific файла используется default. `gofmt`, `go build ./...` и `git diff --check` прошли; GVM сообщил о read-only stat cache, сборка завершилась с кодом 0. Unit и runtime проверки выбора профиля не запускались.
- Для F02-T07 профиль получил argv-массив `command`; команда после `--` заменяет его, иначе используется команда профиля. Отсутствие обеих команд приводит к ошибке до preflight. `gofmt`, `go build ./...` и `git diff --check` прошли; GVM сообщил о read-only stat cache, сборка завершилась с кодом 0. Unit и runtime проверки не запускались.
