# F08 QA

## Приёмочные сценарии

- Обычный `isobox run` работает без установленных desktop launchers и settings.
- Установленный launcher валиден, передаёт профиль, app-id и argv, а его удаление не затрагивает профиль/HOME.
- Выбирается первый существующий системный XDG settings файл; пользовательские overrides применяются по полям и перекрывают системные defaults.
- Install/update/remove работают только с пользовательскими файлами и не требуют root.
- Systemd install требует profile command, пишет unit с корректным profile path/app-id и не включает/не запускает его; enable/disable переключают автозапуск без изменения текущего состояния; remove останавливает/отключает и удаляет только управляемый unit.

## Выполнено

- `gofmt` для integration, profile и CLI Go-файлов — успешно; `git diff --check` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/integration` — успешно; проверены создание/update/remove, режим `0600`, защита чужих файлов, system/user/profile precedence и отказ на неизвестном settings field.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — успешно при исходной реализации F08; полный прогон повторён после актуальных изменений в текущем рабочем дереве.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; GVM предупреждает о read-only stat cache в `/home/user/.gvm`, код возврата 0.
- `python3 -c 'import ast, pathlib; ast.parse(pathlib.Path("ROADMAP/F08-system-integration/source/desktop_acceptance.py").read_text(encoding="utf-8"))'` — успешно.
- `python3 ROADMAP/F08-system-integration/source/desktop_acceptance.py /tmp/isobox-f08-live-isobox` — успешно под обычным пользователем в живой X11-сессии:
  - `help` и прямой `run` работают без launcher и settings файлов;
  - первый system XDG config побеждает следующий, а частичный user DNS override сливается по полям;
  - пользовательский `network_mode=off` перекрывает системный `direct`; команда стартует при скрытых `pasta` и `slirp4netns`;
  - install создаёт валидный mode-`0600` launcher в XDG data path с пробелами;
  - `gio launch` реально запускает `isobox run`; marker создан в app HOME при profile path с пробелами и argv с пробелами, `%` и `$`;
  - remove удаляет launcher, сохраняя profile и app HOME.
- Предыдущий вызов `gio launch` в ограниченной оболочке не имел доступа к дисплею (`Cannot open display`). Повторная проверка из активной X11-сессии прошла; этот отказ был ограничением оболочки.

Подробная повторяемая инструкция — `ROADMAP/F08-system-integration/source/README.md`.

После F02-T03 проверки XDG system/user settings выше относятся только к
предыдущему поведению и больше не являются критерием текущей реализации.
Acceptance harness обновлён для проверки первого создания `default.unic`;
после этого изменения harness и runtime acceptance ещё не запускались.

F08-T02: добавлены команды install/enable/disable/remove для systemd user unit. Enable/disable переключают автозапуск в `default.target` без `--now`; remove останавливает и отключает unit. Установка проверяет profile command, пишет только управляемый unit с безопасным quoting `ExecStart` и вызывает `daemon-reload`. Runtime-проверки пользовательского systemd manager не запускались.

Для добавления enable/disable выполнены `gofmt` для изменённых Go-файлов, `go build ./...` (код возврата 0; GVM вывел предупреждение о read-only stat cache) и `git diff --check` — успешно. Runtime-проверка команд через пользовательский systemd manager не выполнялась.

Для переноса desktop-команд под `integration desktop` выполнены `gofmt`, `go build ./...` (код возврата 0; то же предупреждение GVM) и `git diff --check` — успешно. Runtime launcher acceptance в этом изменении не запускалась.
