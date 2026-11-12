# F04 Контекст

- Bubblewrap создаёт пустой mount namespace/root и собирает доступы из опций запуска. Это низкоуровневый механизм; изоляционную политику полностью задаёт вызывающий isobox.
- Базовые системные runtime-каталоги — read-only; нестандартные host paths только из профиля.
- `HOME` задаётся профилем как host path; при отсутствии создаётся в `/tmp` и сохраняется между запусками, отдельно для пары profile/app-id. `--app-id` обязателен только для такого управляемого HOME. Очистка `/tmp` ОС может удалить данные.
- GUI-доступ задаётся вручную и отдельно; полного D-Bus и portal integration нет по умолчанию.
- Источники: [bubblewrap README](https://github.com/containers/bubblewrap/blob/main/README.md), [bubblewrap man page](https://github.com/containers/bubblewrap/blob/main/bwrap.xml).
- Необязательное системное desktop integration — F08, не часть минимальной bwrap-команды.
