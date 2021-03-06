// +build android linux

/*
 * Copyright (c) 2014, Psiphon Inc.
 * All rights reserved.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package psiphon

import (
	"errors"
	dns "github.com/Psiphon-Inc/dns"
	"net"
	"os"
	"syscall"
	"time"
)

const DNS_PORT = 53

// LookupIP resolves a hostname. When BindToDevice is not required, it
// simply uses net.LookupIP.
// When BindToDevice is required, LookupIP explicitly creates a UDP
// socket, binds it to the device, and makes an explicit DNS request
// to the specified DNS resolver.
func LookupIP(host string, config *DialConfig) (addrs []net.IP, err error) {
	if config.BindToDeviceProvider != nil {
		return bindLookupIP(host, config)
	}
	return net.LookupIP(host)
}

// bindLookupIP implements the BindToDevice LookupIP case.
// To implement socket device binding, the lower-level syscall APIs are used.
// The sequence of syscalls in this implementation are taken from:
// https://code.google.com/p/go/issues/detail?id=6966
func bindLookupIP(host string, config *DialConfig) (addrs []net.IP, err error) {

	// When the input host is an IP address, echo it back
	ipAddr := net.ParseIP(host)
	if ipAddr != nil {
		return []net.IP{ipAddr}, nil
	}

	socketFd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, ContextError(err)
	}
	defer syscall.Close(socketFd)

	// TODO: check BindToDevice result
	config.BindToDeviceProvider.BindToDevice(socketFd)

	// config.BindToDeviceDnsServer must be an IP address
	ipAddr = net.ParseIP(config.BindToDeviceDnsServer)
	if ipAddr == nil {
		return nil, ContextError(errors.New("invalid IP address"))
	}

	// TODO: IPv6 support
	var ip [4]byte
	copy(ip[:], ipAddr.To4())
	sockAddr := syscall.SockaddrInet4{Addr: ip, Port: DNS_PORT}
	// Note: no timeout or interrupt for this connect, as it's a datagram socket
	err = syscall.Connect(socketFd, &sockAddr)
	if err != nil {
		return nil, ContextError(err)
	}

	// Convert the syscall socket to a net.Conn, for use in the dns package
	file := os.NewFile(uintptr(socketFd), "")
	defer file.Close()
	conn, err := net.FileConn(file)
	if err != nil {
		return nil, ContextError(err)
	}

	// Set DNS query timeouts, using the ConnectTimeout from the overall Dial
	if config.ConnectTimeout != 0 {
		conn.SetReadDeadline(time.Now().Add(config.ConnectTimeout))
		conn.SetWriteDeadline(time.Now().Add(config.ConnectTimeout))
	}

	// Make the DNS query
	// TODO: make interruptible?
	dnsConn := &dns.Conn{Conn: conn}
	defer dnsConn.Close()
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(host), dns.TypeA)
	query.RecursionDesired = true
	dnsConn.WriteMsg(query)

	// Process the response
	response, err := dnsConn.ReadMsg()
	if err != nil {
		return nil, ContextError(err)
	}
	addrs = make([]net.IP, 0)
	for _, answer := range response.Answer {
		if a, ok := answer.(*dns.A); ok {
			addrs = append(addrs, a.A)
		}
	}
	return addrs, nil
}
