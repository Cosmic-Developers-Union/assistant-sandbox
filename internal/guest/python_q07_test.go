package guest_test

import (
	"testing"
	"time"
)

const (
	python312TLSCertDigest = "3f0a04419ca7cfaa7dff63b0b0591fdd838c987ed0020880e9029f4fc1f55174"
	python312TLSKeyDigest  = "a1268bae3c57472c5ab798a451cc5e2f2d3935e12e57ff6379c88980c4b8efe2"
)

// TestPython312NativeDependencies 验证 Q07:ssl、hashlib、zlib、sqlite3
// 在客体执行真实功能,不以导入成功或跳过代替。
func TestPython312NativeDependencies(t *testing.T) {
	_, connection := python312Guest(t)
	machine := connection.VM()
	cert := python312Asset(t, "testdata/python312/q07-cert.pem", python312TLSCertDigest)
	key := python312Asset(t, "testdata/python312/q07-key.pem", python312TLSKeyDigest)
	writeGuestFile(t, machine, "/work/q07-cert.pem", cert, 0o644)
	writeGuestFile(t, machine, "/work/q07-key.pem", key, 0o600)

	script := `import hashlib, hmac, os, sqlite3, ssl, zlib

assert hashlib.sha256(b"abc").hexdigest() == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
assert hashlib.blake2b(b"abc", digest_size=16).hexdigest() == "cf4ab791c62b8d2b2109c90275287816"
assert hmac.new(b"key", b"data", hashlib.sha256).hexdigest() == "5031fe3d989c6d1537a013fa6e739da23463fdaec3b70137d828e36ace221bd0"

payload = b"guest-zlib-" * 100
packed = zlib.compress(payload, 9)
assert packed != payload and zlib.decompress(packed) == payload
assert zlib.crc32(b"123456789") == 0xCBF43926
assert zlib.adler32(b"123456789") == 0x091E01DE

database = "/work/q07.sqlite3"
if os.path.exists(database):
    os.unlink(database)
connection = sqlite3.connect(database)
connection.execute("create table items (id integer primary key, name text not null)")
connection.executemany("insert into items (name) values (?)", [("alpha",), ("beta",)])
connection.commit()
assert connection.execute("select id, name from items order by id").fetchall() == [(1, "alpha"), (2, "beta")]
assert connection.execute("pragma integrity_check").fetchone() == ("ok",)
assert sqlite3.sqlite_version_info >= (3, 35, 0), sqlite3.sqlite_version
connection.close()
assert os.path.exists(database)

server_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
server_ctx.load_cert_chain(certfile="/work/q07-cert.pem", keyfile="/work/q07-key.pem")
client_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
client_ctx.load_verify_locations(cafile="/work/q07-cert.pem")
client_in, client_out = ssl.MemoryBIO(), ssl.MemoryBIO()
server_in, server_out = ssl.MemoryBIO(), ssl.MemoryBIO()
client = client_ctx.wrap_bio(client_in, client_out, server_hostname="guest.test")
server = server_ctx.wrap_bio(server_in, server_out, server_side=True)
client_done = server_done = False
for _ in range(64):
    if not client_done:
        try:
            client.do_handshake()
            client_done = True
        except ssl.SSLWantReadError:
            pass
    chunk = client_out.read()
    if chunk:
        server_in.write(chunk)
    if not server_done:
        try:
            server.do_handshake()
            server_done = True
        except ssl.SSLWantReadError:
            pass
    chunk = server_out.read()
    if chunk:
        client_in.write(chunk)
    if client_done and server_done:
        break
assert client_done and server_done, "TLS handshake did not complete"
certificate = client.getpeercert()
assert certificate["subject"] == ((("commonName", "guest.test"),),), certificate
assert ssl.OPENSSL_VERSION.startswith("OpenSSL "), ssl.OPENSSL_VERSION
client.write(b"ping")
server_in.write(client_out.read())
assert server.read(4) == b"ping"
server.write(b"pong")
client_in.write(server_out.read())
assert client.read(4) == b"pong"
os.unlink(database)
print("Q07-OK")
`
	output, code := runPython312(t, connection, script, 240*time.Second)
	if code != 0 || output != "Q07-OK\n" {
		t.Fatalf("原生依赖 exit=%d output=%q", code, output)
	}
}
