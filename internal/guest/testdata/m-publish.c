/* M13 探针:真实客体监听 127.0.0.1:argv[1],等待一次外部连接,
 * 读取 "ping" 后回 "pong" 并输出 M13-OK。
 * socket/bind/listen 失败 10、accept 失败 20、收发失败 30。 */
#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

int main(int argc, char **argv) {
	if (argc != 2) return 2;
	int fd = socket(AF_INET, SOCK_STREAM, 0);
	if (fd < 0) return 10;
	struct sockaddr_in addr = {0};
	addr.sin_family = AF_INET;
	addr.sin_port = htons((unsigned short)atoi(argv[1]));
	addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	if (bind(fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) return 10;
	if (listen(fd, 4) < 0) return 10;
	int conn = accept(fd, 0, 0);
	if (conn < 0) return 20;
	char buffer[4];
	if (read(conn, buffer, 4) != 4) return 30;
	if (memcmp(buffer, "ping", 4) != 0) return 30;
	if (write(conn, "pong", 4) != 4) return 30;
	close(conn);
	close(fd);
	write(1, "M13-OK\n", 7);
	return 0;
}
