# F07 QA

## Планируемые сценарии

- CPU, memory, process-count и open-file limit наблюдаемо ограничивают приложение.
- Каждый лимит по отдельности отсутствует/запрошен; отсутствие не меняет незапрошенные ограничения.
- Ограничение невозможно применить, превышает доступный parent cgroup или механизм недоступен: приложение не запускается.
- Проверить, что дочерние процессы приложения остаются в той же группе лимитов.
- Проверить cleanup cgroup/scope после нормального выхода, сигнала и аварии запуска.

## Выполнение

- `gofmt -w internal/cli/cli.go internal/preflight/preflight.go internal/sandbox/build.go internal/sandbox/run_linux.go internal/sandbox/run_other.go internal/limits/limits.go internal/limits/limits_other.go internal/limits/cgroup_linux.go` — успешно.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — успешно; Go вывел предупреждение, что GVM stat cache недоступен для записи из-за read-only `/home/user/.gvm`.
- `git diff --check` — успешно.
- `systemctl --user show-environment` — runtime-проверка пользовательской шины недоступна: `Operation not permitted`.
- `/proc/self/mountinfo` — cgroup v2 виден, но `/sys/fs/cgroup` смонтирован `ro` в окружении выполнения.

Runtime-приёмка не выполнена: проверить реальный transient scope, наблюдаемые лимиты приложения, родительские лимиты и cleanup cgroup здесь нельзя. Задача остаётся `[~]` до проверки на системе с доступной пользовательской шиной и управляемой cgroup v2.
