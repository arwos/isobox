# isobox

`isobox` запускает Linux-приложения в изолированной среде на базе [bubblewrap](https://github.com/containers/bubblewrap). Профиль задаёт доступ к файлам, сети, графической сессии и ресурсам; команду приложения и аргументы передаёт пользователь.

## Возможности

- Минимальная файловая среда с системными каталогами только для чтения и явными профильно заданными mounts.
- Изолированный `HOME`: существующий каталог можно указать в профиле; иначе сохраняемый каталог создаётся под `/tmp` и привязывается к `--app-id`.
- Сетевые режимы `off`, `direct` и `proxy`. Сеть работает только по IPv4; опубликованы могут быть только явно заданные входящие порты.
- Proxy mode использует SOCKS5 для TCP и UDP. SOCKS5 не шифрует учётные данные или трафик.
- Отдельные разрешения Wayland, X11, audio, GPU и пользовательской session D-Bus.
- Ограничения CPU, памяти, числа задач и открытых файлов, если их поддерживает хост.
- Необязательные пользовательские desktop launchers и общие настройки по XDG.

## Требования

- Linux с включёнными user namespaces.
- Go `1.26.8` для сборки из исходников.
- `bubblewrap` (`bwrap`); для сетевых режимов — `pasta` или совместимый `slirp4netns`. Требуемые возможности зависят от режима и профиля.
- Для `off` с опубликованными портами нужен `pasta` и `nft`. Для proxy mode требуется доступный TUN и настроенный SOCKS5 сервер. Для лимитов CPU/памяти/процессов нужны cgroup v2 и пользовательский `systemd-run`.

Некоторые возможности зависят от настроек ядра, пользовательской сессии и установленных программ. isobox не устанавливает системные зависимости и прекращает запуск, если не может применить обязательное ограничение.

## Сборка

```sh
git clone https://github.com/arwos/isobox.git
cd isobox
go build -o isobox ./cmd/isobox
```

Проверьте доступность CLI:

```sh
./isobox help
```

## Быстрый старт

Создайте файл `minimal.unic`:

```unic
version 1;

network {
  mode off;
}
```

Запустите приложение, передав его команду после `--`:

```sh
./isobox run --profile=./minimal.unic --app-id=demo -- /usr/bin/true
```

Все аргументы после `--` передаются приложению как есть. Профиль не содержит команду приложения. Без `--app-id` можно запускать профиль с заданным `home_path`; если профиль не задаёт `home_path`, укажите стабильный `--app-id`, чтобы использовать отдельный сохраняемый между запусками каталог под `/tmp`. Содержимое `/tmp` может быть удалено системой.

## Профиль

Файл профиля использует формат UNIC. Пример разрешает приложению чтение каталога `/opt/my-app/data`, задаёт отдельный `HOME` и включает только Wayland:

```unic
version 1;
home_path '/home/alice/.local/share/my-app';

mount {
  source '/opt/my-app/data';
  target '/opt/my-app/data';
  read_only true;
}

network {
  mode off;
}

gui {
  wayland true;
}
```

Доступные поля профиля:

| Блок | Поля |
| --- | --- |
| Корень | `version` (сейчас `1`), `home_path` |
| `mount` | `source`, `target`, `read_only` (по умолчанию `true`); блок можно повторять |
| `network` | `mode`, `dns`, `proxy_alias`, повторяемый `port` |
| `network.port` | `host_address` (IPv4), `port`, необязательный `end_port`, `protocol` (`tcp`, `udp` или `both`) |
| `gui` | `wayland`, `x11`, `audio`, `gpu`, `desktop` (по умолчанию выключены) |
| `limits` | `cpu_quota_percent`, `memory_bytes`, `process_count`, `open_files` |

Режим сети по умолчанию — `off`. `direct` предоставляет IPv4-сеть через rootless backend. `proxy` требует `proxy_alias` и SOCKS5 настройки. Значения DNS и общие сетевые defaults можно задать в `settings.unic`; профильные значения имеют приоритет. Неизвестные поля и неподдержанные значения приводят к ошибке валидации.

Пример публикации одного TCP-порта на loopback хоста:

```unic
network {
  mode off;
  port {
    host_address '127.0.0.1';
    port 8080;
    protocol tcp;
  }
}
```

Сетевые разрешения не расширяются автоматически: в частности, не объявленные входящие порты закрыты, IPv6 отключён.

## SOCKS5

Добавьте alias прокси. При `userpass` программа запросит имя и пароль в терминале; они не передаются через аргументы командной строки.

```sh
./isobox proxy add --alias=work --server=proxy.example:1080 --auth=userpass
./isobox proxy list
```

Укажите alias в профиле:

```unic
network {
  mode proxy;
  proxy_alias 'work';
}
```

Хранилище прокси находится в пользовательской конфигурации и ограничено правами доступа. SOCKS5 не шифрует логин, пароль или трафик приложения: используйте только доверенную сеть или защищённый транспорт к прокси. Удаление alias: `./isobox proxy remove --alias=work`.

## Общие настройки и desktop launcher

Необязательный файл пользовательских настроек находится в `$XDG_CONFIG_HOME/isobox/settings.unic` (по умолчанию `~/.config/isobox/settings.unic`). Например:

```unic
version 1;

defaults {
  network_mode off;
  dns '1.1.1.1';
}
```

Можно установить или удалить пользовательский desktop launcher:

```sh
./isobox integration install \
  --id=my-app --name='My App' --profile=/home/alice/.config/isobox/my-app.unic \
  --app-id=my-app -- /usr/bin/my-app
./isobox integration remove --id=my-app
```

## Проверки

Go-проверки из корня репозитория:

```sh
go test ./...
go vet ./...
go build ./...
```

Host и runtime acceptance-сценарии доступны через `./tests/run.sh --list`. Запускайте их от обычного пользователя, не через `sudo`; часть сценариев требует сети, графической сессии, Podman или пользовательского systemd/cgroup. Требования и описание сценариев приведены в [`tests/README.md`](tests/README.md).

## Лицензия

MIT. См. [`LICENSE`](LICENSE).
