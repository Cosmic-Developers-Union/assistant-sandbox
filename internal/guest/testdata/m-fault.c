/* T02 崩溃探针:向未映射地址 0 写入,触发 SIGSEGV。
 * 正常运行不应返回;若返回则 exit 10。 */
int main(void) {
	volatile int *address = 0;
	*address = 1;
	return 10;
}
