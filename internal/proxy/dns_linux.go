//go:build linux

/*
 *  Copyright (c) 2026 Mikhail Knyazhev <markus621@yandex.com>. All rights reserved.
 *  Use of this source code is governed by a MIT license that can be found in the LICENSE file.
 */

package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"

	"github.com/arwos/isobox/internal/profile"
)

const dnsExchangeTimeout = 5 * time.Second

type dnsQuestion struct {
	name  string
	type_ uint16
	class uint16
	end   int
}

func (tunnel *Tunnel) serveDNSUDP(local *gonet.UDPConn) {
	defer func() { _ = local.Close() }()
	stopClose := context.AfterFunc(tunnel.ctx, func() { _ = local.Close() })
	defer stopClose()
	buffer := make([]byte, 4096)
	for {
		_ = local.SetReadDeadline(time.Now().Add(tunnelUDPIdleTimeout))
		n, err := local.Read(buffer)
		if err != nil {
			return
		}
		query := append([]byte(nil), buffer[:n]...)
		ctx, cancel := context.WithTimeout(tunnel.ctx, tunnel.dnsQueryTimeout())
		response, err := tunnel.resolveDNS(ctx, query, false)
		cancel()
		if err != nil {
			continue
		}
		_ = local.SetWriteDeadline(time.Now().Add(dnsExchangeTimeout))
		if _, err := local.Write(response); err != nil {
			return
		}
	}
}

func (tunnel *Tunnel) serveDNSTCP(local net.Conn) {
	defer func() { _ = local.Close() }()
	stopClose := context.AfterFunc(tunnel.ctx, func() { _ = local.Close() })
	defer stopClose()
	for {
		_ = local.SetDeadline(time.Now().Add(tunnelUDPIdleTimeout))
		var size [2]byte
		if _, err := io.ReadFull(local, size[:]); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(size[:]))
		if length < 12 || length > 4096 {
			return
		}
		query := make([]byte, length)
		if _, err := io.ReadFull(local, query); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(tunnel.ctx, tunnel.dnsQueryTimeout())
		response, err := tunnel.resolveDNS(ctx, query, true)
		cancel()
		if err != nil {
			response, err = refusedDNSResponse(query)
		}
		if err != nil {
			return
		}
		if len(response) > 65535 {
			return
		}
		binary.BigEndian.PutUint16(size[:], uint16(len(response)))
		if _, err := local.Write(size[:]); err != nil {
			return
		}
		if _, err := local.Write(response); err != nil {
			return
		}
	}
}

func (tunnel *Tunnel) resolveDNS(ctx context.Context, query []byte, tcpTransport bool) ([]byte, error) {
	question, err := parseDNSQuestion(query)
	if err != nil {
		return nil, err
	}
	if tunnel.mode == profile.NetworkModeEgress && question.class == 1 && question.name == "host.gateway.internal" {
		if question.type_ == 1 {
			return staticAResponse(query, question, []net.IP{net.ParseIP(tunnel.hostGatewayEndpoint()).To4()}), nil
		}
		// The host gateway is IPv4-only. Answer other record types locally so
		// libc's AAAA lookup does not escape to an upstream DNS server.
		return staticEmptyResponse(query, question), nil
	}
	if question.class == 1 {
		if addresses := tunnel.dnsRecords[question.name]; len(addresses) != 0 {
			switch question.type_ {
			case 1:
				return staticAResponse(query, question, addresses), nil
			case 28:
				return staticEmptyResponse(query, question), nil
			}
		}
	}
	upstreams := tunnel.configuredDNSAddresses()
	if len(upstreams) == 0 {
		tunnel.logDNSFailure(question, tcpTransport, "no upstream DNS is configured")
		return refusedDNSResponse(query)
	}
	protocol := profile.PortProtocolUDP
	if tcpTransport {
		protocol = profile.PortProtocolTCP
	}
	var lastErr error
	for _, upstream := range upstreams {
		destination, err := tunnel.destination(upstream, 53, protocol)
		if err != nil {
			lastErr = err
			continue
		}
		var response []byte
		if tcpTransport {
			response, err = tunnel.exchangeDNSTCP(ctx, destination, query)
		} else {
			response, err = tunnel.exchangeDNSUDP(ctx, destination, query)
		}
		if err != nil {
			lastErr = err
			continue
		}
		if len(response) < 12 {
			lastErr = fmt.Errorf("upstream DNS response is shorter than the DNS header")
			continue
		}
		responseCode := binary.BigEndian.Uint16(response[2:4]) & 0x000f
		answerCount := binary.BigEndian.Uint16(response[6:8])
		if responseCode == 2 || responseCode == 5 {
			lastErr = fmt.Errorf("upstream %s returned DNS rcode=%d", upstream, responseCode)
			tunnel.logDNSFailure(question, tcpTransport, lastErr.Error())
			continue
		}
		if responseCode != 0 || answerCount == 0 {
			tunnel.logDNSFailure(question, tcpTransport, fmt.Sprintf("upstream %s response rcode=%d answers=%d", upstream, responseCode, answerCount))
		}
		return response, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("all upstream DNS servers failed")
	}
	tunnel.logDNSFailure(question, tcpTransport, lastErr.Error())
	return nil, lastErr
}

func (tunnel *Tunnel) dnsQueryTimeout() time.Duration {
	count := len(tunnel.configuredDNSAddresses())
	if count < 1 {
		count = 1
	}
	return dnsExchangeTimeout * time.Duration(count)
}

func (tunnel *Tunnel) logDNSFailure(question dnsQuestion, tcpTransport bool, reason string) {
	if tunnel == nil || tunnel.mode != profile.NetworkModeEgress {
		return
	}
	transport := "udp"
	if tcpTransport {
		transport = "tcp"
	}
	tunnel.eventLog.logf("egress DNS unresolved domain=%q qtype=%d transport=%s reason=%q", question.name, question.type_, transport, reason)
}

func (tunnel *Tunnel) exchangeDNSUDP(ctx context.Context, destination string, query []byte) ([]byte, error) {
	if tunnel.client == nil {
		connection, err := tunnel.dialContext(ctx, "udp4", destination)
		if err != nil {
			return nil, err
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(dnsExchangeTimeout))
		if _, err := connection.Write(query); err != nil {
			return nil, err
		}
		buffer := make([]byte, 4096)
		n, err := connection.Read(buffer)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), buffer[:n]...), nil
	}
	association, err := tunnel.client.UDPAssociate(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = association.Close() }()
	if err := association.SendTo(destination, query); err != nil {
		return nil, err
	}
	_ = association.SetReadDeadline(time.Now().Add(dnsExchangeTimeout))
	buffer := make([]byte, 4096)
	for {
		n, source, err := association.ReadFrom(buffer)
		if err != nil {
			return nil, err
		}
		if source == destination {
			return append([]byte(nil), buffer[:n]...), nil
		}
	}
}

func (tunnel *Tunnel) exchangeDNSTCP(ctx context.Context, destination string, query []byte) ([]byte, error) {
	connection, err := tunnel.dialContext(ctx, "tcp4", destination)
	if err != nil {
		return nil, err
	}
	defer func() { _ = connection.Close() }()
	_ = connection.SetDeadline(time.Now().Add(dnsExchangeTimeout))
	var size [2]byte
	binary.BigEndian.PutUint16(size[:], uint16(len(query)))
	if _, err := connection.Write(size[:]); err != nil {
		return nil, err
	}
	if _, err := connection.Write(query); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(connection, size[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(size[:]))
	if length < 12 || length > 65535 {
		return nil, fmt.Errorf("upstream DNS returned an invalid TCP response length")
	}
	response := make([]byte, length)
	if _, err := io.ReadFull(connection, response); err != nil {
		return nil, err
	}
	return response, nil
}

func parseDNSQuestion(message []byte) (dnsQuestion, error) {
	if len(message) < 12 || binary.BigEndian.Uint16(message[4:6]) != 1 || binary.BigEndian.Uint16(message[2:4])&0x8000 != 0 {
		return dnsQuestion{}, fmt.Errorf("invalid DNS query")
	}
	name, end, err := parseDNSName(message, 12)
	if err != nil || end+4 > len(message) {
		return dnsQuestion{}, fmt.Errorf("invalid DNS question")
	}
	normalized := "."
	if name != "." {
		normalized, err = profile.NormalizeDomain(name)
		if err != nil {
			return dnsQuestion{}, err
		}
	}
	return dnsQuestion{name: normalized, type_: binary.BigEndian.Uint16(message[end : end+2]), class: binary.BigEndian.Uint16(message[end+2 : end+4]), end: end + 4}, nil
}

func parseDNSName(message []byte, offset int) (string, int, error) {
	labels := make([]string, 0, 8)
	position, end := offset, -1
	visited := make(map[int]struct{})
	for steps := 0; steps < 128; steps++ {
		if position >= len(message) {
			return "", 0, fmt.Errorf("truncated DNS name")
		}
		length := int(message[position])
		if length&0xc0 == 0xc0 {
			if position+1 >= len(message) {
				return "", 0, fmt.Errorf("truncated DNS compression pointer")
			}
			pointer := (length&0x3f)<<8 | int(message[position+1])
			if pointer >= len(message) {
				return "", 0, fmt.Errorf("invalid DNS compression pointer")
			}
			if end < 0 {
				end = position + 2
			}
			if _, exists := visited[pointer]; exists {
				return "", 0, fmt.Errorf("cyclic DNS compression pointer")
			}
			visited[pointer] = struct{}{}
			position = pointer
			continue
		}
		if length&0xc0 != 0 || length > 63 {
			return "", 0, fmt.Errorf("invalid DNS label length")
		}
		position++
		if length == 0 {
			if end < 0 {
				end = position
			}
			return stringsJoin(labels), end, nil
		}
		if position+length > len(message) {
			return "", 0, fmt.Errorf("truncated DNS label")
		}
		labels = append(labels, string(message[position:position+length]))
		position += length
	}
	return "", 0, fmt.Errorf("DNS name has too many compression pointers")
}

func stringsJoin(labels []string) string {
	if len(labels) == 0 {
		return "."
	}
	result := labels[0]
	for _, label := range labels[1:] {
		result += "." + label
	}
	return result
}

func staticAResponse(query []byte, question dnsQuestion, addresses []net.IP) []byte {
	response := make([]byte, 12+question.end-12, 12+question.end-12+len(addresses)*16)
	copy(response[:2], query[:2])
	flags := binary.BigEndian.Uint16(query[2:4])&0x0100 | 0x8080 | 0x0400
	binary.BigEndian.PutUint16(response[2:4], flags)
	binary.BigEndian.PutUint16(response[4:6], 1)
	binary.BigEndian.PutUint16(response[6:8], uint16(len(addresses)))
	copy(response[12:], query[12:question.end])
	for _, address := range addresses {
		response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		response = append(response, address.To4()...)
	}
	return response
}

func staticEmptyResponse(query []byte, question dnsQuestion) []byte {
	response := make([]byte, 12+question.end-12)
	copy(response[:2], query[:2])
	flags := binary.BigEndian.Uint16(query[2:4])&0x0100 | 0x8080 | 0x0400
	binary.BigEndian.PutUint16(response[2:4], flags)
	binary.BigEndian.PutUint16(response[4:6], 1)
	copy(response[12:], query[12:question.end])
	return response
}

func refusedDNSResponse(query []byte) ([]byte, error) {
	question, err := parseDNSQuestion(query)
	if err != nil {
		return nil, err
	}
	response := make([]byte, 12+question.end-12)
	copy(response[:2], query[:2])
	flags := binary.BigEndian.Uint16(query[2:4])&0x0100 | 0x8005
	binary.BigEndian.PutUint16(response[2:4], flags)
	binary.BigEndian.PutUint16(response[4:6], 1)
	copy(response[12:], query[12:question.end])
	return response, nil
}
