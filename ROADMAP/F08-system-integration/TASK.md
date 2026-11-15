# F08 — Необязательная системная интеграция

## Зависимости

- Требует: F02, F04.
- Интеграция не является обязательной для `isobox run`.

## Задачи

- [x] F08-T01 — Добавить пользовательские desktop launchers.
  - Шаги: формировать/обновлять launcher для пользователя без root и не менять профиль/HOME при удалении.
  - Приёмка: launcher запускает isobox с выбранным профилем и app-id; при отключённой интеграции базовый CLI работает.
  - Реализация: `isobox integration desktop install/remove` управляет пользовательскими `.desktop` файлами. Системные/user settings layers, ранее реализованные рядом с launcher, удалены задачей F02-T03.
  - Результат: live-приёмка в пользовательской X11-сессии подтвердила запуск установленного `.desktop` через `gio launch`, передачу spaced profile path и argv с зарезервированными символами, работу базового CLI без launcher/settings, приоритет XDG system/user overrides и безопасное удаление launcher без изменения профиля или HOME. Повторяемая проверка: `source/README.md`.
  - Обновление 2026-11-15: live-результаты выше относятся к прежнему поведению settings layers. Launcher install/remove остаются в силе.
- [x] F08-T02 — Устанавливать и удалять пользовательские systemd units для профиля.
  - Шаги: добавить `isobox integration systemd install --profile=PATH --app-id=ID`, `enable/disable/remove --app-id=ID`; требовать непустой `command` в профиле; устанавливать unit в XDG user unit directory, запускать `isobox run` без argv после `--`.
  - Приёмка: unit запускает профильную команду; install не включает/запускает его; enable/disable переключают автозапуск без start/stop текущего сервиса; remove останавливает/отключает только unit, управляемый isobox; чужие файлы не заменяются и не удаляются.
