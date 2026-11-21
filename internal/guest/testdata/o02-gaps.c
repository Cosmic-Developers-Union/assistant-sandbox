/* Q02 缺口探针:argv[1]=file 打开缺失文件;argv[1]=syscall 调用
 * mincore(232, ENOTSUP)。成功表示缺口已按预期暴露。
 * 参数错误 2、文件行为意外 10/11、系统调用行为意外 12。 */
#include <errno.h>
#include <fcntl.h>
#include <string.h>
#include <sys/syscall.h>
#include <unistd.h>

int main(int argc, char **argv) {
	if (argc != 2) return 2;
	if (strcmp(argv[1], "file") == 0) {
		int fd = open("/work/missing-gap", O_RDONLY);
		if (fd >= 0) {
			close(fd);
			return 10;
		}
		return errno == ENOENT ? 0 : 11;
	}
	if (strcmp(argv[1], "syscall") == 0) {
		long result = syscall(232, 0, 0, 0);
		return (result == -1 && errno == ENOTSUP) ? 0 : 12;
	}
	return 2;
}
