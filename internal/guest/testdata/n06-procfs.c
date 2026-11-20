/* N06/N07 探针:真实客体程序读取 /proc/self 的动态视图。
 * 构建:zig cc -static -target riscv64-linux-musl -O2 -s \
 *   -o n06-procfs n06-procfs.c
 * 断言码:10 打开 status;11 pid 不符;12 exe;13 cwd;14 maps;15 fd;
 * 16 cmdline。成功输出 N06-OK。 */
#define _GNU_SOURCE
#include <dirent.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>

static int read_file(const char *path, char *buffer, size_t size) {
	int fd = open(path, O_RDONLY);
	if (fd < 0) return -1;
	ssize_t n = read(fd, buffer, size - 1);
	close(fd);
	if (n < 0) return -1;
	buffer[n] = 0;
	return (int)n;
}

int main(void) {
	char buffer[8192];
	if (read_file("/proc/self/status", buffer, sizeof(buffer)) < 0) return 10;
	char expect[64];
	snprintf(expect, sizeof(expect), "Pid:\t%d\n", (int)getpid());
	if (!strstr(buffer, expect)) return 11;
	memset(buffer, 0, sizeof(buffer));
	if (readlink("/proc/self/exe", buffer, sizeof(buffer) - 1) < 0) return 12;
	if (strcmp(buffer, "/probe") != 0) return 12;
	memset(buffer, 0, sizeof(buffer));
	if (readlink("/proc/self/cwd", buffer, sizeof(buffer) - 1) < 0) return 13;
	if (strcmp(buffer, "/work") != 0) return 13;
	if (read_file("/proc/self/maps", buffer, sizeof(buffer)) < 0) return 14;
	if (!strstr(buffer, "r-x")) return 14;
	DIR *dir = opendir("/proc/self/fd");
	if (!dir) return 15;
	int found = 0;
	struct dirent *entry;
	while ((entry = readdir(dir)) != NULL) {
		if (strcmp(entry->d_name, "0") == 0 || strcmp(entry->d_name, "1") == 0 ||
		    strcmp(entry->d_name, "2") == 0)
			found++;
	}
	closedir(dir);
	if (found < 2) return 15;
	if (read_file("/proc/self/cmdline", buffer, sizeof(buffer)) < 0) return 16;
	if (strncmp(buffer, "/probe", 6) != 0) return 16;
	printf("N06-OK pid=%d\n", (int)getpid());
	return 0;
}
