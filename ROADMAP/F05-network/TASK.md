# F05 — Режимы сети и публикация портов

## Зависимости

- Требует: F01, F02, F03, F04.
- Proxy mode и SOCKS client описаны в F06.

## Задачи

- [x] F05-T01 — Реализовать сетевой режим `off` с host-to-app портами.
  - Шаги: закрыть исходящий доступ из контейнера к Интернету, LAN и хосту; открыть только объявленные host bind address/port; сохранить ответы уже принятых соединений; проверить выбранную архитектуру для TCP и UDP.
  - Приёмка: приложение не может самостоятельно начать TCP/UDP/DNS/ICMP-запрос; хост достигает объявленный listener; любой не объявленный порт закрыт; если egress нельзя гарантированно закрыть, запуск отменяется.
  - Реализация: pasta принимает только явные адреса/порты, отключены автоматические и обратные forwarding-правила и bypass через splice. После подготовки bwrap mounts helper создаёт network namespace с краткоживущим `CAP_SYS_ADMIN`, устанавливает default-drop nftables с `CAP_NET_ADMIN`, затем сбрасывает capabilities и запрещает приложению создавать namespace. Helper сообщает supervisor-у о готовности через временный private Unix socket; supervisor находит host TID этого netns, запускает pasta и только после PID/socket readiness выпускает приложение. Если firewall, helper, socket readiness или pasta недоступны, приложение не запускается.
  - Ограничение backend: для `off` с опубликованными портами пока выбирается только pasta. slirp4netns не используется, пока не подтверждена эквивалентность его host-forward пути с фильтром; без pasta запуск завершается ошибкой.
  - Статус `[x]`: live acceptance прошла на текущем rootless host. Проверены запрет host-listener и внешнего TCP, UDP/DNS, отказ в создании ICMP socket, доступ host к объявленному TCP+UDP порту с ответами и отказ TCP/UDP на соседнем не объявленном порту. Ограничения ранних запусков и полный вывод приведены в `QA.md`.
- [!] F05-T02 — Реализовать `direct` через rootless backend и явный port forwarding.
  - Шаги: предпочитать pasta; задавать forwarding только из профиля; применять отдельные TCP/UDP правила, оба протокола при неуказанном protocol, single/range ports и обязательный host bind address.
  - Приёмка: direct доступен приложению только по IPv4; IPv6 networking отключён; wildcard bind не выбирается неявно; портовая конфигурация одинакова для совместимых backend; не объявленные порты не открыты. Профильные IPv6 DNS и host bind адреса отклоняются до запуска.
  - Реализация: pasta конфигурирует direct namespace с `--ipv4-only`, отключает auto/reverse port forwarding и применяет только IPv4 host forwards из профиля. slirp4netns запускается без `--enable-ipv6`; host forwards добавляются отдельными API-запросами по каждому порту. DNS из host resolver фильтруется до IPv4; если IPv4 nameserver отсутствует, запуск завершается ошибкой.
  - Реализация: при совместимой IPv4-конфигурации preflight сохраняет slirp fallback; если pasta не запускается до открытия bwrap startup gate, isobox останавливает pasta и пытается поднять slirp. Для fallback заранее формируется read-only resolver с IPv4 DNS-адресами обоих backend. Если fallback тоже не поднимается, приложение не запускается.
  - Статус `[!]`: реализация и unit-проверки IPv4-only завершены, но live acceptance не может пройти на текущем хосте. Pasta сценарий остановлен host preflight: bwrap не может открыть NETLINK_ROUTE (`Operation not permitted`); slirp сценарий остановился при создании host IPv4 socket (`EPERM`) до запуска sandbox. Runtime acceptance нужно повторить на хосте, где rootless namespaces и обычные сокеты доступны.
