# F05 — Режимы сети и публикация портов

## Зависимости

- Требует: F01, F02, F03, F04.
- Proxy mode и SOCKS client описаны в F06.

## Задачи

- [~] F05-T01 — Реализовать сетевой режим `off` с host-to-app портами.
  - Шаги: закрыть исходящий доступ из контейнера к Интернету, LAN и хосту; открыть только объявленные host bind address/port; сохранить ответы уже принятых соединений; проверить выбранную архитектуру для TCP и UDP.
  - Приёмка: приложение не может самостоятельно начать TCP/UDP/DNS/ICMP-запрос; хост достигает объявленный listener; любой не объявленный порт закрыт; если egress нельзя гарантированно закрыть, запуск отменяется.
  - Реализация: pasta принимает только явные адреса/порты, отключены автоматические и обратные forwarding-правила и bypass через splice. bwrap удерживает приложение до готовности pasta. Внутренний helper устанавливает default-drop nftables для input/output/forward, оставляет loopback, established ответы и новые входящие TCP/UDP только на объявленные порты; затем сбрасывает capability и запрещает приложению создавать namespace. Если firewall, helper, socket readiness или pasta недоступны, приложение не запускается.
  - Ограничение backend: для `off` с опубликованными портами пока выбирается только pasta. slirp4netns не используется, пока не подтверждена эквивалентность его host-forward пути с фильтром; без pasta запуск завершается ошибкой.
  - Статус `[~]`: код и сборка готовы, но runtime-проверки egress/ingress для TCP и UDP ещё не выполнены; до них приёмка не считается завершённой.
- [~] F05-T02 — Реализовать `direct` через rootless backend и явный port forwarding.
  - Шаги: предпочитать pasta; задавать forwarding только из профиля; применять отдельные TCP/UDP правила, оба протокола при неуказанном protocol, single/range ports и обязательный host bind address.
  - Приёмка: прямой режим доступен приложению; wildcard bind не выбирается неявно; портовая конфигурация одинакова при выборе поддерживаемого fallback backend; не объявленные порты не открыты.
  - Реализация: pasta конфигурирует direct namespace, отключает auto/reverse port forwarding, применяет только host forwards из профиля и готовит DNS через read-only `/etc/resolv.conf`. Если pasta не подходит, slirp4netns используется с `ready-fd`; host forwards добавляются отдельными API-запросами по каждому порту. Его host-forward API IPv4-only, поэтому IPv6 host bind без подходящего pasta блокирует запуск.
  - Статус `[~]`: backend lifecycle, DNS wiring и явный forwarding собраны и проходят Go build; live acceptance TCP/UDP, DNS, IPv6 и host-port конфликтов ещё не выполнялась.
