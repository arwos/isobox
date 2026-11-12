# F07 Контекст

- Согласованные лимиты первой версии: CPU, память, число процессов и открытых файлов.
- Запрошенный, но неприменимый или ослабленный лимит блокирует запуск приложения.
- Кандидат-механизмы: systemd resource control (`CPUQuota`, `MemoryMax`, `TasksMax`) и rlimit-параметр open files; официальные документы: [systemd.resource-control](https://freedesktop.org/software/systemd/man/latest/systemd.resource-control.html), [systemd.exec](https://freedesktop.org/software/systemd/man/latest/systemd.exec).
- Эти механизмы не выбраны окончательно: проверить cgroup v2, user systemd availability и эффективный parent limit на целевых системах. Не считать `RLIMIT_AS` эквивалентом memory cgroup limit.
