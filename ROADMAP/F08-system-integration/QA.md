# F08 QA

## Планируемые сценарии

- CLI работает на системе без desktop integration.
- Сгенерированный launcher передаёт корректный profile, app-id и команду.
- Пользовательское значение перекрывает системный default; изменение/удаление launcher не затрагивает профиль и данные приложения.
- Установка и удаление интеграции не требуют привилегий root, если меняются только пользовательские файлы.

## Выполнение

- `gofmt -w internal/profile/schema.go internal/profile/decode.go internal/integration/settings.go internal/integration/desktop.go internal/integration/desktop_linux.go internal/integration/owner_linux.go internal/integration/desktop_other.go internal/cli/cli.go internal/cli/integration.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; Go вывел предупреждение о недоступном для записи GVM stat cache под `/home/user/.gvm`.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/integration` — успешно; покрыты install/update/remove, режим `0600`, защита неуправляемых файлов, порядок system/user/profile и отказ на неизвестном settings field.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — успешно.
- `git diff --check` — успешно.
- CLI `integration install` создал launcher в отдельном временном `XDG_DATA_HOME` с пробелами в пути; `stat` подтвердил mode `600`; `desktop-file-validate` завершился успешно. `integration remove` удалил launcher, профиль остался на месте.
- `isobox help` — успешно, команда доступна без desktop integration.
- `gio launch <temporary .desktop file>` — desktop runtime не запустился: `Failed to parse arguments: Cannot open display:`. Launcher удалён после проверки.
- Фактический запуск из desktop session и видимость в desktop menu требуют runtime-приёмки на системе с графической сессией.
