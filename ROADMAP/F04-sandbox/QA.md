# F04 QA

## Планируемые сценарии

- Попытаться прочитать host home и системный файл на запись: доступ отсутствует; штатные системные файлы доступны read-only.
- Запустить команду, которая проверяет наличие `/proc`, `/dev`, `/tmp`, `/run`, cwd `/` и очищенного host environment.
- Проверить, что `argv` после `--` и код завершения приложения передаются точно; отправленные сигналы доходят до процесса в sandbox.
- Проверить, что отсутствующий обязательный `/usr` или неудачный bwrap mount блокирует запуск до payload.
- Проверить, что профили с сетью кроме `off` или опубликованными портами получают отказ вместо запуска с ослабленной конфигурацией.
- Смонтировать явно разрешённый путь и проверить отказ для отсутствующего/небезопасного/неприменимого mount.
- С заданным HOME проверить стабильное переиспользование; без заданного HOME проверить изоляцию по app-id и приватные права каталога.
- Проверить Wayland/X11/audio/GPU grants по отдельности; при выключенных grants ресурсы отсутствуют.
- Проверить отсутствие полного D-Bus и подключённых GUI sockets/devices по умолчанию; explicit desktop grant подключает только session bus socket, не system bus.
- Проверить, что isobox не вызывает portal API; при explicit session-bus grant портал может быть доступен приложению как обычный session-bus service.
- Проверить аргументы, exit status, сигнализацию и cleanup после завершения приложения.

## Выполнение

- `gofmt -w internal/sandbox/build.go internal/sandbox/run_linux.go internal/sandbox/run_other.go internal/cli/cli.go` — успешно.
- `git diff --check` — без замечаний.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0; GVM вывел предупреждение о read-only stat cache в `/home/user/.gvm/.cache`.
- Прочитан вывод `bwrap --help`; локально установлен bubblewrap 0.12.0, в его help есть `--new-session`, `--die-with-parent` и `--json-status-fd`.

- F04-T02: `gofmt -w internal/sandbox/build.go internal/sandbox/profile_mounts.go internal/sandbox/home_linux.go internal/sandbox/home_other.go internal/cli/cli.go` — успешно.
- F04-T02: `git diff --check` — без замечаний.
- F04-T02: `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0; GVM вывел предупреждение о read-only stat cache в `/home/user/.gvm/.cache`.
- F04-T02: просмотрена документация bubblewrap; файловые операции применяются в порядке аргументов, отсутствующие родительские каталоги для destination создаются автоматически ([man page](https://github.com/containers/bubblewrap/blob/main/bwrap.xml)).
- F04-T03: `gofmt -w internal/profile/schema.go internal/sandbox/build.go internal/sandbox/profile_mounts.go internal/sandbox/gui.go internal/sandbox/gui_linux.go internal/sandbox/gui_other.go` — успешно.
- F04-T03: `git diff --check` — без замечаний.
- F04-T03: `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — exit 0; GVM вывел предупреждение о read-only stat cache в `/home/user/.gvm/.cache`.
- F04-T03: сверены официальные документы bubblewrap `--dev-bind`, Wayland socket environment, PipeWire socket names, PulseAudio cookie, D-Bus Unix addresses и Xauthority; ссылки зафиксированы в `CONTEXT.md`.

- F04-T02: подготовлен runtime acceptance runner `./tests/run.sh f04-mount-home`; он собирает приложение и проверяет явные read-only/writable mounts, `home_path`, persistence/изоляцию managed HOME, права `0700` и fail-closed на отсутствующем mount source.
- F04-T02: `bash -n tests/f04-sandbox/run.sh tests/run.sh` — успешно.
- F04-T02: `python3 -c 'import ast; ast.parse(open("tests/f04-sandbox/probe.py", encoding="utf-8").read())'` — успешно.
- F04-T02: `git diff --check` — без замечаний.
- F04-T02: runtime runner не запускался в этой среде; приёмка bwrap mounts/HOME ожидает отчёт с целевого пользовательского хоста.

- F04-T01: подготовлен runtime acceptance runner `./tests/run.sh f04-base-runtime`; он проверяет root/runtime paths, очищенное environment, read-only system mounts, argv/exit status, SIGTERM forwarding и fail-closed на симулированной ошибке обязательного `/usr` bind.
- F04-T01: `bash -n tests/f04-sandbox/base-runtime/run.sh tests/run.sh` — успешно.
- F04-T01: `python3 -c 'import ast; ast.parse(open("tests/f04-sandbox/base-runtime/probe.py", encoding="utf-8").read())'` — успешно.
- F04-T01: `git diff --check` — без замечаний.
- F04-T01: runtime runner не запускался в этой среде; live-приёмка базового bwrap root и lifecycle ожидает отчёт с целевого пользовательского хоста.

- F04-T03: подготовлен runtime acceptance runner `./tests/run.sh f04-gui-grants`; он проверяет default-deny, независимые Wayland/X11/PipeWire/PulseAudio/GPU/session-bus grants, отсутствие system bus и отказ до app marker для невалидных/недоступных ресурсов.
- F04-T03: `bash -n tests/f04-sandbox/gui-grants/run.sh tests/run.sh` — успешно.
- F04-T03: `python3 -c 'import ast; ast.parse(open("tests/f04-sandbox/gui-grants/probe.py", encoding="utf-8").read()); ast.parse(open("tests/f04-sandbox/gui-grants/fake_socket_server.py", encoding="utf-8").read())'` — успешно.
- F04-T03: `git diff --check` — без замечаний.
- F04-T03: runtime runner не запускался в этой среде; проверка сокетов/devices в реальном bwrap остаётся пользовательской live-приёмкой.

Не проверялось:

- `isobox run` не запускался, поэтому реальное чтение файлов из root, передача сигналов и коды exit status не проверены в runtime.
- Автоматические и runtime-тесты не запускались.
- Доступность каждой необязательной системной mount-path зависит от дистрибутива; в этом окружении их набор и вызовы bwrap не исполнялись.
- F04-T02: не запускался bwrap с реальными profile mounts/HOME; проверка реальных mount permissions, вложенных путей, приватных режимов и повторного использования HOME остаётся runtime acceptance.
- F04-T03: не запускался bwrap с включёнными/выключенными GUI grants; доступность сокетов, Xauthority, устройств и D-Bus в реальной сессии остаётся runtime acceptance.
