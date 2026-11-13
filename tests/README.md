# Тестовый набор isobox

Скрипты запускают временные sandbox-процессы от обычного пользователя. Не запускайте их через `sudo`. Результат каждого прогона сохраняется отдельно в `/tmp/isobox-test-results/`; для разбора пришлите файл `result.txt` из напечатанной папки результата.

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
