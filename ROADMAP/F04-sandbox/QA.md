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
- Проверить bwrap hostname `isobox-<app-id>`, hostname `isobox` без app-id и предел длины для app-id максимальной длины.
- Проверить, что rootless preflight подтверждает `--unshare-cgroup`, а cgroup лимиты по-прежнему видны host supervisor до открытия app gate.
- Проверить, что `getcwd()` и `PWD` приложения равны `/home/isobox`.

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
- F04-T02: runner сделан executable для документированного прямого запуска; `bash -n tests/f04-sandbox/run.sh tests/run.sh`, `test -x tests/f04-sandbox/run.sh` и `git diff --check` — успешно.
- F04-T02: добавлена очистка трёх generated managed-HOME leaf paths после приёмки; `bash -n tests/f04-sandbox/run.sh`, `python3 -c 'import ast; ast.parse(open("tests/f04-sandbox/probe.py", encoding="utf-8").read())'` и `git diff --check` — успешно.
- F04-T02: пользовательский отчёт `/tmp/isobox-test-results/f04-mount-home.5ylFUo/result.txt` на `6a781b2` завершился `runtime_acceptance=passed` на Linux Mint 7 / kernel 6.12.116 / bubblewrap 0.12.0. Проверены explicit HOME, read-only/writable mounts, persistence, разделение managed HOME по profile/app-id, owner/mode `0700`, fail-closed для отсутствующего source и удаление тестовых HOME leaf-каталогов.

- F04-T01: подготовлен runtime acceptance runner `./tests/run.sh f04-base-runtime`; он проверяет root/runtime paths, очищенное environment, read-only system mounts, argv/exit status, SIGTERM forwarding и fail-closed на симулированной ошибке обязательного `/usr` bind.
- F04-T01: `bash -n tests/f04-sandbox/base-runtime/run.sh tests/run.sh` — успешно.
- F04-T01: `python3 -c 'import ast; ast.parse(open("tests/f04-sandbox/base-runtime/probe.py", encoding="utf-8").read())'` — успешно.
- F04-T01: `git diff --check` — без замечаний.
- F04-T01: пользовательский отчёт `/tmp/isobox-test-results/f04-base-runtime.pNmyWf/result.txt` на `6a781b2` завершился `runtime_acceptance=passed` на Linux Mint 7 / kernel 6.12.116 / bubblewrap 0.12.0. Проверены root/runtime paths, clean environment, read-only system mounts, argv, exit status 37, SIGTERM delivery и отказ до payload при ошибке обязательного `/usr` bind.

- F04-T03: подготовлен runtime acceptance runner `./tests/run.sh f04-gui-grants`; он проверяет default-deny, независимые Wayland/X11/PipeWire/PulseAudio/GPU/session-bus grants, отсутствие system bus и отказ до app marker для невалидных/недоступных ресурсов.
- F04-T03: `bash -n tests/f04-sandbox/gui-grants/run.sh tests/run.sh` — успешно.
- F04-T03: `python3 -c 'import ast; ast.parse(open("tests/f04-sandbox/gui-grants/probe.py", encoding="utf-8").read()); ast.parse(open("tests/f04-sandbox/gui-grants/fake_socket_server.py", encoding="utf-8").read())'` — успешно.
- F04-T03: `git diff --check` — без замечаний.
- F04-T03: пользовательский отчёт `/tmp/isobox-test-results/f04-gui-grants.A6Iite/result.txt` на `6a781b2` остановился на первом default-deny probe. Probe сообщил `PASS GUI grant default`, но runner завершился с `default-deny probe missed its expected checkpoint`, потому что проверял marker `default-deny.started`, а probe нормализовал точку в `default-deny_started`. Исправлены marker names для обычных GUI probes и GPU probe; полная runtime-приёмка grants пока не подтверждена.
- F04-T03: после исправления выполнены `bash -n tests/f04-sandbox/gui-grants/run.sh tests/run.sh`, Python AST parse обоих GUI probe scripts и `git diff --check` — успешно. Runtime runner после исправления не запускался.
- F04-T03: пользовательский отчёт `/tmp/isobox-test-results/f04-gui-grants.iJ9hAr/result.txt` на `d0df920` подтвердил default-deny, Wayland, X11, PipeWire, PulseAudio, desktop/session-bus grants и fail-closed negative cases (`wayland-invalid`, TCP DISPLAY, remote audio, missing session bus). Runner затем завершился с `tests/f04-sandbox/gui-grants/run.sh:228: x11_socket: unbound variable` до cleanup checkpoint и GPU probe. Поэтому эта попытка не завершилась `runtime_acceptance=passed`.
- F04-T03: после определения `x11_socket` по `fake_x11_dir` и выделенному display выполнены `bash -n tests/f04-sandbox/gui-grants/run.sh tests/run.sh`, Python AST parse обоих GUI probe scripts и `git diff --check` — успешно. Повторный runtime runner не запускался; GPU probe и финальная проверка cleanup остаются непроверенными.
- F04-T03: пользовательский отчёт `/tmp/isobox-test-results/f04-gui-grants.4bDuLc/result.txt` на `0a567ca` завершился `runtime_acceptance=passed` на Linux Mint 7 / kernel 6.12.116 / bubblewrap 0.12.0. Проверены default-deny, Wayland, X11, PipeWire, PulseAudio, desktop/session-bus grants, fail-closed negative cases, доступ к имеющемуся GPU device и cleanup synthetic sockets.

Границы проверки:

- Base root/lifecycle и profile mounts/HOME прошли на целевом пользовательском хосте по отчётам выше; в текущей локальной среде эти runtime runners не запускались.
- Полная F04-T03 GUI-приёмка подтверждена отчётом `4bDuLc`; synthetic echo services подтверждают доступ и передачу данных по Unix sockets, но не эмулируют протоколы Wayland/X11/audio/D-Bus.
- Synthetic echo services подтверждают доступ и передачу данных по Unix sockets, но не эмулируют протоколы Wayland/X11/audio/D-Bus.

F04-T04: hostname добавлен в argv bubblewrap; `gofmt`, `go build ./...` и `git diff --check` прошли. GVM выдал предупреждение о read-only stat cache, сборка завершилась с кодом 0. Unit/runtime проверки hostname не запускались.

F04-T05: `--unshare-cgroup` добавлен в bwrap argv и rootless preflight probe. `gofmt`, `go build ./...` и `git diff --check` прошли; GVM вывел предупреждение о read-only stat cache, сборка завершилась с кодом 0. Rootless preflight probe и runtime-проверки ещё не запускались.

F04-T06: bwrap устанавливает cwd и `PWD` в `/home/isobox`; acceptance probe обновлён. `gofmt`, `go build ./...` и `git diff --check` прошли; Go сообщил о read-only GVM stat cache, сборка завершилась с кодом 0. Runtime-приёмка не запускалась.

F04-T07: host passwd/group mounts заменены минимальными read-only account files; эффективные UID/GID сохраняются, пользователь назван `isobox`, HOME в passwd — `/home/isobox`. `gofmt`, `go build ./...` и `git diff --check` прошли; Go сообщил о read-only GVM stat cache, сборка завершилась с кодом 0. Runtime-приёмка не запускалась.

F04-T08: managed HOME перенесён в `~/.isobox/home/<app-id>/`; acceptance runner обновлён, чтобы проверять persistence по app-id и чистить только свои уникальные тестовые каталоги. `go build ./...` и `git diff --check` прошли; runtime runner не запускался.
