# F08 Контекст

- Системная интеграция необязательна; базовый rootless CLI не требует system service или root.
- Пользователь согласовал desktop launcher и дополнительные системные defaults, которые пользователь может переопределить.
- Эти defaults не являются принудительными ограничениями безопасности. Security boundary задаётся профилем запуска и проверками F03–F07.
- Desktop entry: freedesktop Desktop Entry 1.5, `Type=Application`, статический `Exec` без shell и без динамических field codes; literal `%` экранируется как `%%`. Аргументы с зарезервированными символами заключаются в кавычки с двумя слоями escaping. Новые строки и другие control characters в значениях/аргументах отвергаются с ошибкой.
- Команда установки: `isobox integration install --id=ID --name=NAME --profile=PATH --app-id=ID [--icon=ICON] [--terminal] -- APP [ARGS...]`; повторный install обновляет только launcher с маркером isobox. `isobox integration remove --id=ID` удаляет только принадлежащий пользователю обычный файл с этим маркером.
- Launchers хранятся в `$XDG_DATA_HOME/applications/isobox-ID.desktop`; при пустом/относительном `XDG_DATA_HOME` используется `~/.local/share`. Создаваемые файлы имеют mode `0600`; каталог должен быть обычным каталогом пользователя и не быть доступен на запись другим пользователям/группам. Root и desktop portals не используются.
- Системные defaults читаются из первого существующего `$XDG_CONFIG_DIRS/isobox/settings.unic`; при пустом `XDG_CONFIG_DIRS` используется `/etc/xdg`. Пользовательский файл — `$XDG_CONFIG_HOME/isobox/settings.unic`, default `~/.config/isobox/settings.unic`. Если home пользователя отсутствует и XDG_CONFIG_HOME не задан, optional user layer пропускается.
- Merge order: встроенный `network_mode=off` → системный UNIC override → пользовательский UNIC override → явные `profile.network.mode` и `profile.network.dns`. Overrides применяются по полям, поэтому пользователь может переопределить только выбранные системные значения. Ошибка/неизвестное поле в существующем settings файле диагностируется и останавливает `run`; defaults не являются admin policy и не блокируют явные значения профиля.
- Settings пример:

  ```unic
  version 1;
  defaults {
      network_mode direct;
      dns '1.1.1.1';
  }
  ```

- Использованы официальные спецификации: [Desktop Entry 1.5](https://specifications.freedesktop.org/desktop-entry/latest-single/) и [XDG Base Directory](https://specifications.freedesktop.org/basedir/). `Exec` не запускается через shell; executable разрешается в абсолютный путь при создании launcher.
- Runtime-проверка видимости launcher в desktop menu и его запуска из desktop session остаётся открытой.
