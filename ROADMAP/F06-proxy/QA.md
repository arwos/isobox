# F06 QA

## Планируемые сценарии

- SOCKS5 no-auth и username/password: успешный и неуспешный handshake; секреты не попадают в stdout/stderr/log/argv.
- TCP CONNECT к IPv4 и domain; IPv6 address type и IPv6 TUN packets отклоняются; прокси отказал, недоступен или завершился во время потока — direct connection не появляется.
- UDP ASSOCIATE для DNS и произвольной UDP datagram; проверка ответа и тихого drop; прокси без UDP не запускает profile.
- DNS общего настроечного resolver и профильного resolver проходит только через SOCKS; при packet capture на хосте прямого DNS нет.
- QUIC-like UDP flow проходит SOCKS UDP; TCP/UDP отказ прокси не вызывает direct fallback.
- ICMP/raw socket отклоняются; sandbox TUN не поднимает IPv6 маршрут.
- TUN fd закрывается после выхода; аварийный exit proxy process оставляет egress закрытым.
- Podman sing-box fixture покрывает TCP, UDP, auth и отказ relay; runtime может запускаться без sing-box.
- Plain SOCKS5 показывает явное предупреждение; файл secrets имеет mode `0600`.

## Выполнение

Проверки не запускались: это планирование, реализации ещё нет.
