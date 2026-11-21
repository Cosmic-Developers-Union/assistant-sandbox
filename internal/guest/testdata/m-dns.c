/* M10 DNS 探针:真实 musl 客体经 getaddrinfo 解析 A 记录。
 * 成功解析 libc.test → 192.0.2.7 且 missing.test 报 EAI_NONAME 时
 * 输出 M10-OK 并 exit 0;否则用不同退出码定位。 */
#include <arpa/inet.h>
#include <netdb.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>

int main(void) {
	struct addrinfo hints = {0}, *result = NULL;
	hints.ai_family = AF_INET;
	hints.ai_socktype = SOCK_STREAM;
	if (getaddrinfo("libc.test", "80", &hints, &result) != 0 || !result) return 10;
	struct sockaddr_in *addr = (struct sockaddr_in *)result->ai_addr;
	char text[INET_ADDRSTRLEN];
	if (!inet_ntop(AF_INET, &addr->sin_addr, text, sizeof(text))) return 11;
	if (strcmp(text, "192.0.2.7") != 0) return 12;
	freeaddrinfo(result);
	result = NULL;
	if (getaddrinfo("missing.test", "80", &hints, &result) != EAI_NONAME) return 13;
	printf("M10-OK\n");
	return 0;
}
