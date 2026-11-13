# F07 QA

## Приёмочные сценарии

- CPU, memory, число kernel tasks и открытые файлы ограничиваются точно по профилю.
- `process_count` включает процессы и потоки приложения; служебные процессы bwrap не расходуют его бюджет.
- Незаявленный дополнительный task запрещён cgroup лимитом.
- Отсутствующий или недостаточный механизм лимитов блокирует приложение до запуска.
- Transient scope удаляется после нормального завершения приложения.

## Выполнено

- `gofmt` для `internal/limits/*.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./internal/limits` — успешно после поправки резерва `TasksMax`; проверяются cgroup properties, overflow, парсинг quota/cpuset и отказ при более строгих parent limits.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go test ./...` — успешно после всех изменений F07.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; GVM вывел предупреждение о недоступном stat cache в read-only `/home/user/.gvm`, код возврата 0.
- `git diff --check` — успешно.
- Сборка бинарного файла для live-приёмки: `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build -o /tmp/isobox-f07-live/isobox ./cmd/isobox` — успешно. GVM сообщил о недоступном для записи stat cache в read-only `/home/user/.gvm`; на результат сборки это не повлияло.
- Синтаксис `ROADMAP/F07-limits/source/live_acceptance.py` проверен через Python `ast.parse` — успешно.
- `python3 ROADMAP/F07-limits/source/live_acceptance.py /tmp/isobox-f07-live/isobox` — успешно под обычным пользователем (UID 1000) с доступными user systemd manager и cgroup v2:
  - профиль только с `open_files 32` получил `RLIMIT_NOFILE=32/32`;
  - комбинированный профиль получил `cpu.max` ровно 50%, `memory.max=268435456` и `pids.max=6` для четырёх app tasks плюс двух процессов bwrap;
  - `pids.current=6` при четырёх активных app tasks; пятый task приложения был запрещён;
  - приложение получило `RLIMIT_NOFILE=64/64`;
  - cgroup transient scope исчез после штатного выхода;
  - при отсутствии `systemd-run` запрос CPU лимита завершился до запуска payload.
- Начальные попытки live-проверки в ограниченной оболочке не имели доступа к user systemd bus (`Operation not permitted`) и cgroup controls (`/sys/fs/cgroup` read-only). Повтор с разрешённым rootless user scope прошёл; эти первоначальные отказы не являются отказом на целевом хосте.

## Регрессионная проверка IPv4 bootstrap helper

- После добавления namespace-local IPv6 helper обычная сборка сначала не проходила профиль `process_count 4`: Go runtime helper исчерпывал `TasksMax=6`, пытаясь создать служебный поток до `exec` приложения.
- Helper теперь получает `GOMAXPROCS=1` только на время своей настройки и удаляет переменную перед `exec`; приложение сохраняет собственное поведение runtime.
- `python3 ROADMAP/F07-limits/source/live_acceptance.py /tmp/isobox-test-bin/isobox` — прошло: `pids.max=6`, `pids.current=6` с четырьмя задачами приложения, пятая задача запрещена, остальные CPU/memory/open-files и fail-closed проверки прошли.
- Проверка выявила, что late network helper исполняет приложение со своего закреплённого host TID; `RLIMIT_NOFILE` теперь применяется к этому TID. Сетевой off-helper также удаляет внутренний `GOMAXPROCS` перед `exec`.
- При `off` с опубликованным TCP-портом временный `TasksMax` helper-ов был снижен `systemctl set-property` до точного профильного `process_count+2` до release gate. Live-проверка подтвердила `pids.max=6`, `pids.current=6`, запуск четырёх app tasks, отказ пятой задачи и `RLIMIT_NOFILE=64/64`.
- `python3 ROADMAP/F07-limits/source/live_acceptance.py /tmp/isobox-test-bin/isobox` — полный прогон прошёл: open-files-only, CPU/memory/process/open-files вместе с off published-port firewall setup, запрет лишнего task, fail-closed без `systemd-run` и cleanup scope.
- Минимальный профиль `process_count=1` с off published port также live-запущен успешно; bootstrap allowance был снят перед запуском `/usr/bin/true`.

Инструкция для повторного запуска — `ROADMAP/F07-limits/source/README.md`.
