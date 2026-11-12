# F05 QA

## Планируемые сценарии

- `off`: исходящие TCP/UDP, DNS, IPv4/IPv6 и доступ к host/LAN не проходят; объявленный host port принимает соединение; ответы разрешены только в его рамках.
- `off`: попытки использовать не объявленный host port или соседнее устройство завершаются отказом.
- `direct`: внешний TCP/UDP работает; проверки доступны только объявленные host bind address и диапазоны.
- Убедиться, что pasta auto forwarding отключён; приложение не получает не объявленные входящие порты.
- Сравнить семантику direct mode на pasta/slirp; отказать в fallback при отличии IP/protocol/bind behavior.
- Проверить занятую host-пару адрес/порт, диапазон с конфликтом и низкий порт без возможности bind: точная ошибка, приложение не стартует.
- Проверить отсутствие утечки при отключении backend во время запуска.

## Выполнение

- `gofmt -w internal/preflight/backend.go internal/preflight/preflight.go internal/sandbox/build.go internal/sandbox/off_guard_linux.go internal/sandbox/off_guard_other.go internal/sandbox/forward_linux.go internal/sandbox/run_linux.go internal/sandbox/run_other.go cmd/isobox/main.go internal/cli/cli.go` — прошло.
- `GOCACHE=/tmp/isobox-gocache GOMODCACHE=/tmp/isobox-gomodcache GOPATH=/tmp/isobox-gopath go build ./...` — прошло; GVM вывел предупреждение о недоступном stat cache в `/home/user/.gvm/.cache`, на сборку это не повлияло.
- `git diff --check` — прошло.
- Runtime-сценарии выше не выполнялись. Поэтому ingress для TCP/UDP, отсутствие исходящих TCP/UDP/DNS/ICMP, отказ на не объявленных портах, nft capability и остановка pasta остаются не подтверждены; задача оставлена `[~]`.
