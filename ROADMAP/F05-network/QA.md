# F05 QA

## Планируемые сценарии

- `off`: исходящие TCP/UDP, DNS, IPv4/IPv6 и доступ к host/LAN не проходят; объявленный host port принимает соединение; ответы разрешены только в его рамках.
- `off`: попытки использовать не объявленный host port или соседнее устройство завершаются отказом.
- `direct`: внешний TCP/UDP работает; проверки доступны только объявленные host bind address и диапазоны.
- Убедиться, что pasta auto forwarding отключён; приложение не получает не объявленные входящие порты.
- Сравнить семантику direct mode на pasta/slirp; отказать в fallback при отличии IP/protocol/bind behavior.
- Проверить занятую host-пару адрес/порт, диапазон с конфликтом и низкий порт без возможности bind: точная ошибка, приложение не стартует.
- Проверить отсутствие утечки при отключении backend во время запуска.
- `direct`: внешние IPv4/IPv6 TCP/UDP и profile DNS работают на pasta и slirp; DNS host-loopback и profile override работают без чтения host `/etc/resolv.conf` приложением.
- `direct`: TCP-only, UDP-only, both, одиночный порт и диапазон проходят через выбранный backend; host bind address совпадает с профилем; конфликт bind/API отменяет запуск до приложения.

## Выполнение

- `gofmt -w internal/cli/cli.go internal/preflight/backend.go internal/preflight/preflight.go internal/sandbox/build.go internal/sandbox/forward_linux.go internal/sandbox/network.go internal/sandbox/run_linux.go internal/sandbox/run_other.go internal/sandbox/slirp_linux.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — прошло; GVM вывел предупреждение о недоступном stat cache в `/home/user/.gvm/.cache`, на сборку это не повлияло.
- `git diff --check` — прошло.
- Проверка `pasta --help` подтвердила DNS и PID options, но не показала все explicit network/forwarding options, которые требует preflight; такой pasta не будет выбран. `slirp4netns --help` показывает configure, IPv6, readiness, exit и API socket options.
- Runtime-сценарии выше не выполнялись. Для T01 ingress и egress-фильтр, а для T02 direct TCP/UDP/DNS и pasta/slirp forwarding остаются не подтверждены; обе задачи оставлены `[~]`.
