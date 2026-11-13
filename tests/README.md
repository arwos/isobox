# Тестовый набор isobox

Скрипты запускают временные sandbox-процессы от обычного пользователя. Не запускайте их через `sudo`. Результат каждого прогона сохраняется отдельно в `/tmp/isobox-test-results/`; для разбора пришлите файл `result.txt` из напечатанной папки результата.

Запуск через общий диспетчер:

```sh
./tests/run.sh --list
./tests/run.sh host-info
```

Передайте имя сценария из списка, чтобы запустить его; параметры сценария можно указать после имени. Каждый запуск печатает путь к `result.txt`, который можно приложить к сообщению. Сначала удобно выполнить `host-info`, а затем запускать нужные проверки по одной.

## Сведения о хосте и smoke-тест

- `./tests/host-info/run.sh` — собрать версии инструментов, сведения о user namespaces, cgroup v2, TUN, пользовательском systemd и Podman. Скрипт не меняет настройки системы и не отправляет сетевые запросы.
- `./tests/host-info/run.sh --smoke` — дополнительно собрать isobox и запустить `/usr/bin/true` в sandbox с отключённой сетью. Запуск безопасен и не требует `sudo`; неудача оставит подробный вывод для диагностики.

Оба сценария сохраняют полный отчёт в отдельном `result.txt`. В режиме `--smoke` сборка Go может скачать отсутствующие модули. Можно задать каталог отчётов через `ISOBOX_TEST_RESULTS` и путь к бинарнику через `ISOBOX_TEST_BINARY`.

## Bwrap sandbox

- `./tests/run.sh f04-base-runtime` — проверяет изоляцию root и системных путей, `/proc`/`/dev`/tmpfs, очистку окружения, точные argv и exit status, пересылку SIGTERM и fail-closed при ошибке обязательного `/usr` bind.
- `./tests/run.sh f04-gui-grants` — создаёт временные локальные сокеты и отдельно проверяет Wayland, X11, PipeWire, PulseAudio, GPU и session-bus grants; по умолчанию GUI-ресурсы должны отсутствовать. Runner использует свободный display номер в `/tmp/.X11-unix` и удаляет созданный socket.
- `./tests/run.sh f04-mount-home` — проверяет явные read-only/writable mounts, точный сохраняемый `home_path`, разделение управляемого HOME по профилю и `--app-id`, режим `0700` и fail-closed отказ для недоступного mount.
- Нужны Go, Python 3, `bwrap`, пользовательские namespaces и настоящий `/tmp` со sticky bit. Запускайте от обычного пользователя, не через `sudo`.

Для сетевых проверок нужны Linux, Go, Python 3, bubblewrap и доступные пользовательские namespaces. В зависимости от сценария нужны `pasta`, `slirp4netns` и `nft`. Полные direct-тесты проверяют внешний IPv4 DNS/TCP/UDP, поэтому требуют доступа в Интернет.

## Сценарии F05

- `./tests/f05-network/pasta/run.sh` — direct через pasta, DNS, IPv4 TCP/UDP, порты и отсутствие IPv6.
- `./tests/f05-network/slirp4netns/run.sh` — те же проверки через slirp4netns, плюс режим `off` без опубликованных портов.
- `./tests/f05-network/auto-fallback/run.sh` — искусственно ломает pasta и проверяет переход на slirp4netns с сохранением правил.
- `./tests/f05-network/off-published/run.sh` — egress-фильтр режима `off` и входящие TCP/UDP порты из профиля.
- `./tests/f05-network/profile-validation/run.sh` — проверяет, что IPv6 DNS и bind-адреса отклоняются до запуска приложения.

## Лимиты ресурсов

- `./tests/f07-limits/run.sh` — проверяет CPU, память, процессы и потоки, открытые файлы, отказ запуска без `systemd-run` и удаление временного systemd scope.
- Этот runner также проверяет, что сочетание `off` с опубликованным портом и `process_count` сохраняет точный лимит задач приложения после firewall setup.
- Нужны `systemd-run`, пользовательский systemd manager и cgroup v2. Проверка сочетания с опубликованным портом дополнительно требует `pasta`, `nft` и `systemctl`; если этих сетевых инструментов нет, сценарий отдельно сообщает `SKIP`.

Чтобы проверить только локальные namespace и forwarding без внешнего Интернета, передайте `--local-only` в один из первых трёх сценариев, например:

```sh
./tests/f05-network/pasta/run.sh --local-only
```

Результаты пишутся в каталоги вида `/tmp/isobox-test-results/pasta-direct.XXXXXX/`. В них находятся `result.txt`, `metadata.txt`, `build.log` и `output.txt`. Можно задать свои пути переменными `ISOBOX_TEST_RESULTS` и `ISOBOX_TEST_BINARY`.

В `tests/f05-network/fixtures/` лежат UNIC-профили, используемые проверкой валидации, и примеры разрешённых IPv4 профилей. Сетевые и resource-limit acceptance-сценарии создают временные профили и отдельный HOME.

## Прокси SOCKS5

- `./tests/f06-proxy/client/run.sh` — SOCKS5 handshake, no-auth и username/password, TCP `CONNECT`, UDP `ASSOCIATE`, IPv4/domain адреса и fail-closed ошибки. Используется встроенный тестовый сервер, поэтому root, Podman и внешний Интернет не требуются.
- `./tests/f06-proxy/client/run.sh --race` — тот же набор с Go race detector.

Оба запуска создают отдельную папку `f06-proxy-client.XXXXXX` в каталоге результатов и печатают путь к `result.txt`. Пришлите этот файл; при ошибке приложите также `test.log`. Эти проверки пока не проходят через TUN из реального приложения; для этого нужен отдельный runtime acceptance после подключения F06 proxy к `isobox run`.
