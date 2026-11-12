# F07 Контекст

- Согласованные лимиты первой версии: CPU, память, число kernel tasks (процессы и потоки) и открытых файлов.
- Запрошенный, но неприменимый или ослабленный лимит блокирует запуск приложения.
- Выбран механизм: transient user scope `systemd-run --user --scope --slice-inherit` с cgroup v2 `CPUQuota`, `MemoryMax` и `TasksMax`; открытые файлы ограничиваются `RLIMIT_NOFILE` через `prlimit64` для процесса приложения. `RLIMIT_AS` не считается эквивалентом memory cgroup limit.
- Пользователь подтвердил, что `process_count` ограничивает процессы вместе с потоками. Значение передаётся как `TasksMax=process_count+1`: одно место резервируется monitor-процессу bwrap. Контроллер pids считает kernel tasks, поэтому лимит включает потоки приложения и может сработать раньше, чем процесс-only счётчик.
- bwrap ждёт на startup gate. До его открытия isobox проверяет, что дочерний процесс находится в созданном cgroup, запрошенные значения установлены, родительские cgroup и cpuset не дают меньший лимит, а `RLIMIT_NOFILE` установлен ровно в запрошенное значение. Любой отказ закрывает gate и останавливает запуск.
- Для CPU/memory/process лимитов обязательны доступные `systemd-run`, пользовательский systemd manager и нужные cgroup v2 controllers; для open files запрошенное значение не может превышать унаследованный hard limit. Недостающая возможность блокирует запуск.
- Официальные документы: [systemd-run](https://freedesktop.org/software/systemd/man/latest/systemd-run.html), [systemd.resource-control](https://freedesktop.org/software/systemd/man/latest/systemd.resource-control.html), [systemd.exec](https://freedesktop.org/software/systemd/man/latest/systemd.exec).
- Runtime-проверка transient scope и фактических cgroup controls в текущей среде не пройдена: доступ к пользовательской systemd-шине запрещён, а `/sys/fs/cgroup` смонтирован read-only. На поддерживаемой пользовательской системе нужно подтвердить cgroup-поведение до закрытия задачи.
