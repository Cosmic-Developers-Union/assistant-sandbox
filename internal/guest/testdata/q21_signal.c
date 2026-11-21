/* Q21 信号探针:安装 SIGTERM 处理器,报告就绪后等待信号并正常退出。
 * 输出 ready 与 signal 证明 child_process.kill 的信号到达客体进程。 */
#include <signal.h>
#include <stdio.h>
#include <unistd.h>

static volatile sig_atomic_t received;

static void handle(int number) { (void)number; received = 1; }

int main(void) {
	struct sigaction action = {0};
	action.sa_handler = handle;
	sigemptyset(&action.sa_mask);
	sigaction(SIGTERM, &action, 0);
	printf("ready\n");
	fflush(stdout);
	while (!received) pause();
	printf("signal\n");
	return 0;
}
