package guest_test

import (
	"strings"
	"testing"
	"time"
)

// TestPython312AsyncIONetwork 验证 Q11:asyncio 计时、取消、TCP/UDP
// 与受控 TLS 服务,并确认空闲等待不忙等。
func TestPython312AsyncIONetwork(t *testing.T) {
	_, connection := python312Guest(t)
	machine := connection.VM()
	cert := python312Asset(t, "testdata/python312/q07-cert.pem", python312TLSCertDigest)
	key := python312Asset(t, "testdata/python312/q07-key.pem", python312TLSKeyDigest)
	writeGuestFile(t, machine, "/work/q07-cert.pem", cert, 0o644)
	writeGuestFile(t, machine, "/work/q07-key.pem", key, 0o600)

	script := `import asyncio, ssl, time

async def echo(reader, writer):
    data = await reader.read(100)
    writer.write(b"echo:" + data)
    await writer.drain()
    writer.close()
    await writer.wait_closed()

class UDPEcho(asyncio.DatagramProtocol):
    def connection_made(self, transport):
        self.transport = transport
    def datagram_received(self, data, addr):
        self.transport.sendto(b"udp:" + data, addr)

class UDPClient(asyncio.DatagramProtocol):
    def __init__(self, future):
        self.future = future
    def datagram_received(self, data, addr):
        if not self.future.done():
            self.future.set_result(data)

async def main():
    server = await asyncio.start_server(echo, "127.0.0.1", 34567)
    reader, writer = await asyncio.open_connection("127.0.0.1", 34567)
    writer.write(b"ping")
    await writer.drain()
    reply = await asyncio.wait_for(reader.read(100), 10)
    assert reply == b"echo:ping", reply
    writer.close()
    await writer.wait_closed()
    server.close()
    await server.wait_closed()
    print("tcp", flush=True)

    loop = asyncio.get_running_loop()
    transport, protocol = await loop.create_datagram_endpoint(UDPEcho, local_addr=("127.0.0.1", 34568))
    future = loop.create_future()
    client, client_protocol = await loop.create_datagram_endpoint(lambda: UDPClient(future), local_addr=("127.0.0.1", 0))
    client.sendto(b"ping", ("127.0.0.1", 34568))
    reply = await asyncio.wait_for(future, 10)
    assert reply == b"udp:ping", reply
    client.close()
    transport.close()
    print("udp", flush=True)

    server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    server_context.load_cert_chain(certfile="/work/q07-cert.pem", keyfile="/work/q07-key.pem")
    client_context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    client_context.load_verify_locations(cafile="/work/q07-cert.pem")
    tls_server = await asyncio.start_server(echo, "127.0.0.1", 34569, ssl=server_context)
    reader, writer = await asyncio.open_connection("127.0.0.1", 34569, ssl=client_context, server_hostname="guest.test")
    writer.write(b"secure")
    await writer.drain()
    reply = await asyncio.wait_for(reader.read(100), 10)
    assert reply == b"echo:secure", reply
    writer.close()
    await writer.wait_closed()
    tls_server.close()
    await tls_server.wait_closed()
    print("tls", flush=True)

    task = asyncio.create_task(asyncio.sleep(10))
    await asyncio.sleep(0.01)
    task.cancel()
    try:
        await task
    except asyncio.CancelledError:
        pass
    else:
        raise AssertionError("cancellation failed")
    print("cancel", flush=True)

    start = time.process_time()
    await asyncio.sleep(0.5)
    used = time.process_time() - start
    assert used < 0.3, used
    print("idle", used, flush=True)
    print("Q11-OK", flush=True)

asyncio.run(main())
`
	output, code := runPython312(t, connection, script, 360*time.Second)
	if code != 0 || !strings.HasSuffix(output, "Q11-OK\n") {
		t.Fatalf("asyncio 用例 exit=%d output=%q", code, output)
	}
}
