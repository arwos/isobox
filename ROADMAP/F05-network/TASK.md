# F05 — Режимы сети и публикация портов

## Зависимости

- Требует: F01, F02, F03, F04.
- Proxy mode и SOCKS client описаны в F06.

## Задачи

- [!] F05-T01 — Реализовать сетевой режим `off` с host-to-app портами.
  - Шаги: закрыть исходящий доступ из контейнера к Интернету, LAN и хосту; открыть только объявленные host bind address/port; сохранить ответы уже принятых соединений; проверить выбранную архитектуру для TCP и UDP.
  - Приёмка: приложение не может самостоятельно начать TCP/UDP/DNS/ICMP-запрос; хост достигает объявленный listener; любой не объявленный порт закрыт; если egress нельзя гарантированно закрыть, запуск отменяется.
  - Реализация: pasta принимает только явные адреса/порты, отключены автоматические и обратные forwarding-правила и bypass через splice. bwrap удерживает приложение до PID readiness-файла pasta; procfs ownership-проверка выполняется дополнительно, если доступна. Внутренний helper устанавливает default-drop nftables для input/output/forward, оставляет loopback, established ответы и новые входящие TCP/UDP только на объявленные порты; затем сбрасывает capability и запрещает приложению создавать namespace. Если firewall, helper, socket readiness или pasta недоступны, приложение не запускается.
  - Ограничение backend: для `off` с опубликованными портами пока выбирается только pasta. slirp4netns не используется, пока не подтверждена эквивалентность его host-forward пути с фильтром; без pasta запуск завершается ошибкой.
  - Статус `[!]`: `off` без объявленных портов прошёл runtime-проверку запрета host-loopback, внешних TCP/UDP и DNS. Для `off` с портами установленный pasta проходит capability preflight, но live harness завершается до приложения на `nft -f -`: `netlink: Error: cache initialization failed: Operation not permitted`. Fail-closed подтверждён; применение nft правил, host ingress и ответы на TCP/UDP соединения ещё не приняты. Нужен хост, где nft доступен в rootless network namespace. Детали в `QA.md`.
- [!] F05-T02 — Реализовать `direct` через rootless backend и явный port forwarding.
  - Шаги: предпочитать pasta; задавать forwarding только из профиля; применять отдельные TCP/UDP правила, оба протокола при неуказанном protocol, single/range ports и обязательный host bind address.
  - Приёмка: прямой режим доступен приложению; wildcard bind не выбирается неявно; портовая конфигурация одинакова при выборе поддерживаемого fallback backend; не объявленные порты не открыты.
  - Реализация: pasta конфигурирует direct namespace, отключает auto/reverse port forwarding, применяет только host forwards из профиля и готовит DNS через read-only `/etc/resolv.conf`. Если pasta не подходит, slirp4netns используется с `ready-fd`; host forwards добавляются отдельными API-запросами по каждому порту. Его host-forward API IPv4-only, поэтому IPv6 host bind без подходящего pasta блокирует запуск.
  - Статус `[!]`: slirp4netns 1.2.1 прошёл воспроизводимую runtime-приёмку: внешний IPv4 TCP/UDP и DNS (включая профильный override), полный TCP/UDP диапазон с bind на `127.0.0.1`, и отказ на не объявленном порту. Pasta capability options проходят parser probes, но меж backend runtime-проверка не выполнена; у хоста нет IPv6 default route, поэтому внешний IPv6 путь не проверен. Нужен хост с успешно запускающейся pasta и IPv6 route для полного закрытия; детали в `QA.md` и `source/README.md`.
