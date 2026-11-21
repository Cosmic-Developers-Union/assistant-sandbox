/* T03 输出预算探针:写 6 字节后等待 stdin 一个字节,便于外部
 * 观察指标;第二次写应因 Output 配额耗尽失败(EAGAIN)。
 * 首写失败 10、阻塞读失败 30、第二次写意外成功 20、errno 不符 21。 */
#include <errno.h>
#include <unistd.h>

int main(void) {
	if (write(1, "abcdef", 6) != 6) return 10;
	char byte;
	if (read(0, &byte, 1) != 1) return 30;
	errno = 0;
	if (write(1, "abcdef", 6) != -1) return 20;
	if (errno != EAGAIN) return 21;
	return 0;
}
