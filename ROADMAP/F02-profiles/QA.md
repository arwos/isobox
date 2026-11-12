# F02 QA

## Планируемые сценарии

- Разобрать валидный профиль и defaults с каждым типом поля.
- Отклонить неизвестную версию, некорректный тип, отсутствующее обязательное поле, недопустимый порт/диапазон и путь, который нельзя безопасно смонтировать.
- Убедиться, что неизвестное поле не превращает `off` в `direct` и не убирает обязательный proxy alias.
- Проверить precedence defaults → профиль и профильный override DNS.

## Выполнение

Проверено:

- `gofmt -w internal/profile/strict_unic.go internal/profile/decode_test.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — первый запуск остановился на vet-диагностике dynamic format в `strictUNICParser.expect`; после замены на `"%s", message` повторный запуск прошёл. Добавлены проверки round-trip вложенного профиля, отказа неизвестного поля/отсутствующего mount source и merge system → user → profile. Другие пакеты пока сообщают `[no test files]`.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; GVM вывел предупреждение о недоступном stat cache в read-only `/home/user/.gvm/.cache`.
- `git diff --check` — без замечаний.
- Проверены исходники go-unic v1.0.2: неизвестные поля struct пропускаются, повторные одиночные поля дают ошибку, повторные slice fields собираются decoder в slice.

Эти тесты проверяют decode/merge, но не runtime поведение filesystem mounts, сети или proxy.
