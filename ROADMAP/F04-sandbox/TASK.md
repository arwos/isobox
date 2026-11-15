# F04 — Bubblewrap sandbox

## Зависимости

- Требует: F01, F02, F03.
- Используется F05–F07 и F08.

## Задачи

- [x] F04-T01 — Собирать минимальный root и запускать команду через bwrap.
  - Шаги: автоматически предоставить базовые системные пути только для чтения; создать proc/dev/tmp и рабочий каталог согласно проверенной схеме; отделить аргументы приложения от аргументов isobox.
  - Приёмка: host root/home не становятся доступны целиком; каждый обязательный mount применён либо запуск отменён; аргументы после `--` передаются приложению без изменений; завершение и сигналы передаются с предсказуемым кодом.
  - Реализация и пользовательская runtime-приёмка завершены. Отчёт `/tmp/isobox-test-results/f04-base-runtime.pNmyWf/result.txt` на `6a781b2` подтверждает изоляцию root/runtime путей, очищенное окружение, read-only системные mounts, точную передачу argv/exit status, SIGTERM forwarding и fail-closed при отказе обязательного `/usr` bind.
- [x] F04-T02 — Подключать явные mounts и изолированный HOME.
  - Шаги: обработать только профильные host paths; смонтировать профильный HOME в фиксированный sandbox HOME; иначе создать приватный `~/.isobox/home/<app-id>/`, переиспользовать его по app-id и сохранить после выхода.
  - Приёмка: разные app-id получают разные каталоги; заданный путь профиля используется точно; права каталога исключают доступ других пользователей; неподдержанный mount блокирует запуск.
  - Первоначальная реализация прошла пользовательскую runtime-приёмку по отчёту `/tmp/isobox-test-results/f04-mount-home.5ylFUo/result.txt` на `6a781b2`; она проверяла прежнее разделение managed HOME по profile/app-id. Текущую схему `~/.isobox/home/<app-id>/` отслеживает F04-T08.
- [x] F04-T03 — Реализовать ручные GUI-разрешения профиля.
  - Шаги: отдельные grants для Wayland/X11, audio, GPU и desktop integration; не предоставлять полный D-Bus по умолчанию и не подключать порталы.
  - Приёмка: каждое разрешение можно включить независимо; выключенные socket/device/bus недоступны приложению.
  - Реализация и пользовательская runtime-приёмка завершены. Отчёт `/tmp/isobox-test-results/f04-gui-grants.4bDuLc/result.txt` на `0a567ca` подтверждает default-deny, независимые Wayland/X11/PipeWire/PulseAudio/GPU/session-bus grants, отказ до приложения для некорректных и недоступных разрешений, cleanup synthetic sockets и `runtime_acceptance=passed`.
- [x] F04-T04 — Задавать hostname sandbox по app-id.
  - Шаги: передавать в bubblewrap `--hostname isobox-<app-id>` в создаваемом UTS namespace; без app-id использовать `isobox`; ограничивать имя лимитом Linux в 64 байта.
  - Приёмка: аргументы bwrap содержат hostname для запуска с/без app-id; hostname хоста не меняется.
- [x] F04-T05 — Изолировать представление cgroup внутри sandbox.
  - Шаги: создавать cgroup namespace через `--unshare-cgroup`; подтвердить его доступность в rootless bwrap preflight; сохранять применение/проверку лимитов во внешнем host supervisor.
  - Приёмка: sandbox видит cgroup namespace; cgroup v2 limits проверяются host supervisor до открытия app gate; недоступный namespace блокирует запуск.
- [x] F04-T06 — Запускать приложение с cwd и PWD в HOME sandbox.
  - Шаги: устанавливать `--chdir /home/isobox` и `PWD=/home/isobox` для приложения и bootstrap helper-ов.
  - Приёмка: `getcwd()` и `PWD` приложения равны `/home/isobox`.
- [x] F04-T07 — Давать sandbox-пользователю имя `isobox`.
  - Шаги: не подключать host `/etc/passwd` и `/etc/group`; монтировать минимальные read-only account databases, где текущие эффективные UID/GID принадлежат `isobox` с HOME `/home/isobox`.
  - Приёмка: `id -u`/`id -g` сохраняют числовые IDs хоста; `whoami` возвращает `isobox`; passwd-запись указывает `/home/isobox`; host account entries не видны.
- [x] F04-T08 — Хранить managed HOME в `~/.isobox/home/<app-id>/`.
  - Шаги: заменить временное хранилище на пользовательский каталог, переиспользовать HOME по app-id вне зависимости от профиля; сохранить приоритет явного `home_path`.
  - Приёмка: managed HOME имеет режим `0700`, сохраняется между запусками, общий для одного app-id в разных профилях и отдельный для разных app-id.
