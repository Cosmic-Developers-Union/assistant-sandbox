package guest_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/Cosmic-Developers-Union/assistant-sandbox/instance"
	vmnet "github.com/Cosmic-Developers-Union/assistant-sandbox/vm/net"
)

// testCA 是 M11 的测试证书颁发机构;只有它的 PEM 会装入客体 CA 存储。
type testCA struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	pem         []byte
}

// newTestCA 生成一个 ECDSA P-256 测试 CA。
func newTestCA(t *testing.T, name string, serial int64) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{
		certificate: certificate,
		key:         key,
		pem:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issueServer 由测试 CA 签发一个服务器证书。
func (ca *testCA) issueServer(t *testing.T, dnsNames []string, notBefore, notAfter time.Time, serial int64) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dnsNamesOnly := make([]string, 0, len(dnsNames))
	var ipAddresses []net.IP
	for _, name := range dnsNames {
		if ip := net.ParseIP(name); ip != nil {
			ipAddresses = append(ipAddresses, ip)
		} else {
			dnsNamesOnly = append(dnsNamesOnly, name)
		}
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNamesOnly,
		IPAddresses:  ipAddresses,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// startTLSServer 在宿主回环启动一个 TLS 服务;客户端完成校验后
// 收到 "secure\n"。
func startTLSServer(t *testing.T, certificate tls.Certificate) uint16 {
	t.Helper()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = connection.Close() }()
				_ = connection.SetDeadline(time.Now().Add(20 * time.Second))
				_, _ = io.WriteString(connection, "secure\n")
			}()
		}
	}()
	return uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // 内核分配端口在 uint16 范围
}

// startGuestHTTPS 在宿主回环启动 HTTPS 服务,供受控包索引使用。
func startGuestHTTPS(t *testing.T, certificate tls.Certificate, handler http.Handler) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 20 * time.Second}
	server.SetKeepAlivesEnabled(false)
	tlsListener := tls.NewListener(listener, &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(tlsListener) }()
	return uint16(listener.Addr().(*net.TCPAddr).Port) //nolint:gosec // 内核分配端口在 uint16 范围
}

// tlsEgressPolicy 允许 DNS 与四个 TLS 端口;四个客体端口分别映射到
// 宿主上使用不同证书的测试服务。
func tlsEgressPolicy(dnsPort uint16, ports map[uint16]uint16) *vmnet.EgressPolicy {
	policy := vmnet.NewEgressPolicy(
		vmnet.EgressRule{
			Protocol: vmnet.ProtocolUDP,
			Prefix:   netip.MustParsePrefix(guestGateway + "/32"),
			PortMin:  53,
			PortMax:  53,
		},
		vmnet.EgressRule{
			Protocol: vmnet.ProtocolTCP,
			Prefix:   netip.MustParsePrefix(guestGateway + "/32"),
			PortMin:  443,
			PortMax:  446,
		},
	)
	policy.PortMap = map[uint16]uint16{53: dnsPort}
	for guestPort, hostPort := range ports {
		policy.PortMap[guestPort] = hostPort
	}
	return policy
}

// installGuestCA 把 CA 写入客体系统证书存储,并返回 Python 使用的
// SSL_CERT_FILE 环境配置。
func installGuestCA(t *testing.T, machine *instance.VM, certificate []byte) map[string]string {
	t.Helper()
	for _, name := range []string{"/etc/ssl/certs/ca-certificates.crt", "/etc/ssl/cert.pem"} {
		writeGuestFile(t, machine, name, certificate, 0o644)
	}
	return map[string]string{"SSL_CERT_FILE": "/etc/ssl/certs/ca-certificates.crt"}
}
