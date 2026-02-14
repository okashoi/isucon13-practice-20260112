package main

import (
	"log"
	"net"

	"github.com/miekg/dns"
)

func startDNSServer() {
	ip := net.ParseIP(powerDNSSubdomainAddress)
	if ip == nil {
		log.Fatalf("invalid ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS: %s", powerDNSSubdomainAddress)
	}

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Authoritative = true

		for _, q := range r.Question {
			switch q.Qtype {
			case dns.TypeA:
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeA,
						Class:  dns.ClassINET,
						Ttl:    0,
					},
					A: ip,
				})
			case dns.TypeNS:
				m.Answer = append(m.Answer, &dns.NS{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeNS,
						Class:  dns.ClassINET,
						Ttl:    0,
					},
					Ns: "ns1.t.isucon.pw.",
				})
			case dns.TypeSOA:
				m.Answer = append(m.Answer, &dns.SOA{
					Hdr: dns.RR_Header{
						Name:   q.Name,
						Rrtype: dns.TypeSOA,
						Class:  dns.ClassINET,
						Ttl:    0,
					},
					Ns:      "ns1.t.isucon.pw.",
					Mbox:    "hostmaster.t.isucon.pw.",
					Serial:  1,
					Refresh: 10800,
					Retry:   3600,
					Expire:  604800,
					Minttl:  3600,
				})
			}
		}

		w.WriteMsg(m)
	})

	server := &dns.Server{
		Addr:    ":53",
		Net:     "udp",
		Handler: handler,
	}

	log.Printf("starting DNS server on :53")
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("failed to start DNS server: %v", err)
	}
}
