# F04 Контекст

- Bubblewrap создаёт пустой mount namespace/root и собирает доступы из опций запуска. Это низкоуровневый механизм; изоляционную политику полностью задаёт вызывающий isobox.
- Базовые системные runtime-каталоги — read-only; нестандартные host paths только из профиля.
- `HOME` задаётся профилем как host path; при отсутствии создаётся в `/tmp` и сохраняется между запусками, отдельно для пары profile/app-id. `--app-id` обязателен только для такого управляемого HOME. Очистка `/tmp` ОС может удалить данные.
- GUI-доступ задаётся вручную и отдельно; полного D-Bus и portal integration нет по умолчанию.
- F04-T01 собирает root только из `/usr` (обязательный) и существующих `/bin`, `/sbin`, `/lib`, `/lib64`; доверенные каталоги сертификатов и отдельные runtime-файлы `/etc` подключаются read-only. Весь `/etc`, host root и host home не монтируются. Любой выбранный `--ro-bind` остаётся обязательным для bwrap: ошибка bind завершает запуск.
- В sandbox создаются отдельные `/proc`, `/dev`, `/tmp`, `/run`, `/var/tmp`; рабочий каталог фиксирован на `/`. Окружение очищается и получает только внутренние `PATH`, `HOME=/tmp`, `TMPDIR=/tmp`, `PWD=/`. Это временный HOME до F04-T02. `--unshare-user`, `--unshare-pid`, `--unshare-ipc`, `--unshare-uts`, `--unshare-net` обязательны; поэтому базовый режим сети здесь `off`.
- Профиль с явным HOME/mounts, GUI-разрешениями или сетью кроме `off` пока отклоняется до F04-T02/F04-T03/F05/F06. isobox не молча игнорирует уже заявленные разрешения и не запускает такой профиль с иной семантикой.
- Bubblewrap получает команду отдельными argv после `--`. `--new-session` отключает controlling-terminal ioctl TIOCSTI, который bwrap рекомендует блокировать seccomp-ом в обычной сессии. Для управления signal lifecycle isobox получает PID дочерней команды через `--json-status-fd`, пересылает INT/TERM/HUP/QUIT её process group, а `--die-with-parent` завершает sandbox при гибели isobox. bwrap возвращает shell-совместимый exit status, включая `128+signal`.
- Источники: [bubblewrap README](https://github.com/containers/bubblewrap/blob/main/README.md), [bubblewrap man page](https://github.com/containers/bubblewrap/blob/main/bwrap.xml), [bubblewrap implementation](https://github.com/containers/bubblewrap/blob/main/bubblewrap.c).
- Необязательное системное desktop integration — F08, не часть минимальной bwrap-команды.
