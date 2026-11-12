# F02 QA

## Планируемые сценарии

- Разобрать валидный профиль и defaults с каждым типом поля.
- Отклонить неизвестную версию, некорректный тип, отсутствующее обязательное поле, недопустимый порт/диапазон и путь, который нельзя безопасно смонтировать.
- Убедиться, что неизвестное поле не превращает `off` в `direct` и не убирает обязательный proxy alias.
- Проверить precedence defaults → профиль и профильный override DNS.

## Выполнение

Проверено:

- `gofmt -w internal/profile/schema.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0; GVM вывел предупреждение о недоступном stat cache в read-only `/home/user/.gvm/.cache`.
- `git diff --check` — без замечаний.

Не запускалось:

- UNIC decode/round-trip для значений по умолчанию и nested blocks; это проверяется задачей F02-T02 после добавления parser.
- Тесты не добавлялись и не запускались.
