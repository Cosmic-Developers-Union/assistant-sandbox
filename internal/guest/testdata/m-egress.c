/* M09 探针:真实客体经 socket/connect/write/read 访问受策略约束的
 * 宿主 TCP 服务。argv[1]=IPv4, argv[2]=端口。
 * 去/回显成功 exit 0;socket 失败 10、connect 失败 20、回显失败 30。 */
#include <arpa/inet.h>
#include <netinet/in.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

int main(int argc, char **argv) {
	if (argc != 3) return 2;
	int fd = socket(AF_INET, SOCK_STREAM, 0);
	if (fd < 0) return 10;
	struct sockaddr_in addr = {0};
	addr.sin_family = AF_INET;
	addr.sin_port = htons((unsigned short)atoi(argv[2]));
	if (inet_pton(AF_INET, argv[1], &addr.sin_addr) != 1) return 2;
	if (connect(fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) return 20;
	if (write(fd, "ping", 4) != 4) return 30;
	char buffer[4];
	if (read(fd, buffer, 4) != 4) return 30;
	if (memcmp(buffer, "ping", 4) != 0) return 31;
	close(fd);
	return 0;
}
