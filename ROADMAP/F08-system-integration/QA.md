# F08 QA

## Планируемые сценарии

- CLI работает на системе без desktop integration.
- Сгенерированный launcher передаёт корректный profile, app-id и команду.
- Пользовательское значение перекрывает системный default; изменение/удаление launcher не затрагивает профиль и данные приложения.
- Установка и удаление интеграции не требуют привилегий root, если меняются только пользовательские файлы.

## Выполнение

- `gofmt -w internal/profile/schema.go internal/profile/decode.go internal/integration/settings.go internal/integration/desktop.go internal/integration/desktop_linux.go internal/integration/owner_linux.go internal/integration/desktop_other.go internal/cli/cli.go internal/cli/integration.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; Go вывел предупреждение о недоступном для записи GVM stat cache под `/home/user/.gvm`.
- `git diff --check` — успешно.
- Проверка запуска из desktop session не выполнялась в текущем окружении; системные/user settings precedence и desktop menu visibility требуют runtime-приёмки.
